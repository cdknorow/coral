package tmux

import (
	"os"
	"runtime"
	"testing"
	"time"
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
	SetSendKeysSettleForTest(20 * time.Millisecond)
	os.Exit(m.Run())
}

// skipWithoutUnixTmux skips a test that needs a real tmux, its socket, a shell script standing in
// for it, or ps: Coral doesn't use tmux on Windows (the pty backend runs agents there), so these
// have nothing to test on it (#3920 T3).
func skipWithoutUnixTmux(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Coral doesn't use tmux on Windows (the pty backend runs agents there)")
	}
}
