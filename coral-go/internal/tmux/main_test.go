package tmux

import (
	"os"
	"testing"
	"time"
)

// TestMain removes the tmux variables a developer's shell may carry. Running these tests from inside
// a Coral (or any tmux) session would otherwise point the tests' tmux clients at that live server.
func TestMain(m *testing.M) {
	os.Unsetenv("TMUX")
	os.Unsetenv("TMUX_PANE")
	// The tests drive shells, which need no 300ms between a command and its
	// Enter (production keeps it for agent TUIs).
	SetSendKeysSettleForTest(20 * time.Millisecond)
	os.Exit(m.Run())
}
