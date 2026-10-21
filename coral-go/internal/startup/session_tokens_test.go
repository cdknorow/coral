package startup

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// #3387: token files live in a 0700 folder, each 0600 with an unpredictable name; leftovers
// are swept at startup; a file holds the token the database knows.
func TestSetupSessionTokens(t *testing.T) {
	home := t.TempDir()
	db, err := storetest.Open(t, filepath.Join(home, "sessions.db"))
	require.NoError(t, err)
	defer db.Close()
	dir := filepath.Join(home, "session_tokens")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	stale := filepath.Join(dir, "t-stale")
	require.NoError(t, os.WriteFile(stale, []byte("old"), 0o600))

	setupSessionTokens(db, home)
	t.Cleanup(func() { agent.SessionTokenMinter = nil })
	assert.NoFileExists(t, stale, "leftovers are swept")
	if runtime.GOOS != "windows" {
		st, err := os.Stat(dir)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), st.Mode().Perm())
	}

	p1 := agent.SessionTokenMinter("sid-1", "claude-sid-1")
	p2 := agent.SessionTokenMinter("sid-1", "claude-sid-1")
	require.NotEmpty(t, p1)
	assert.NotEqual(t, p1, p2, "a new file each time")
	assert.Equal(t, dir, filepath.Dir(p1))
	assert.NotContains(t, filepath.Base(p1), "sid-1", "the name is not the session id")
	if runtime.GOOS != "windows" {
		st, err := os.Stat(p2)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	}
	tok1, _ := os.ReadFile(p1)
	tok2, _ := os.ReadFile(p2)
	_, err = db.SessionTokenOwnerOf(context.Background(), string(tok1))
	assert.ErrorIs(t, err, store.ErrUnknownSessionToken, "the second mint revoked the first")
	o, err := db.SessionTokenOwnerOf(context.Background(), string(tok2))
	require.NoError(t, err)
	assert.Equal(t, "claude-sid-1", o.SessionName)
}

// #13: a Codex agent's token file stays while its token is live, so a Codex agent that outlived a
// restart keeps its tools' token; a revoked one goes at startup, as does an unknown one.
func TestSessionTokenSweepKeepsLiveTokens(t *testing.T) {
	home := t.TempDir()
	db, err := storetest.Open(t, filepath.Join(home, "sessions.db"))
	require.NoError(t, err)
	defer db.Close()
	setupSessionTokens(db, home)
	t.Cleanup(func() { agent.SessionTokenMinter = nil })
	live := agent.SessionTokenMinter("sid-13", "codex-sid-13")
	old := agent.SessionTokenMinter("sid-14", "codex-sid-14")
	revoked := agent.SessionTokenMinter("sid-14", "codex-sid-14") // revokes old's token
	require.NotEmpty(t, live)
	require.NotEmpty(t, revoked)
	unknown := filepath.Join(home, "session_tokens", "t-unknown")
	require.NoError(t, os.WriteFile(unknown, []byte("nobody's"), 0o600))

	sweepSessionTokenFiles(db, filepath.Join(home, "session_tokens"))
	assert.FileExists(t, live)
	assert.FileExists(t, revoked, "the session's current token")
	assert.NoFileExists(t, old, "revoked")
	assert.NoFileExists(t, unknown)
}
