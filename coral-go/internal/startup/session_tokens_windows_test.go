//go:build windows

package startup

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/secfile/secfiletest"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// #3905: each session token file gets an access list of its own (only this user and SYSTEM).
func TestSessionTokenFilesArePrivate(t *testing.T) {
	home := t.TempDir()
	db, err := storetest.Open(t, filepath.Join(home, "sessions.db"))
	require.NoError(t, err)
	defer db.Close()
	setupSessionTokens(db, home)
	t.Cleanup(func() { agent.SessionTokenMinter = nil })
	p := agent.SessionTokenMinter("sid-1", "claude-sid-1")
	require.NotEmpty(t, p)
	secfiletest.RequirePrivate(t, p)
}
