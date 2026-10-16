package tmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSocketArgsCarryTheServerConfig(t *testing.T) {
	t.Setenv(ServerConfigEnv, "")
	assert.Nil(t, socketArgs(""), "tmux's default socket gets no -S and no -f")
	assert.Equal(t, []string{"-S", "/x/tmux.sock", "-f", os.DevNull}, socketArgs("/x/tmux.sock"))
	t.Setenv(ServerConfigEnv, "/etc/coral-tmux.conf")
	assert.Equal(t, []string{"-S", "/x/tmux.sock", "-f", "/etc/coral-tmux.conf"}, socketArgs("/x/tmux.sock"))
}

// #3635 (B): Coral's tmux server starts without the operator's ~/.tmux.conf; CORAL_TMUX_CONF
// opts in to a config of the operator's choosing.
func TestCoralTmuxServerSkipsTheOperatorsConfig(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, ".tmux.conf"), []byte("set -g @coral_probe from-home\n"), 0o600))
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	ctx := context.Background()

	t.Setenv(ServerConfigEnv, "")
	c := newTestClient(t)
	createTestSession(t, c, "claude-00000000-0000-0000-0000-000000000001", home)
	got, _ := c.run(ctx, "show-options", "-gqv", "@coral_probe")
	assert.Empty(t, got, "~/.tmux.conf was not loaded into Coral's server")

	conf := filepath.Join(home, "coral-tmux.conf")
	require.NoError(t, os.WriteFile(conf, []byte("set -g @coral_probe from-coral-conf\n"), 0o600))
	t.Setenv(ServerConfigEnv, conf)
	c2 := newTestClient(t)
	createTestSession(t, c2, "claude-00000000-0000-0000-0000-000000000002", home)
	got, _ = c2.run(ctx, "show-options", "-gqv", "@coral_probe")
	assert.Equal(t, "from-coral-conf", got, "CORAL_TMUX_CONF is loaded")
}

// #3635 (C): the startup count is the Coral agent sessions only the default-socket merge finds:
// not ones also on Coral's socket, and not the operator's own sessions. tmux's default socket is
// moved into a temporary TMUX_TMPDIR, so the operator's real tmux is never touched.
func TestDefaultSocketMergesCountsOnlyCoralAgentsFoundOnlyThere(t *testing.T) {
	require.Empty(t, os.Getenv("TMUX"), "testguard drops TMUX, so the default socket comes from TMUX_TMPDIR")
	c := newTestClient(t)
	tmpdir, err := os.MkdirTemp("", "ctmx-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpdir) })
	t.Setenv("TMUX_TMPDIR", tmpdir)
	defaultSock := filepath.Join(tmpdir, fmt.Sprintf("tmux-%d", os.Getuid()), "default")
	t.Cleanup(func() { exec.Command(c.TmuxBin, "-S", defaultSock, "kill-server").Run() })

	both := "claude-00000000-0000-0000-0000-00000000000b"
	onlyDefault := "codex-00000000-0000-0000-0000-00000000000d"
	createTestSession(t, c, both, tmpdir)
	for _, name := range []string{both, onlyDefault, "my-own-work"} {
		out, err := exec.Command(c.TmuxBin, "new-session", "-d", "-s", name, "sleep 60").CombinedOutput()
		require.NoError(t, err, "%s", strings.TrimSpace(string(out)))
	}
	_, err = os.Stat(defaultSock)
	require.NoError(t, err, "the default-socket server is in the temporary TMUX_TMPDIR")

	ctx := context.Background()
	assert.Empty(t, c.DefaultSocketMerges(ctx), "the merge is off for a test client")
	assert.Equal(t, 0, c.LogDefaultSocketMerges(ctx))
	c.FallbackToDefault = true
	assert.Equal(t, []string{onlyDefault}, c.DefaultSocketMerges(ctx))
	assert.Equal(t, 1, c.LogDefaultSocketMerges(ctx))
}
