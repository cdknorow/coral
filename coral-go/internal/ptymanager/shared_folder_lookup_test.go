package ptymanager

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/terminalinput"
)

// #3704 (as #3687 for tmux): two teammates in one folder each resolve only to their own
// session, by session id or exact session name; the folder name finds neither, so nothing is
// read from, typed into or killed in another agent's terminal.
func TestPTYTeammatesInOneFolderResolveOnlyToThemselves(t *testing.T) {
	skipIfWindows(t)
	term, backend := newTestTerminal(t)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	const sidA = "1a2b3c4d-0000-4000-8000-00000000000a"
	const sidB = "1a2b3c4d-0000-4000-8000-00000000000b"
	keyA, keyB := naming.SessionName("claude", sidA), naming.SessionName("claude", sidB)
	for _, s := range [][2]string{{keyA, sidA}, {keyB, sidB}} {
		err := backend.Spawn(s[0], "claude", dir, s[1], "", 80, 24)
		skipIfPTYSpawnDenied(t, err)
		require.NoError(t, err)
	}
	folder := filepath.Base(dir)

	for _, c := range []struct{ name, sid, want string }{
		{"", sidA, keyA}, {"", sidB, keyB}, {keyA, "", keyA}, {keyB, "", keyB}, {folder, sidB, keyB},
	} {
		pane, err := term.FindSession(ctx, c.name, "claude", c.sid)
		require.NoError(t, err)
		require.NotNil(t, pane, "%+v", c)
		assert.Equal(t, c.want, pane.SessionName, "%+v", c)
	}
	pane, err := term.FindSession(ctx, folder, "claude", "")
	require.NoError(t, err)
	assert.Nil(t, pane, "the shared folder names no session")
	target, err := term.FindTarget(ctx, folder, "claude", "")
	require.NoError(t, err)
	assert.Empty(t, target)

	assert.Equal(t, folder, term.resolveSessionKey(folder), "not resolved to a teammate's key")
	assert.Equal(t, keyB, term.resolveSessionKey(sidB), "a session id (a pane's Target) is its own session")
	assert.Error(t, term.SendTerminalInput(ctx, terminalinput.Unclassified, folder, "x"), "nothing typed by folder")

	// A person typed in B only: A's gate check, by A's session id, does not see it.
	before := time.Now().Add(-time.Second)
	require.NoError(t, backend.SendHumanInput(keyB, []byte("h")))
	assert.False(t, term.HumanInputSince(folder, sidA, before))
	assert.True(t, term.HumanInputSince(folder, sidB, before))

	assert.Error(t, term.KillSession(ctx, folder, "claude", ""), "the folder kills nobody")
	assert.True(t, backend.IsRunning(keyA))
	assert.True(t, backend.IsRunning(keyB))
	require.NoError(t, term.KillSession(ctx, "", "claude", sidB))
	assert.True(t, backend.IsRunning(keyA), "killing B leaves A")
}
