package tmux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// has reports whether env contains s, without printing env on failure (it holds the
// developer's or CI's real tokens).
func has(env, s string) bool { return strings.Contains(env, s) }

// paneEnv starts a window in session and returns the environment its process got.
func paneEnv(t *testing.T, c *Client, session string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "env.txt")
	_, err := c.run(context.Background(), "new-window", "-t", session, "env > "+out+".tmp && mv "+out+".tmp "+out+"; sleep 5")
	require.NoError(t, err)
	var b []byte
	require.Eventually(t, func() bool { b, err = os.ReadFile(out); return err == nil }, 10*time.Second, 20*time.Millisecond)
	return string(b)
}

// #3389: a tmux server Coral starts gives agent panes no server-only settings.
func TestAgentPanesFromANewServerHaveNoServerSettings(t *testing.T) {
	c := newTestClient(t)
	t.Setenv("CORAL_SECRET_KEY", "leak-3389")
	t.Setenv("CORAL_CLOUD_URL", "https://app.coralhub.dev")
	t.Setenv("CORAL_URL", "http://127.0.0.1:9999")
	t.Setenv("CORAL_SESSION_TOKEN", "tok-3389")   // #3387: only ever exported inside a pane's own shell
	t.Setenv("CORAL_SESSION_NAME", "parent-3421") // #3421: nor another session's identity
	require.NoError(t, c.NewSession(context.Background(), "agent-new", t.TempDir()))
	g, err := c.run(context.Background(), "show-environment", "-g")
	require.NoError(t, err)
	assert.False(t, has(g, "CORAL_SESSION_TOKEN="), "the tmux server's global environment never holds an agent token")
	env := paneEnv(t, c, "agent-new")
	assert.False(t, has(env, "tok-3389"), "nor does a new pane inherit one")
	assert.False(t, has(g, "parent-3421") || has(env, "parent-3421"), "nor another session's name")
	assert.False(t, has(env, "leak-3389"), "CORAL_SECRET_KEY reached the agent pane")
	assert.False(t, has(env, "CORAL_CLOUD_URL="), "CORAL_CLOUD_URL reached the agent pane")
	assert.True(t, has(env, "CORAL_URL=http://127.0.0.1:9999"), "what coral-board reads still reaches the agent")
}

// An already running server (started by an older Coral, with the settings in its own
// environment) is scrubbed before Coral opens a new agent pane on it.
func TestAgentPanesOnAnExistingServerHaveNoServerSettings(t *testing.T) {
	c := newTestClient(t)
	pre := exec.Command(c.TmuxBin, "-S", c.SocketPath, "new-session", "-d", "-s", "older", "sleep 30")
	pre.Env = append(os.Environ(), "CORAL_SECRET_KEY=leak-3389", "CORAL_TLS_KEY=/k.pem", "CORAL_URL=http://127.0.0.1:9999",
		"CORAL_SESSION_NAME=parent-session", "CORAL_SUBSCRIBER_ID=ParentRole", "CORAL_SESSION_TOKEN=leak-token-3421")
	out, err := pre.CombinedOutput()
	require.NoError(t, err, string(out))
	before := paneEnv(t, c, "older")
	require.True(t, has(before, "leak-3389"), "the older server leaks it: the case under test")
	require.True(t, has(before, "CORAL_SESSION_NAME=parent-session"))

	// This Coral's own location (startup sets it, #3421) replaces the older server's.
	t.Setenv("CORAL_URL", "http://127.0.0.1:8555")
	require.NoError(t, c.NewSession(context.Background(), "agent-after", t.TempDir()))
	env := paneEnv(t, c, "agent-after")
	assert.False(t, has(env, "leak-3389"), "CORAL_SECRET_KEY reached the agent pane")
	assert.False(t, has(env, "CORAL_TLS_KEY="), "CORAL_TLS_KEY reached the agent pane")
	assert.True(t, has(env, "CORAL_URL=http://127.0.0.1:8555"), "this server's CORAL_URL reaches the agent")
	assert.False(t, has(env, "127.0.0.1:9999"), "never the older server's")
	assert.False(t, has(env, "parent-session"), "an agent identity from whatever started tmux never reaches a pane")
	assert.False(t, has(env, "ParentRole"))
	assert.False(t, has(env, "leak-token-3421"))
	g, err := c.run(context.Background(), "show-environment", "-g")
	require.NoError(t, err)
	assert.True(t, has(g, "-CORAL_SECRET_KEY"), "CORAL_SECRET_KEY is marked for removal in the server's global environment")
}
