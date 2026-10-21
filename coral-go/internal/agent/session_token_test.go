//go:build !windows

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

// #3387: the launch line gives the agent CORAL_SESSION_TOKEN from a file it then
// deletes; only the file's path is in the line. Run for real by /bin/sh: the CLI sees
// the token, the file is gone, and it's gone even if the CLI doesn't exist.
func TestLaunchLineDeliversTheSessionTokenWithoutTypingIt(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	const token = "tok_ABCdef0123456789-_xyz"
	var file string
	SessionTokenMinter = func(sessionID, sessionName string) string {
		assert.Equal(t, "11111111-2222-3333-4444-555555555555", sessionID)
		f, err := os.CreateTemp(dir, "t-*")
		require.NoError(t, err)
		f.WriteString(token)
		f.Close()
		file = f.Name()
		return file
	}
	t.Cleanup(func() { SessionTokenMinter = nil })

	envOut := filepath.Join(dir, "env.txt")
	stub := filepath.Join(dir, "cli")
	require.NoError(t, os.WriteFile(stub, []byte("#!/bin/sh\nprintf '%s|%s' \"$CORAL_SESSION_TOKEN\" \"$CORAL_SESSION_AUTH_FILE\" > "+envOut+"\n"), 0o755))
	for name, ag := range map[string]Agent{"claude": &ClaudeAgent{}, "codex": &CodexAgent{}, "agy": &AgyAgent{}, "pi": &PiAgent{}} {
		os.Remove(envOut)
		p := LaunchParams{SessionID: "11111111-2222-3333-4444-555555555555", CLIPath: stub, CoralPort: 8420}
		line := WrapWithBundlePath(buildLaunch(ag, p))
		assert.NotContains(t, line, token, "%s: the token is never typed", name)
		assert.Contains(t, line, file, name)
		out, err := exec.Command("/bin/sh", "-c", line).CombinedOutput()
		require.NoError(t, err, "%s: %s", name, out)
		got, err := os.ReadFile(envOut)
		require.NoError(t, err, name)
		if name == "codex" {
			// #13: Codex's commands may not see the variable, so they get the file, kept.
			assert.Equal(t, token+"|"+file, string(got), "%s: the CLI has its token and its file", name)
			assert.FileExists(t, file, "%s: the file stays for the tools", name)
			os.Remove(file)
		} else {
			assert.Equal(t, token+"|", string(got), "%s: the CLI has its token, and no file", name)
			assert.NoFileExists(t, file, "%s: the file is deleted", name)
		}
		CleanupTempFiles(p.SessionID)
	}

	// The CLI never starts: the file is still deleted.
	p := LaunchParams{SessionID: "11111111-2222-3333-4444-555555555555", CLIPath: filepath.Join(dir, "missing-cli")}
	line := buildLaunch((&ClaudeAgent{}), p)
	_, _ = exec.Command("/bin/sh", "-c", line).CombinedOutput()
	assert.NoFileExists(t, file)
	CleanupTempFiles(p.SessionID)

	// No minter (tests, other callers): no token part at all.
	SessionTokenMinter = nil
	// (It is still unset: an agent never keeps a token from whatever started Coral, #3421.)
	assert.NotContains(t, buildLaunch((&ClaudeAgent{}), p), `CORAL_SESSION_TOKEN="$(cat`)
	assert.False(t, strings.Contains(buildLaunch((&CodexAgent{}), p), `CORAL_SESSION_TOKEN="$(cat`))
	assert.Contains(t, buildLaunch((&ClaudeAgent{}), p), "unset ")
	CleanupTempFiles(p.SessionID)
}
