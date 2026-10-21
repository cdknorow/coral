//go:build !windows

package startup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// #4411: the startup sweep removes the per-session state of a session that ended in THIS Coral over
// 30 days ago, by exact name, regular files and the session's own Pi folder only, never through a
// symlink; a live, sleeping, archived, recently ended or unknown session keeps everything, and so does
// any near-miss name.
func TestSessionStateSweep(t *testing.T) {
	iso := t.TempDir()
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, iso)
	}
	t.Setenv("HOME", filepath.Join(iso, "home"))
	t.Setenv("USERPROFILE", filepath.Join(iso, "home"))
	dataDir := filepath.Join(iso, "data")
	t.Setenv("CORAL_DATA_DIR", dataDir)
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "pi-sessions"), 0o700))

	db, err := store.OpenWithKey(context.Background(), filepath.Join(dataDir, "sessions.db"), "")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ss := store.NewSessionStore(db)
	ctx := context.Background()
	const (
		old      = "00000000-0000-4000-8000-000000000001" // ended 40 days ago: swept
		oldPi    = "00000000-0000-4000-8000-000000000002" // ended 40 days ago, a pi agent
		recent   = "00000000-0000-4000-8000-000000000003" // ended a day ago
		live     = "00000000-0000-4000-8000-000000000004"
		sleeping = "00000000-0000-4000-8000-000000000005"
		archived = "00000000-0000-4000-8000-000000000006"
		unknown  = "00000000-0000-4000-8000-000000000007" // not in this database
		oldLink  = "00000000-0000-4000-8000-000000000008" // ended 40 days ago; its files are symlinks
	)
	long := time.Now().Add(-40 * 24 * time.Hour).UTC().Format(store.ISOFormat)
	day := time.Now().Add(-24 * time.Hour).UTC().Format(store.ISOFormat)
	for _, s := range []struct {
		id, typ, status, stopped string
		sleeping, archived       int
	}{
		{old, "claude", "inactive", long, 0, 0}, {oldPi, "pi", "inactive", long, 0, 0},
		{recent, "claude", "inactive", day, 0, 0}, {live, "claude", "active", "", 0, 0},
		{sleeping, "claude", "inactive", long, 1, 0}, {archived, "claude", "inactive", long, 1, 1},
		{oldLink, "claude", "inactive", long, 0, 0},
	} {
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: s.id, AgentType: s.typ, AgentName: "a", WorkingDir: "/tmp"}))
		_, err := db.Exec(`UPDATE live_sessions SET status = ?, stopped_at = NULLIF(?, ''), is_sleeping = ?, is_archived = ? WHERE session_id = ?`, s.status, s.stopped, s.sleeping, s.archived, s.id)
		require.NoError(t, err)
	}

	write := func(rel string) string {
		p := filepath.Join(dataDir, rel)
		require.NoError(t, os.WriteFile(p, []byte("{}"), 0o600))
		return p
	}
	rounds := func(typ, id string) string {
		sum := sha256.Sum256([]byte(typ + "-" + id))
		return "board_review_rounds_" + hex.EncodeToString(sum[:8]) + ".json"
	}
	piDir := func(id string) string {
		p := filepath.Join(dataDir, "pi-sessions", id)
		require.NoError(t, os.MkdirAll(filepath.Join(p, "inner"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(p, "inner", "s.jsonl"), []byte("x"), 0o600))
		return p
	}
	gone := []string{
		write("board_state_claude-" + old + ".json"),
		write(rounds("claude", old)),
		write("board_state_pi-" + oldPi + ".json"),
		piDir(oldPi),
	}
	var kept []string
	for _, id := range []string{recent, live, sleeping, archived, unknown} {
		kept = append(kept, write("board_state_claude-"+id+".json"), write(rounds("claude", id)), piDir(id))
	}
	kept = append(kept,
		write("board_state_codex-"+old+".json"),      // the database says claude: not this file's session name
		write("board_state_claude-"+old+".json.bak"), // near misses
		write("board_state_"+old+".json"),
		write("board_review_rounds_"+rounds("claude", old)[20:35]+".json"),
		write("board_state_claude-"+old[:35]+"X.json"),
	)
	// Symlinks named like an old session's files: never removed, nor what they point to.
	outsideFile := filepath.Join(iso, "outside.json")
	require.NoError(t, os.WriteFile(outsideFile, []byte("keep"), 0o600))
	outsideDir := filepath.Join(iso, "outside-dir")
	require.NoError(t, os.MkdirAll(outsideDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(outsideDir, "f"), []byte("keep"), 0o600))
	linkFile := filepath.Join(dataDir, "board_state_claude-"+oldLink+".json")
	require.NoError(t, os.Symlink(outsideFile, linkFile))
	linkDir := filepath.Join(dataDir, "pi-sessions", oldLink)
	require.NoError(t, os.Symlink(outsideDir, linkDir))
	kept = append(kept, linkFile, linkDir, outsideFile, filepath.Join(outsideDir, "f"))
	// An old Pi session id that is a file, not a folder: not the session's folder.
	notDir := filepath.Join(dataDir, "pi-sessions", old)
	require.NoError(t, os.WriteFile(notDir, []byte("x"), 0o600))
	kept = append(kept, notDir)

	select {
	case <-setupSessionStateSweep(db, dataDir):
	case <-time.After(30 * time.Second):
		t.Fatal("the sweep did not finish")
	}
	for _, p := range gone {
		_, err := os.Lstat(p)
		assert.True(t, os.IsNotExist(err), "removed: %s", filepath.Base(p))
	}
	for _, p := range kept {
		_, err := os.Lstat(p)
		assert.NoError(t, err, "kept: %s", p)
	}
}

// With no session that ended over 30 days ago, the sweep removes nothing (and doesn't list the folder).
func TestSessionStateSweepWithNothingStale(t *testing.T) {
	dataDir := t.TempDir()
	p := filepath.Join(dataDir, "board_state_claude-00000000-0000-4000-8000-000000000009.json")
	require.NoError(t, os.WriteFile(p, []byte("{}"), 0o600))
	assert.Equal(t, 0, sweepSessionStateFiles(dataDir, nil))
	_, err := os.Lstat(p)
	assert.NoError(t, err)
}
