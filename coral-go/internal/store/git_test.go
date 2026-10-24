package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitSnapshotUpsert(t *testing.T) {
	db := openTestDB(t)
	s := NewGitStore(db)
	ctx := context.Background()

	sid := "sess-1"
	snap := &GitSnapshot{
		AgentName:        "my-repo",
		AgentType:        "claude",
		WorkingDirectory: "/tmp/repo",
		Branch:           "main",
		CommitHash:       "abc123",
		CommitSubject:    "initial commit",
		SessionID:        &sid,
	}
	err := s.UpsertGitSnapshot(ctx, snap)
	require.NoError(t, err)

	// Get latest
	latest, err := s.GetLatestGitState(ctx, "my-repo")
	require.NoError(t, err)
	require.NotNil(t, latest)
	assert.Equal(t, "main", latest.Branch)
	assert.Equal(t, "abc123", latest.CommitHash)

	// Get by session
	bySession, err := s.GetLatestGitStateBySession(ctx, "sess-1")
	require.NoError(t, err)
	require.NotNil(t, bySession)
	assert.Equal(t, "abc123", bySession.CommitHash)

	// Duplicate insert (same session_id + commit_hash) should not create new row
	snap.Branch = "feature-branch"
	err = s.UpsertGitSnapshot(ctx, snap)
	require.NoError(t, err)

	snaps, err := s.GetGitSnapshots(ctx, "my-repo", 10)
	require.NoError(t, err)
	assert.Len(t, snaps, 1)                            // Still just one
	assert.Equal(t, "feature-branch", snaps[0].Branch) // But branch updated
}

func TestGitSnapshotAllLatest(t *testing.T) {
	db := openTestDB(t)
	s := NewGitStore(db)
	ctx := context.Background()

	sid1 := "sess-1"
	sid2 := "sess-2"

	s.UpsertGitSnapshot(ctx, &GitSnapshot{
		AgentName: "repo-a", AgentType: "claude", WorkingDirectory: "/a",
		Branch: "main", CommitHash: "aaa", SessionID: &sid1,
	})
	s.UpsertGitSnapshot(ctx, &GitSnapshot{
		AgentName: "repo-b", AgentType: "agy", WorkingDirectory: "/b",
		Branch: "dev", CommitHash: "bbb", SessionID: &sid2,
	})

	all, err := s.GetAllLatestGitState(ctx)
	require.NoError(t, err)
	assert.Contains(t, all, "sess-1")
	assert.Contains(t, all, "sess-2")
	assert.Contains(t, all, "repo-a")
	assert.Contains(t, all, "repo-b")
}

func TestChangedFiles(t *testing.T) {
	db := openTestDB(t)
	s := NewGitStore(db)
	ctx := context.Background()

	sid := "sess-1"
	files := []ChangedFile{
		{Filepath: "main.go", Additions: 10, Deletions: 5, Status: "M"},
		{Filepath: "new.go", Additions: 50, Deletions: 0, Status: "A"},
	}
	err := s.ReplaceChangedFiles(ctx, "my-repo", "/tmp/repo", files, &sid, "branch_point")
	require.NoError(t, err)

	result, found, err := s.GetChangedFiles(ctx, "my-repo", &sid, "branch_point")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Len(t, result, 2)

	// Cache miss for a different mode
	result, found, err = s.GetChangedFiles(ctx, "my-repo", &sid, "previous_commit")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, result)

	// Store a second mode — both caches coexist
	files2 := []ChangedFile{
		{Filepath: "only.go", Additions: 1, Deletions: 1, Status: "M"},
	}
	err = s.ReplaceChangedFiles(ctx, "my-repo", "/tmp/repo", files2, &sid, "previous_commit")
	require.NoError(t, err)

	result, found, err = s.GetChangedFiles(ctx, "my-repo", &sid, "previous_commit")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Len(t, result, 1)
	assert.Equal(t, "only.go", result[0].Filepath)

	// Original mode cache is still intact
	result, found, err = s.GetChangedFiles(ctx, "my-repo", &sid, "branch_point")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Len(t, result, 2)

	// File counts (across all modes)
	counts, err := s.GetAllChangedFileCounts(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, counts["sess-1"])
}

// #6832 (Security's #6831): a commit subject and a branch name are the repository's text. Escape sequences and the characters that reorder or hide text are taken out before they are stored.
func TestGitSnapshotTextIsCleanedBeforeItIsStored(t *testing.T) {
	db := openTestDB(t)
	s := NewGitStore(db)
	ctx := context.Background()
	sid := "sess-clean"
	snap := &GitSnapshot{
		AgentName: "clean-repo", AgentType: "claude", WorkingDirectory: "/tmp/repo", SessionID: &sid, CommitHash: "def456",
		Branch:        "feature/\u202epwned\x1b[31m",
		CommitSubject: "fix \x1b[2Jthe build \u202egpj.exe\u200b and \u2066more\u2069",
	}
	require.NoError(t, s.UpsertGitSnapshot(ctx, snap))
	got, err := s.GetLatestGitStateBySession(ctx, sid)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "feature/pwned", got.Branch)
	assert.Equal(t, "fix the build gpj.exe and more", got.CommitSubject)
	assert.NotContains(t, got.CommitSubject, "\x1b")
	assert.NotContains(t, got.CommitSubject, "\u202e")
}

// Security's #6836 SHOULD: a row stored before the write path cleaned its text (written here by plain SQL) is handed out clean by every getter, so no route shows it raw.
func TestOldGitSnapshotRowsAreCleanedByEveryGetter(t *testing.T) {
	db := openTestDB(t)
	s := NewGitStore(db)
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `INSERT INTO git_snapshots (agent_name, agent_type, working_directory, branch, commit_hash, commit_subject, commit_timestamp, session_id, recorded_at)
		VALUES ('old-repo', 'claude', '/tmp/old', ?, 'abc', ?, '2026-10-01T10:00:00Z', 'sess-old', '2026-10-01T10:00:00Z')`,
		"old/\u202ebranch\x1b[31m", "old \x1b[2Jsubject \u202egpj")
	require.NoError(t, err)
	sid := "sess-old"
	check := func(what string, branch, subject string) {
		assert.Equal(t, "old/branch", branch, what)
		assert.Equal(t, "old subject gpj", subject, what)
	}
	g, err := s.GetLatestGitState(ctx, "old-repo")
	require.NoError(t, err)
	check("by agent", g.Branch, g.CommitSubject)
	g, err = s.GetLatestGitStateBySession(ctx, sid)
	require.NoError(t, err)
	check("by session", g.Branch, g.CommitSubject)
	list, err := s.GetGitSnapshots(ctx, "old-repo", 5)
	require.NoError(t, err)
	require.Len(t, list, 1)
	check("list", list[0].Branch, list[0].CommitSubject)
	list, err = s.GetRecentGitSnapshots(ctx, "", 5)
	require.NoError(t, err)
	require.Len(t, list, 1)
	check("recent", list[0].Branch, list[0].CommitSubject)
	list, err = s.GetGitSnapshotsForSession(ctx, sid, 5)
	require.NoError(t, err)
	require.Len(t, list, 1)
	check("for session", list[0].Branch, list[0].CommitSubject)
	all, err := s.GetAllLatestGitState(ctx)
	require.NoError(t, err)
	check("all latest", all[sid].Branch, all[sid].CommitSubject)
	live, err := s.GetLiveGitState(ctx, map[string]string{sid: "old-repo"})
	require.NoError(t, err)
	check("live", live[sid].Branch, live[sid].CommitSubject)
}
