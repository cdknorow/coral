package tmux

import (
	"os"
	"testing"
)

// TestMain removes the tmux variables a developer's shell may carry. Running these tests from inside
// a Coral (or any tmux) session would otherwise point the tests' tmux clients at that live server.
func TestMain(m *testing.M) {
	os.Unsetenv("TMUX")
	os.Unsetenv("TMUX_PANE")
	os.Exit(m.Run())
}
