package routes

import (
	"errors"
	"strings"
	"testing"
)

// The pure PTY backend (the default on Windows, and --backend pty) never runs tmux: a failed start there is not a missing tmux, whatever this computer has installed (#6046 review).
func TestSpawnErrorDoesNotBlameTmuxForAFailureThatIsNotAboutTmux(t *testing.T) {
	prev := tmuxAvailable
	t.Cleanup(func() { tmuxAvailable = prev })
	tmuxAvailable = func() bool { return false } // a Windows computer, or one that chose --backend pty
	got := spawnError(errors.New("fork/exec /bin/zsh: no such file or directory")).Error()
	if strings.Contains(got, "tmux is required") {
		t.Fatalf("a PTY start failure is reported as a missing tmux: %q", got)
	}
}
