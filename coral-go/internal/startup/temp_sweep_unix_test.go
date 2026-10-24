//go:build !windows

package startup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/internal/cmdhandoff"
	"github.com/subgentic/coral/internal/store"
)

// #4381: at startup launch temp files move to <data dir>/tmp, and the stale ones are swept. In the
// shared temp folder only a session this Coral's database saw end loses its files; a session it
// never heard of (another Coral's) keeps them, as does a live one. In its own folder a session that
// is not live goes.
func TestStartupSweepsOnlyItsOwnEndedSessionsFromTheSharedTempFolder(t *testing.T) {
	shared := t.TempDir()
	t.Setenv("TMPDIR", shared) // this test's own "system" temp folder, never the real one
	// The running processes: one agent, named by the live session's id (fixed, so nothing else
	// running on the machine can change the answer).
	prev := listRunningCommandLines
	listRunningCommandLines = func(context.Context) (string, error) {
		return "/sbin/launchd\n/usr/bin/claude --session-id aaaaaaaa-0000-4000-8000-000000000002\n", nil
	}
	t.Cleanup(func() { listRunningCommandLines = prev })
	dataDir := t.TempDir()
	db, err := store.OpenWithKey(context.Background(), filepath.Join(dataDir, "sessions.db"), "")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ss := store.NewSessionStore(db)
	ctx := context.Background()
	const (
		ended   = "aaaaaaaa-0000-4000-8000-000000000001"
		live    = "aaaaaaaa-0000-4000-8000-000000000002"
		unknown = "aaaaaaaa-0000-4000-8000-000000000003"
	)
	for _, id := range []string{ended, live} {
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: id, AgentType: "claude", AgentName: "a", WorkingDir: "/tmp"}))
	}
	require.NoError(t, ss.UnregisterLiveSession(ctx, ended))

	old := time.Now().Add(-48 * time.Hour)
	stale := func(dir, kind, id string) string {
		p := filepath.Join(dir, "coral_"+kind+"_"+id+"_123.txt")
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
		require.NoError(t, os.Chtimes(p, old, old))
		return p
	}
	private := filepath.Join(dataDir, "tmp")
	sharedEnded, sharedLive, sharedUnknown := stale(shared, "prompt", ended), stale(shared, "prompt", live), stale(shared, "prompt", unknown)
	privateUnknown, privateLive := stale(private, "prompt", unknown), stale(private, "prompt", live)

	select {
	case <-setupLaunchTempDir(db, dataDir):
	case <-time.After(30 * time.Second):
		t.Fatal("the sweep did not finish")
	}
	gone := func(p string) bool { _, err := os.Lstat(p); return os.IsNotExist(err) }
	assert.True(t, gone(sharedEnded), "shared folder: a session this Coral saw end")
	assert.False(t, gone(sharedUnknown), "shared folder: a session it never heard of stays")
	assert.False(t, gone(sharedLive), "shared folder: a live session stays")
	assert.True(t, gone(privateUnknown), "own folder: a session that is not live")
	assert.False(t, gone(privateLive), "own folder: a live session stays")

	info, err := os.Stat(private)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "<data dir>/tmp is private")
}

// #6978: after startup, a start command's script goes to <data dir>/tmp when the system temp folder (TMPDIR) names a folder that is gone, so the launch is not lost to it.
func TestStartupMakesTheLaunchFolderTheStartCommandScriptsFallback(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	dataDir := t.TempDir()
	db, err := store.OpenWithKey(context.Background(), filepath.Join(dataDir, "sessions.db"), "")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	t.Cleanup(func() { cmdhandoff.SetFallbackDir(func() string { return "" }) })
	select {
	case <-setupLaunchTempDir(db, dataDir):
	case <-time.After(30 * time.Second):
		t.Fatal("the sweep did not finish")
	}

	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "deleted"))
	inv, cleanup, err := cmdhandoff.ShellScript("true")
	require.NoError(t, err, "the script is written to Coral's own folder")
	t.Cleanup(cleanup)
	assert.Contains(t, inv, filepath.Join(dataDir, "tmp"))
}
