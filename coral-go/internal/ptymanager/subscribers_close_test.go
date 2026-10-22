package ptymanager

import (
	"errors"
	"runtime"
	"testing"
	"time"
)

// A terminal whose process ends on its own (the shell's exit) closes its subscribers' channels, so the dashboard's stream says terminal_closed instead of
// staying open on a dead terminal (found on Windows, where the pty backend is the only one: terminal_gone_ws_browser waited for terminal_closed for ever).
func TestPTYBackendSubscribersCloseWhenTheProcessEnds(t *testing.T) {
	if runtime.GOOS != "windows" {
		canFork(t)
	}
	backend := NewPTYBackend()
	defer backend.Close()
	if err := backend.Spawn("ends-test", "test", t.TempDir(), "sid-ends", endingCommand(), 80, 24); err != nil {
		t.Fatalf("Spawn failed: %v", err)
	}
	ch, err := backend.Attach("ends-test", "ws-1")
	if err != nil { // it may already be gone: then Attach says so, which is the same news
		if !errors.Is(err, ErrTerminalGone) {
			t.Fatalf("Attach failed: %v", err)
		}
		return
	}
	deadline := time.After(15 * time.Second)
	for {
		select {
		case _, open := <-ch:
			if !open {
				return
			}
		case <-deadline:
			t.Fatal("the subscriber's channel stayed open after the process ended")
		}
	}
}

// endingCommand is a program that ends at once: the shell's own exit.
func endingCommand() string {
	if runtime.GOOS == "windows" {
		return "powershell.exe -NoProfile -Command exit"
	}
	return "exit"
}
