package ptymanager

import (
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// #4842: ending a session waits for it to exit on SIGTERM and force-kills it after a grace. Kill's is 5 s, which an agent that ignores SIGTERM
// spent in full on every pty restart; a restart ends the old terminal with KillWithin and a short one. The process is a real one on a real
// pty: one that ignores SIGTERM (the signal stays ignored through exec), and one that does not.
func TestKillWithinForceKillsAProcessThatIgnoresSIGTERMAfterTheGrace(t *testing.T) {
	t.Parallel()
	canFork(t)
	b := NewPTYBackend()
	t.Cleanup(func() { b.Close() })
	const name = "claude-00000000-0000-4000-8000-000000004842"
	if err := b.Spawn(name, "claude", t.TempDir(), "00000000-0000-4000-8000-000000004842", "trap '' TERM HUP; exec sleep 600", 80, 24); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // the trap and the exec are done
	start := time.Now()
	if err := b.KillWithin(name, 400*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	if took < 350*time.Millisecond || took > 3*time.Second {
		t.Fatalf("a process that ignores SIGTERM is force-killed after the grace (400 ms), took %v", took)
	}
	if b.IsRunning(name) {
		t.Fatal("the session is gone")
	}
}

func TestKillWithinDoesNotWaitForAProcessThatExitsOnSIGTERM(t *testing.T) {
	t.Parallel()
	canFork(t)
	b := NewPTYBackend()
	t.Cleanup(func() { b.Close() })
	const name = "claude-00000000-0000-4000-8000-000000004843"
	if err := b.Spawn(name, "claude", t.TempDir(), "00000000-0000-4000-8000-000000004843", "sleep 600", 80, 24); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	if err := b.KillWithin(name, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("a process that exits on SIGTERM is not waited for past its exit: %v", took)
	}
	if err := b.KillWithin(name, time.Second); err == nil {
		t.Fatal("a session that is gone is an error, as for Kill")
	}
}

// #4984: closing a terminal sends SIGHUP, and an interactive shell exits on it though it ignores SIGTERM, so ending a terminal agent no longer waits
// the whole 5 s grace for a SIGKILL. A process that ignores SIGTERM but not SIGHUP (an interactive shell's own stance) ends at once; one that
// ignores both is still force-killed after the grace.
// skipIfHangupIgnored: a process that starts with SIGHUP ignored (a test run under nohup, or from a detached script) passes that to every child
// it starts, and a shell cannot undo it; a SIGHUP then ends nothing and the kill waits its grace, as it did before #4984. That is the
// environment, not the code (it made both tests below fail on every run of a nohup'd epoch job, #4979).
func skipIfHangupIgnored(t *testing.T) {
	t.Helper()
	if signal.Ignored(syscall.SIGHUP) {
		t.Skip("SIGHUP is ignored in this process (started under nohup?): the children inherit that, so a hangup cannot end them")
	}
}

func TestKillEndsAProcessThatIgnoresSIGTERMButNotSIGHUPAtOnce(t *testing.T) {
	canFork(t)
	skipIfHangupIgnored(t)
	b := NewPTYBackend()
	t.Cleanup(func() { b.Close() })
	const name = "claude-00000000-0000-4000-8000-000000004984"
	if err := b.Spawn(name, "claude", t.TempDir(), "00000000-0000-4000-8000-000000004984", "trap '' TERM; exec sleep 600", 80, 24); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // the trap and the exec are done
	start := time.Now()
	if err := b.Kill(name); err != nil { // the full 5 s grace is on offer
		t.Fatal(err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("a process that ignores SIGTERM but not SIGHUP ends at once, took %v", took)
	}
	if b.IsRunning(name) {
		t.Fatal("the session is gone")
	}
}

func TestKillEndsAnInteractiveShellWithinASecond(t *testing.T) {
	canFork(t)
	skipIfHangupIgnored(t)
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	b := NewPTYBackend()
	t.Cleanup(func() { b.Close() })
	const name = "claude-00000000-0000-4000-8000-000000004985"
	if err := b.Spawn(name, "terminal", t.TempDir(), "00000000-0000-4000-8000-000000004985", "bash --norc --noprofile -i", 80, 24); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond) // the prompt is up
	start := time.Now()
	if err := b.Kill(name); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("an interactive shell ends within a second of Kill, took %v", took)
	}
}
