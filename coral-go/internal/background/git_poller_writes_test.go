package background

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// changedFileRows returns the row ids and recorded_at stamps of a session's
// cached list, to tell a rewrite from a freshness refresh.
func changedFileRows(t *testing.T, db *store.DB, sid string) (ids []int64, stamps map[string]bool) {
	t.Helper()
	var rows []struct {
		ID         int64  `db:"id"`
		RecordedAt string `db:"recorded_at"`
	}
	require.NoError(t, db.Select(&rows, `SELECT id, recorded_at FROM git_changed_files WHERE session_id = ? AND diff_mode = 'branch_point' ORDER BY id`, sid))
	stamps = map[string]bool{}
	for _, r := range rows {
		ids = append(ids, r.ID)
		stamps[r.RecordedAt] = true
	}
	return ids, stamps
}

func TestGitPollerSkipsUnchangedListsAndSharesRepoScans(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "sub", "a.txt"), []byte("one\n"), 0o644))
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-q", "-m", "base")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "sub", "a.txt"), []byte("one\ntwo\n"), 0o644))

	db, err := store.Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	gs := store.NewGitStore(db)
	p := NewGitPoller(gs, nil, time.Minute)
	agents := []AgentInfo{
		{AgentName: "root-agent", AgentType: "claude", SessionID: "s-root", WorkingDirectory: repo},
		{AgentName: "sub-agent", AgentType: "claude", SessionID: "s-sub", WorkingDirectory: filepath.Join(repo, "sub")},
	}
	p.SetDiscoverFn(func(context.Context) ([]AgentInfo, error) { return agents, nil })
	ctx := context.Background()

	require.NoError(t, p.PollOnce(ctx))
	for _, sid := range []string{"s-root", "s-sub"} {
		files, found, err := gs.GetChangedFiles(ctx, "", &sid, "branch_point")
		require.NoError(t, err)
		require.True(t, found, sid)
		require.Len(t, files, 1)
		require.Equal(t, "sub/a.txt", files[0].Filepath, "both agents get the repo-root scan")
	}
	firstIDs, firstStamps := changedFileRows(t, db, "s-root")

	// Unchanged repository: same rows, refreshed recorded_at.
	time.Sleep(2 * time.Millisecond)
	require.NoError(t, p.PollOnce(ctx))
	ids, stamps := changedFileRows(t, db, "s-root")
	require.Equal(t, firstIDs, ids, "an unchanged list is not rewritten")
	require.Len(t, stamps, 1)
	for s := range stamps {
		require.False(t, firstStamps[s], "but its recorded_at is refreshed, so the Files cache stays fresh")
	}

	// A changed repository is rewritten.
	require.NoError(t, os.WriteFile(filepath.Join(repo, "b.txt"), []byte("new\n"), 0o644))
	runGit(t, repo, "add", "b.txt")
	require.NoError(t, p.PollOnce(ctx))
	ids, _ = changedFileRows(t, db, "s-root")
	require.Len(t, ids, 2)
	require.NotEqual(t, firstIDs[0], ids[0])

	// Another writer (the Files refresh) replaced the list: the poller notices and rewrites it.
	sid := "s-sub"
	require.NoError(t, gs.ReplaceChangedFiles(ctx, "sub-agent", repo, []store.ChangedFile{{Filepath: "other.go", Status: "M"}}, &sid, "branch_point"))
	require.NoError(t, p.PollOnce(ctx))
	files, _, err := gs.GetChangedFiles(ctx, "", &sid, "branch_point")
	require.NoError(t, err)
	require.Len(t, files, 2)
	require.NotEqual(t, "other.go", files[0].Filepath)

	// Agents that are gone are forgotten.
	agents = agents[:1]
	require.NoError(t, p.PollOnce(ctx))
	require.Len(t, p.written, 1)
}

func TestPRCacheExpiry(t *testing.T) {
	require.Equal(t, 2*time.Minute, prCacheExpiry(0, 2*time.Minute), "no PR yet: recheck every interval")
	require.Equal(t, prCacheTTL, prCacheExpiry(42, 2*time.Minute))
	require.Equal(t, time.Hour, prCacheExpiry(42, time.Hour))
}
