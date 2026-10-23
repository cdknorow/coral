//go:build !windows

package ptymanager

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

// An agent's terminal is more than the shell Coral started in it. The shell runs the agent as a job, which job control puts in a process group of its own; the agent starts helpers that
// leave its session or its group (Codex's code-mode host does); and what a process starts and then outlives is handed to PID 1. Signalling the shell's group reached none of those: quitting
// Coral, or killing one agent, ended the shell and left the agent running, reparented to PID 1 (#6125).
//
// So the whole tree is ended, found two ways: by descent from the shell, and by a tag. Every process Coral starts in a terminal has CORAL_PTY_RUN=<tag> in its environment, which its children
// inherit however they detach (setsid, a double fork, a new group): a process of this terminal is any of our user's processes that carries the tag.

// runTagEnv names the environment variable that holds a terminal's tag.
const runTagEnv = "CORAL_PTY_RUN"

// newRunTag is a fresh random tag for one terminal.
func newRunTag() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return strings.ToLower(strings.ReplaceAll(time.Now().Format("20060102150405.000000000"), ".", ""))
	}
	return hex.EncodeToString(b)
}

// member is a process of a terminal, known by its pid and when it started, so a pid that has been given to another process since is not signalled.
type member struct {
	pid     int
	created int64
}

// current reports whether m is still the process it was when it was found.
func (m member) current() bool {
	p, err := process.NewProcess(int32(m.pid))
	if err != nil {
		return false
	}
	created, err := p.CreateTime()
	return err == nil && created == m.created
}

// procSnap is the process table at one moment: who is whose child, and a handle on each process. One scan serves every terminal that is asked about.
type procSnap struct {
	parent map[int]int
	byPID  map[int]*process.Process
}

func takeSnap() *procSnap {
	procs, err := process.Processes()
	if err != nil {
		return &procSnap{}
	}
	s := &procSnap{parent: make(map[int]int, len(procs)), byPID: make(map[int]*process.Process, len(procs))}
	for _, p := range procs {
		s.byPID[int(p.Pid)] = p
		if pp, err := p.Ppid(); err == nil {
			s.parent[int(p.Pid)] = int(pp)
		}
	}
	return s
}

// tree is every live process of the terminal whose shell is root and that carries tag: root and what descends from it, and what carries the tag wherever it now is. root counts only while it is
// still the process the terminal started (its pid and start time were recorded then): once the shell is gone its pid can belong to an unrelated process, which must not seed the tree. Never Coral's
// own process or group, and never PID 1.
func (s *procSnap) tree(root member, tag string) []member {
	self, selfGroup := os.Getpid(), syscall.Getpgrp()
	members := map[int]bool{}
	if root.pid > 1 && root.current() {
		if _, ok := s.parent[root.pid]; ok {
			members[root.pid] = true
		}
	}
	for grown := true; grown; {
		grown = false
		for pid, pp := range s.parent {
			if !members[pid] && members[pp] {
				members[pid] = true
				grown = true
			}
		}
	}
	want := runTagEnv + "=" + tag
	for pid, p := range s.byPID {
		if members[pid] || tag == "" { // no tag (a tmux pane has none): descent only
			continue
		}
		if env, err := p.Environ(); err == nil {
			for _, kv := range env {
				if kv == want {
					members[pid] = true
					break
				}
			}
		}
	}
	var out []member
	for pid := range members {
		if excluded(pid, self, selfGroup) {
			continue
		}
		created, err := s.byPID[pid].CreateTime()
		if err != nil {
			continue
		}
		out = append(out, member{pid, created})
	}
	return out
}

// treeMembers is the tree of one terminal, from a fresh scan.
func treeMembers(root member, tag string) []member { return takeSnap().tree(root, tag) }

// excluded: a process that is never part of a terminal's tree, whatever carries its tag: PID 1, Coral itself, and anything in Coral's own process group.
func excluded(pid, self, selfGroup int) bool {
	if pid <= 1 || pid == self {
		return true
	}
	g, err := syscall.Getpgid(pid)
	return err == nil && g == selfGroup
}

// signalTree sends sig to each member that is still the process it was and, for one that leads its own group, to the group too (what it started there and did not tag).
func signalTree(ms []member, sig syscall.Signal) {
	for _, m := range ms {
		k := killSeam()
		if !k.memberLive(m) {
			continue
		}
		k.sendSignal(m.pid, sig)
		if g, err := syscall.Getpgid(m.pid); err == nil && g == m.pid {
			k.sendSignal(-m.pid, sig)
		}
	}
}

// The seams the order of killTree's signals is tested through: how a signal is sent, how the tree is swept, whether a member is still the process found, and how many stop-and-sweep rounds there are.
// They are one value behind an atomic pointer, read through killSeam() once per use and replaced by a test with setKillSeam (kill_tree_order_test.go): a plain package variable a test assigns is a
// data race with a goroutine an earlier test left running (#5266, seams_census_test.go).
type killSeams struct {
	sendSignal func(pid int, sig syscall.Signal)
	sweepTree  func(root member, tag string) []member
	memberLive func(m member) bool
	stopRounds int
}

var killSeamsP atomic.Pointer[killSeams] // nil until a test sets its own

// defaultKillSeams is what runs in a program. It is a package variable and not set in an init(): the guardian's own init (guardian_unix.go) runs before main, and before the init of a file that sorts after
// it, and it ends a tree with these.
var defaultKillSeams = &killSeams{
	sendSignal: func(pid int, sig syscall.Signal) { _ = syscall.Kill(pid, sig) },
	sweepTree:  treeMembers,
	memberLive: func(m member) bool { return m.current() },
	stopRounds: 5,
}

// killSeam is the seams in force; read it once per use and keep the value.
func killSeam() *killSeams {
	if k := killSeamsP.Load(); k != nil {
		return k
	}
	return defaultKillSeams
}

// killTree ends known and what else is found of the terminal's tree. A process can fork between the sweep that found the tree and the signal that ends it, and its child would be missed, so the
// tree is first stopped (SIGSTOP: nothing in it can fork), swept again, and stopped again until a sweep finds nothing new, and only then killed; a stopped process dies of SIGKILL like any other.
// It returns what it killed.
func killTree(known []member, root member, tag string) []member {
	set := append([]member(nil), known...)
	// Whatever happens below, a stop is never left without its kill: the set is read when this runs, so a panic or an early return still ends what was stopped (SIGKILL ends a stopped process).
	defer func() { signalTree(set, syscall.SIGKILL) }()
	k := killSeam()
	set = unionOf(set, k.sweepTree(root, tag))
	for i := 0; i < k.stopRounds; i++ {
		signalTree(set, syscall.SIGSTOP)
		next := unionOf(set, k.sweepTree(root, tag))
		if len(next) == len(set) {
			break
		}
		set = next
	}
	return set
}

// anyAlive reports whether any of ms is still the process it was.
func anyAlive(ms []member) bool {
	for _, m := range ms {
		if err := syscall.Kill(m.pid, 0); (err == nil || err == syscall.EPERM) && m.current() {
			return true
		}
	}
	return false
}

// unionOf is a and the members of b that are not in a.
func unionOf(a, b []member) []member {
	seen := map[member]bool{}
	out := make([]member, 0, len(a)+len(b))
	for _, m := range append(append([]member{}, a...), b...) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// maxRemembered is the most members one terminal's remembered set holds: a terminal whose tree is larger than that (a build fanning out thousands of processes) keeps the first this many it found and
// the rest are found by the sweep at kill time. The cap is what keeps a runaway tree from growing Coral's memory or the time a kill takes.
const maxRemembered = 512

// mergeCapped is old and the members of add that are not in it, at most max of them; a member past the cap is dropped, and the second result says so.
func mergeCapped(old, add []member, max int) (merged []member, dropped bool) {
	merged = unionOf(old, nil)
	have := make(map[member]bool, len(merged))
	for _, m := range merged {
		have[m] = true
	}
	for _, m := range add {
		if have[m] {
			continue
		}
		if len(merged) >= max {
			dropped = true
			continue
		}
		have[m] = true
		merged = append(merged, m)
	}
	return merged, dropped
}

// stillThere is the members of ms that are still the processes they were.
func stillThere(ms []member) []member {
	out := ms[:0:0]
	for _, m := range ms {
		if m.current() {
			out = append(out, m)
		}
	}
	return out
}

// seen is what is remembered of the terminal's tree: what Terminate found, and what the sampler (descendant_sampler_unix.go) has seen since it started. Once the shell is gone, what it started has been
// reparented and descent no longer finds it; this is what still does.
type seen struct {
	mu     sync.Mutex
	ms     []member
	capped bool // a debug line was written for this set going past maxRemembered
}

// add remembers ms (up to the cap) and returns everything remembered.
func (s *seen) add(ms []member) []member {
	pruned := false
	for {
		s.mu.Lock()
		merged, dropped := mergeCapped(s.ms, ms, maxRemembered)
		if dropped && !pruned {
			// Members that have ended count against the cap until they are forgotten: a build that started and finished 512 processes between two looks must not push out a helper that is alive now (Security's SHOULD on #6183).
			s.mu.Unlock()
			s.prune()
			pruned = true
			continue
		}
		defer s.mu.Unlock()
		s.ms = merged
		if dropped && !s.capped {
			s.capped = true
			slog.Debug("[ptymanager] a terminal's process tree is past the remembered-set cap: the rest is found when it is killed", "cap", maxRemembered)
		}
		return append([]member(nil), s.ms...)
	}
}

// prune forgets members that have ended (or whose pid now belongs to another process).
func (s *seen) prune() {
	s.mu.Lock()
	ms := append([]member(nil), s.ms...)
	s.mu.Unlock()
	live := stillThere(ms)
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := map[member]bool{}
	for _, m := range live {
		keep[m] = true
	}
	out := s.ms[:0]
	for _, m := range s.ms {
		if keep[m] || !contains(ms, m) { // one added while this ran was not looked at: it stays
			out = append(out, m)
		}
	}
	s.ms = out
	if len(s.ms) < maxRemembered {
		s.capped = false
	}
}

func contains(ms []member, m member) bool {
	for _, x := range ms {
		if x == m {
			return true
		}
	}
	return false
}

func (s *seen) all() []member {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]member(nil), s.ms...)
}

// reapTree is called once the shell has exited. What the agent started may still be running, on the SIGTERM it was sent: it is given what is left of grace to end, then everything of the
// terminal still alive (what Terminate found, and what is found now) gets SIGKILL, and it is waited for briefly.
func (p *unixPTY) reapTree(grace time.Duration) {
	known := p.seen.all()
	deadline := time.Now().Add(grace)
	for anyAlive(known) && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if !anyAlive(unionOf(known, treeMembers(p.root, p.tag))) {
		return
	}
	left := killTree(known, p.root, p.tag)
	for end := time.Now().Add(time.Second); anyAlive(left) && time.Now().Before(end); {
		time.Sleep(10 * time.Millisecond)
	}
}
