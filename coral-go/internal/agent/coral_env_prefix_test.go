package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3421: whatever environment the pane's shell has (a Coral test server started from an
// agent's shell gave its tmux server, and so its panes, that agent's name, role, token,
// port and data folder), the launch line gives the agent CLI this server's own.
func TestLaunchLineOverridesAnInheritedCoralEnvironment(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "env.txt")
	stub := filepath.Join(dir, "cli")
	require.NoError(t, os.WriteFile(stub, []byte("#!/bin/sh\nenv | grep '^CORAL_' | sort > "+out+"\n"), 0o755))
	hostile := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + dir,
		"CORAL_PORT=8420", "CORAL_HOST=10.0.0.9", "CORAL_URL=http://10.0.0.9:8420", "CORAL_DIR=/home/x/.coral", "CORAL_DATA_DIR=/home/x/.coral",
		"CORAL_SESSION_NAME=parent-session", "CORAL_SESSION_ID=parent-sid", "CORAL_SUBSCRIBER_ID=ParentRole", "CORAL_SESSION_TOKEN=leak-token-parent",
	}
	for name, ag := range map[string]Agent{"claude": &ClaudeAgent{}, "codex": &CodexAgent{}, "agy": &AgyAgent{}, "pi": &PiAgent{}} {
		os.Remove(out)
		p := LaunchParams{SessionID: "11111111-2222-3333-4444-555555555555", SessionName: "claude-own", Role: "Backend Dev",
			CLIPath: stub, CoralPort: 8583, CoralHost: "127.0.0.1", CoralDir: "/tmp/own-data"}
		line := buildLaunch(ag, p)
		cmd := exec.Command("/bin/sh", "-c", line)
		cmd.Env = hostile
		b, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s: %s", name, b)
		got, err := os.ReadFile(out)
		require.NoError(t, err, name)
		env := string(got)
		for _, want := range []string{"CORAL_PORT=8583", "CORAL_HOST=127.0.0.1", "CORAL_URL=http://127.0.0.1:8583", "CORAL_DIR=/tmp/own-data",
			"CORAL_DATA_DIR=/tmp/own-data", "CORAL_SESSION_NAME=claude-own", "CORAL_SUBSCRIBER_ID=Backend Dev",
			"CORAL_SESSION_ID=11111111-2222-3333-4444-555555555555"} {
			assert.Contains(t, env, want+"\n", "%s: %s", name, env)
		}
		for _, leak := range []string{"8420", "10.0.0.9", "/home/x/.coral", "parent-session", "parent-sid", "ParentRole", "leak-token-parent"} {
			assert.NotContains(t, env, leak, "%s: the inherited value reached the CLI", name)
		}
		CleanupTempFiles(p.SessionID)
	}

	// A launch that knows no role or name still never keeps the inherited identity.
	os.Remove(out)
	line := buildLaunch((&ClaudeAgent{}), LaunchParams{SessionID: "22222222-2222-3333-4444-555555555555", CLIPath: stub})
	cmd := exec.Command("/bin/sh", "-c", line)
	cmd.Env = hostile
	b, err := cmd.CombinedOutput()
	require.NoError(t, err, string(b))
	got, _ := os.ReadFile(out)
	for _, leak := range []string{"parent-session", "ParentRole", "leak-token-parent"} {
		assert.NotContains(t, string(got), leak)
	}
	assert.True(t, strings.Contains(string(got), "CORAL_SESSION_ID=22222222"), string(got))
	CleanupTempFiles("22222222-2222-3333-4444-555555555555")
}
