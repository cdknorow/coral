package ptymanager

import (
	"testing"
	"time"
)

// #4842: ending a session waits for it to exit on SIGTERM and force-kills it after a grace. Kill's is 5 s, which an agent that ignores SIGTERM
// spent in full on every pty restart; a restart ends the old terminal with KillWithin and a short one. The process is a real one on a real
// pty: one that ignores SIGTERM (the signal stays ignored through exec), and one that does not.
func TestKillWithinForceKillsAProcessThatIgnoresSIGTERMAfterTheGrace(t *testing.T) {
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
