package agent

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3635 (A): every POSIX launch unsets TMUX and TMUX_PANE, so the agent and the commands it
// runs can't reach Coral's tmux server (and every other agent's pane) through them.
func TestLaunchPrefixUnsetsTmux(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell")
	}
	t.Setenv("CORAL_SHELL", "/bin/sh")
	for _, p := range []LaunchParams{
		{SessionName: "claude-aaaaaaaa-0000-0000-0000-000000000000", SessionID: "aaaaaaaa-0000-0000-0000-000000000000", Role: "Dev", CoralPort: 8450},
		{}, // nothing known: identity unset, TMUX still unset
	} {
		pre := coralEnvPrefix(p)
		assert.Contains(t, pre, "TMUX TMUX_PANE", pre)
		cmd := exec.Command("/bin/sh", "-c", pre+" env")
		cmd.Env = []string{"PATH=/usr/bin:/bin", "TMUX=/private/coral/tmux.sock,123,0", "TMUX_PANE=%3"}
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		for _, line := range strings.Split(string(out), "\n") {
			assert.False(t, strings.HasPrefix(line, "TMUX=") || strings.HasPrefix(line, "TMUX_PANE="), "left in the agent's env: %s", line)
		}
	}
}
