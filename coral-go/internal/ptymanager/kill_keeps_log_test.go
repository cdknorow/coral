//go:build !windows

package ptymanager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/naming"
)

// isolateForKillLog isolates the test (the #4381 env rules; a short temp folder, since a tmux socket path must stay
// short) and points the terminal logs at a folder of its own. It returns that folder.
func isolateForKillLog(t *testing.T) string {
	t.Helper()
	short, err := os.MkdirTemp("/tmp", "k4437-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(short) })
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, short)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CORAL_DATA_DIR", filepath.Join(home, "data"))
	logs := t.TempDir()
	naming.SetDefaultLogDir(logs)
	t.Cleanup(func() { naming.SetDefaultLogDir("") })
	return logs
}

// #4437: killing an agent keeps its terminal log, the user's record of the session, on the pty backend.
func TestKillKeepsTheTerminalLogOnPTY(t *testing.T) {
	canFork(t)
	logs := isolateForKillLog(t)
	backend := NewPTYBackend()
	defer backend.Close()
	term := NewPTYSessionTerminal(backend)
	const sid = "4437aaaa-0000-4000-8000-000000000001"
	name := "claude-" + sid
	require.NoError(t, backend.Spawn(name, "claude", t.TempDir(), sid, "sh -c 'echo kept-output; sleep 60'", 80, 24))
	logPath := naming.LogFile(logs, "claude", sid)
	require.Eventually(t, func() bool {
		b, _ := os.ReadFile(logPath)
		return strings.Contains(string(b), "kept-output")
	}, 10*time.Second, 20*time.Millisecond, "the session writes its log")

	require.NoError(t, term.KillSession(context.Background(), name, "claude", sid))
	assert.False(t, backend.IsRunning(name), "the session is gone")
	b, err := os.ReadFile(logPath)
	require.NoError(t, err, "the log is still there")
	assert.Contains(t, string(b), "kept-output", "with what the agent printed")
}

// The same on the tmux backend, whose KillSession removed the log until #4437.
func TestKillKeepsTheTerminalLogOnTmux(t *testing.T) {
	logs := isolateForKillLog(t)
	term := newTmuxTestTerminal(t)
	const sid = "4437bbbb-0000-4000-8000-000000000002"
	name := "claude-" + sid
	spawnTmuxSession(t, term, name, t.TempDir())
	logPath := naming.LogFile(logs, "claude", sid)
	require.NoError(t, os.WriteFile(logPath, []byte("kept-output\n"), 0o600))

	ctx := context.Background()
	require.NoError(t, term.KillSession(ctx, name, "claude", sid))
	assert.False(t, term.HasSession(ctx, name), "the session is gone")
	b, err := os.ReadFile(logPath)
	require.NoError(t, err, "the log is still there")
	assert.Equal(t, "kept-output\n", string(b))
}
