package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func snapshotRows(t *testing.T, d *DB, where string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, d.GetContext(context.Background(), &n, "SELECT COUNT(*) FROM git_snapshots WHERE "+where, args...))
	return n
}

// #5318: an agent with no session id (a NULL in UNIQUE(session_id, commit_hash), which SQLite never matches) added another identical row at every poll.
func TestASnapshotWithNoSessionIDIsRecordedOncePerCommit(t *testing.T) {
	db := openTestDB(t)
	s := NewGitStore(db)
	ctx := context.Background()
	snap := func(commit, branch string) *GitSnapshot {
		return &GitSnapshot{AgentName: "repo", AgentType: "terminal", WorkingDirectory: "/tmp/r", Branch: branch, CommitHash: commit, CommitSubject: "s"}
	}
	for i := 0; i < 20; i++ {
		require.NoError(t, s.UpsertGitSnapshot(ctx, snap("c1", "main")))
	}
	assert.Equal(t, 1, snapshotRows(t, db, "session_id IS NULL AND agent_name = 'repo'"), "twenty polls of one commit: one row")
	// A new commit is a new row; the branch and the time of the existing one are refreshed.
	require.NoError(t, s.UpsertGitSnapshot(ctx, snap("c2", "main")))
	require.NoError(t, s.UpsertGitSnapshot(ctx, snap("c1", "feature")))
	assert.Equal(t, 2, snapshotRows(t, db, "session_id IS NULL AND agent_name = 'repo'"))
	assert.Equal(t, 1, snapshotRows(t, db, "agent_name = 'repo' AND commit_hash = 'c1' AND branch = 'feature'"), "the existing row was updated")
	// Another agent with no session id and the same commit is its own row.
	other := snap("c1", "main")
	other.AgentName = "other"
	require.NoError(t, s.UpsertGitSnapshot(ctx, other))
	require.NoError(t, s.UpsertGitSnapshot(ctx, other))
	assert.Equal(t, 1, snapshotRows(t, db, "agent_name = 'other'"))
	// With a session id nothing changed: one row per (session, commit).
	sid := "s1"
	for i := 0; i < 5; i++ {
		withSession := snap("c1", "main")
		withSession.SessionID = &sid
		require.NoError(t, s.UpsertGitSnapshot(ctx, withSession))
	}
	assert.Equal(t, 1, snapshotRows(t, db, "session_id = 's1'"))
	latest, err := s.GetLatestGitState(ctx, "repo")
	require.NoError(t, err)
	require.NotNil(t, latest)
}
