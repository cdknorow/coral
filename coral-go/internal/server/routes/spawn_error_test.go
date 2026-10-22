package routes

import (
	"errors"
	"strings"
	"testing"

	"github.com/subgentic/coral/internal/tmux"
)

// A launch where tmux is what is missing says so with the one command for this computer, not "pty spawn failed: tmux new-session: exec: not found" (#6046).
func TestSpawnErrorNamesMissingTmuxAndItsInstallCommand(t *testing.T) {
	boom := errors.New("tmux new-session: exec: \"tmux\": executable file not found in $PATH")
	prev := tmuxAvailable
	t.Cleanup(func() { tmuxAvailable = prev })

	tmuxAvailable = func() bool { return false }
	got := spawnError(boom).Error()
	if strings.Contains(got, "pty spawn failed") || !strings.Contains(got, tmux.InstallCommand()) || !strings.Contains(got, "required") {
		t.Fatalf("missing tmux: %q", got)
	}

	tmuxAvailable = func() bool { return true }
	got = spawnError(boom).Error()
	if !strings.HasPrefix(got, "pty spawn failed: ") || !strings.Contains(got, "executable file not found") {
		t.Fatalf("another failure is reported as it was: %q", got)
	}
}
