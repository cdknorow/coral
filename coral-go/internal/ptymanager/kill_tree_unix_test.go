//go:build !windows

package ptymanager

import (
	"github.com/shirou/gopsutil/v4/process"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// #6125: ending an agent's terminal ends what the agent started, not only the shell it was started from. A real Codex was left running, reparented to PID 1, with its code-mode host, when Coral
// quit: the shell's process group was signalled, and the agent had a group of its own (job control) and helpers that had left its session. These use real processes on a real pty: a child that
// setsids (a helper in a session of its own, still its parent's child), one in a process group of its own, and one that double-forks and setsids (a daemon, reparented to PID 1: found by the tag in its
// environment, which the system must let Coral read: Linux does, macOS hides other processes' environment, so that one is skipped there).

func needPerl(t *testing.T) {
	t.Helper()
	canFork(t)
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("perl is needed to make a double-forking, setsid child")
	}
}

// pidFrom waits for the child to write its pid, and makes sure it is not left running if the test fails.
func pidFrom(t *testing.T, path string) int {
	t.Helper()
	var pid int
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if b, err := os.ReadFile(path); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n > 1 {
				pid = n
				break
			}
		}
	}
	if pid == 0 {
		t.Fatal("the child never reported its pid")
	}
	// only this recorded process, and only while it is still the one that reported itself (its start time, as the kill's own identity check): a failed run cannot leave a helper behind, and a pid that was
	// reused since is never signalled
	registered := false
	if p, err := process.NewProcess(int32(pid)); err == nil {
		if created, err := p.CreateTime(); err == nil {
			registered = true
			t.Cleanup(func() {
				if (member{pid, created}).current() {
					syscall.Kill(pid, syscall.SIGKILL)
				}
			})
		}
	}
	if !registered {
		t.Logf("helper %d: its start time could not be read, so nothing will clean it up if this test fails", pid) // Security's LOW on 33b7788fd
	}
	return pid
}

func waitGone(t *testing.T, pid int, within time.Duration) {
	t.Helper()
	for end := time.Now().Add(within); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
	}
	t.Fatalf("process %d is still running %v after its terminal was ended", pid, within)
}

// attached: a helper that starts its own session and stays its parent's child (the parent waits), as Codex's code-mode host does.
const attached = `perl -e 'use POSIX; if (!fork) { POSIX::setsid(); %s open F, ">%s"; print F $$; close F; exec "sleep", "600" } wait'`

// orphanTagReadable: whether this system lets a process read another's environment, which is how a process that left the tree before the kill is found.
func orphanTagReadable(t *testing.T) bool {
	t.Helper()
	c := exec.Command("sleep", "30")
	c.Env = append(c.Environ(), runTagEnv+"=probe")
	if err := c.Start(); err != nil {
		return false
	}
	defer func() { c.Process.Kill(); c.Wait() }()
	time.Sleep(50 * time.Millisecond)
	return len(treeMembers(member{}, "probe")) > 0
}

const daemon = `perl -e 'use POSIX; if (!fork) { POSIX::setsid(); if (!fork) { %s open F, ">%s"; print F $$; close F; exec "sleep", "600" } exit 0 } exit 0'`

func TestKillEndsAChildThatHasASessionOfItsOwn(t *testing.T) {
	needPerl(t)
	for _, ignoreTerm := range []bool{false, true} {
		pidFile := filepath.Join(t.TempDir(), "pid")
		ignore := ""
		if ignoreTerm {
			ignore = `$SIG{TERM} = "IGNORE"; $SIG{HUP} = "IGNORE";`
		}
		b := NewPTYBackend()
		t.Cleanup(func() { b.Close() })
		const name = "claude-00000000-0000-4000-8000-000000006124"
		cmd := strings.Replace(strings.Replace(attached, "%s", ignore, 1), "%s", pidFile, 1)
		if err := b.Spawn(name, "claude", t.TempDir(), "00000000-0000-4000-8000-000000006124", cmd, 80, 24); err != nil {
			t.Fatal(err)
		}
		pid := pidFrom(t, pidFile)
		if sid, _ := unix.Getsid(pid); sid != pid {
			t.Fatalf("the child should lead a session of its own (sid %d)", sid)
		}
		if err := b.KillWithin(name, 600*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		waitGone(t, pid, 3*time.Second)
	}
}

func TestKillEndsAChildThatSetsidsAndIsReparentedToPID1(t *testing.T) {
	needPerl(t)
	if !orphanTagReadable(t) {
		t.Skip("this system does not let Coral read another process's environment (macOS), so a process that left the tree before the kill cannot be found by its tag")
	}
	for _, ignoreTerm := range []bool{false, true} {
		pidFile := filepath.Join(t.TempDir(), "pid")
		ignore := ""
		if ignoreTerm {
			ignore = `$SIG{TERM} = "IGNORE"; $SIG{HUP} = "IGNORE";`
		}
		b := NewPTYBackend()
		t.Cleanup(func() { b.Close() })
		const name = "claude-00000000-0000-4000-8000-000000006125"
		cmd := strings.Replace(strings.Replace(daemon, "%s", ignore, 1), "%s", pidFile, 1) + "; exec sleep 600"
		if err := b.Spawn(name, "claude", t.TempDir(), "00000000-0000-4000-8000-000000006125", cmd, 80, 24); err != nil {
			t.Fatal(err)
		}
		pid := pidFrom(t, pidFile)
		if err := b.KillWithin(name, 600*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		waitGone(t, pid, 3*time.Second)
	}
}

func TestKillEndsAChildInAProcessGroupOfItsOwn(t *testing.T) {
	needPerl(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	b := NewPTYBackend()
	t.Cleanup(func() { b.Close() })
	const name = "claude-00000000-0000-4000-8000-000000006126"
	// what job control does to an agent run from an interactive shell: its own group, still the shell's child
	cmd := `perl -e 'setpgrp(0, 0); open F, ">` + pidFile + `"; print F $$; close F; exec "sleep", "600"' & wait`
	if err := b.Spawn(name, "claude", t.TempDir(), "00000000-0000-4000-8000-000000006126", cmd, 80, 24); err != nil {
		t.Fatal(err)
	}
	pid := pidFrom(t, pidFile)
	if g, err := syscall.Getpgid(pid); err != nil || g != pid {
		t.Fatalf("the child should lead a group of its own (pgid %d, err %v)", g, err)
	}
	if err := b.KillWithin(name, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	waitGone(t, pid, 3*time.Second)
}

// Quitting Coral closes every terminal: the backend's Close ends the whole tree of each.
func TestShutdownEndsWhatTheAgentStarted(t *testing.T) {
	needPerl(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	b := NewPTYBackend()
	const name = "claude-00000000-0000-4000-8000-000000006127"
	cmd := strings.Replace(strings.Replace(attached, "%s", "", 1), "%s", pidFile, 1)
	if err := b.Spawn(name, "claude", t.TempDir(), "00000000-0000-4000-8000-000000006127", cmd, 80, 24); err != nil {
		t.Fatal(err)
	}
	pid := pidFrom(t, pidFile)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	waitGone(t, pid, 8*time.Second)
}

// Another terminal's processes are not touched: only the tagged and the descended are ended.
func TestKillLeavesAnotherTerminalsProcessesAlone(t *testing.T) {
	needPerl(t)
	pidA, pidB := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	b := NewPTYBackend()
	t.Cleanup(func() { b.Close() })
	for i, f := range []string{pidA, pidB} {
		name := "claude-00000000-0000-4000-8000-00000000612" + strconv.Itoa(8+i)
		cmd := strings.Replace(strings.Replace(attached, "%s", "", 1), "%s", f, 1)
		if err := b.Spawn(name, "claude", t.TempDir(), name[7:], cmd, 80, 24); err != nil {
			t.Fatal(err)
		}
	}
	a, other := pidFrom(t, pidA), pidFrom(t, pidB)
	if err := b.KillWithin("claude-00000000-0000-4000-8000-000000006128", time.Second); err != nil {
		t.Fatal(err)
	}
	waitGone(t, a, 3*time.Second)
	if err := syscall.Kill(other, 0); err != nil {
		t.Fatalf("the other terminal's child was ended too: %v", err)
	}
}

// createdOf is the start time of pid, as treeMembers reads it.
func createdOf(t *testing.T, pid int) int64 {
	t.Helper()
	p, err := process.NewProcess(int32(pid))
	if err != nil {
		t.Fatal(err)
	}
	created, err := p.CreateTime()
	if err != nil {
		t.Fatal(err)
	}
	return created
}

// Never a process outside the terminal: Coral itself, its own process group, and PID 1 are excluded whatever puts them in the set (a tagged process in Coral's group, the shell being Coral's own
// pid). The child here is in this test process's group and carries the tag, and is the root of the tree.
func TestTheTreeNeverHoldsCoralItsGroupOrPID1(t *testing.T) {
	canFork(t)
	self, group := os.Getpid(), syscall.Getpgrp()
	if !excluded(1, self, group) || !excluded(self, self, group) {
		t.Fatal("PID 1 and Coral itself are excluded")
	}
	child := exec.Command("sleep", "600")
	child.Env = append(os.Environ(), runTagEnv+"=excl")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { child.Process.Kill(); child.Wait() })
	if g, _ := syscall.Getpgid(child.Process.Pid); g != group {
		t.Fatalf("the child should be in this process's group (%d, not %d)", g, group)
	}
	if !excluded(child.Process.Pid, self, group) {
		t.Fatal("a process in Coral's own group is excluded")
	}
	root := member{child.Process.Pid, createdOf(t, child.Process.Pid)}
	for _, m := range treeMembers(root, "excl") {
		if m.pid == child.Process.Pid || m.pid == self || m.pid == 1 {
			t.Fatalf("the tree holds %d, which is Coral, its group or PID 1", m.pid)
		}
	}
	for _, m := range treeMembers(member{self, createdOf(t, self)}, "excl") {
		if m.pid == self {
			t.Fatal("Coral itself is in its own tree")
		}
	}
	killTree(nil, root, "excl")
	time.Sleep(200 * time.Millisecond)
	if err := syscall.Kill(child.Process.Pid, 0); err != nil {
		t.Fatalf("a process in Coral's own group was signalled: %v", err)
	}
}

// A shell's pid is only the root of its tree while it is still the process the terminal started: after it is reaped the number can be given to an unrelated process, whose children must not be
// ended. A root whose start time is not the recorded one seeds nothing.
func TestARootThatIsNoLongerTheShellSeedsNothing(t *testing.T) {
	canFork(t)
	sh := exec.Command("sh", "-c", "sleep 600 & wait")
	sh.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // a group of its own: not Coral's
	if err := sh.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Kill(-sh.Process.Pid, syscall.SIGKILL); sh.Wait() })
	created := createdOf(t, sh.Process.Pid)
	var got []member
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end) && len(got) < 2; time.Sleep(50 * time.Millisecond) {
		got = treeMembers(member{sh.Process.Pid, created}, "no-such-tag")
	}
	if len(got) < 2 {
		t.Fatalf("the shell and its child are the tree while the root is the recorded process: %v", got)
	}
	if got := treeMembers(member{sh.Process.Pid, created + 1}, "no-such-tag"); len(got) != 0 {
		t.Fatalf("a pid that is no longer the recorded process seeded the tree: %v", got)
	}
	if got := treeMembers(member{}, "no-such-tag"); len(got) != 0 {
		t.Fatalf("an unknown root seeded the tree: %v", got)
	}
}
