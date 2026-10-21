package ptymanager

import (
	"github.com/subgentic/coral/internal/testleak"
	"os"
	"testing"
	"time"

	"github.com/subgentic/coral/internal/tmux"
)

// TestMain removes the tmux variables a developer's shell may carry. Running these tests from inside
// a Coral (or any tmux) session would otherwise point the tests' tmux clients at that live server.
func TestMain(m *testing.M) {
	os.Unsetenv("TMUX")
	os.Unsetenv("TMUX_PANE")
	// The tests' tmux servers start their panes with $SHELL. A developer's interactive shell and its
	// rc files make typed commands wait on an unknown startup; a plain sh has none.
	os.Setenv("SHELL", "/bin/sh")
	// The tests drive shells, which need no 300ms between a command and its
	// Enter (production keeps it for agent TUIs).
	tmux.SetSendKeysSettleForTest(20 * time.Millisecond)
	code := m.Run()
	StopGate(10 * time.Second)    // the gate's retry loop, asleep after a held send
	os.Exit(testleak.Check(code)) // no goroutine left running (#4167)
}
