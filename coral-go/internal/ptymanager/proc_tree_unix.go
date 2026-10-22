//go:build !windows

package ptymanager

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"sync"
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

// treeMembers is every live process of the terminal whose shell is root and that carries tag: root and what descends from it, and what carries the tag wherever it now is. root counts only while
// it is still the process the terminal started (its pid and start time were recorded then): once the shell is gone its pid can belong to an unrelated process, which must not seed the tree. Never
// Coral's own process or group, and never PID 1.
func treeMembers(root member, tag string) []member {
	procs, err := process.Processes()
	if err != nil {
		return nil
	}
	self, selfGroup := os.Getpid(), syscall.Getpgrp()
	parent := make(map[int]int, len(procs))
	byPID := make(map[int]*process.Process, len(procs))
	for _, p := range procs {
		byPID[int(p.Pid)] = p
		if pp, err := p.Ppid(); err == nil {
			parent[int(p.Pid)] = int(pp)
		}
	}
	members := map[int]bool{}
	if root.pid > 1 && root.current() {
		if _, ok := parent[root.pid]; ok {
			members[root.pid] = true
		}
	}
	for grown := true; grown; {
		grown = false
		for pid, pp := range parent {
			if !members[pid] && members[pp] {
				members[pid] = true
				grown = true
			}
		}
	}
	want := runTagEnv + "=" + tag
	for pid, p := range byPID {
		if members[pid] {
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
		created, err := byPID[pid].CreateTime()
		if err != nil {
			continue
		}
		out = append(out, member{pid, created})
	}
	return out
}

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
		if !m.current() {
			continue
		}
		_ = syscall.Kill(m.pid, sig)
		if g, err := syscall.Getpgid(m.pid); err == nil && g == m.pid {
			_ = syscall.Kill(-m.pid, sig)
		}
	}
}

// killTree ends known and what else is found of the terminal's tree. A process can fork between the sweep that found the tree and the signal that ends it, and its child would be missed, so the
// tree is first stopped (SIGSTOP: nothing in it can fork), swept again, and stopped again until a sweep finds nothing new, and only then killed; a stopped process dies of SIGKILL like any other.
// It returns what it killed.
func killTree(known []member, root member, tag string) []member {
	set := unionOf(known, treeMembers(root, tag))
	for i := 0; i < 5; i++ {
		signalTree(set, syscall.SIGSTOP)
		next := unionOf(set, treeMembers(root, tag))
		if len(next) == len(set) {
			break
		}
		set = next
	}
	signalTree(set, syscall.SIGKILL)
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

// seen is what Terminate found of the terminal's tree: once the shell is gone, what it started has been reparented and descent no longer finds it.
type seen struct {
	mu sync.Mutex
	ms []member
}

func (s *seen) add(ms []member) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ms = unionOf(s.ms, ms)
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
