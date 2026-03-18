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
	assert.Len(t, snaps, 1) // Still just one
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
		AgentName: "repo-b", AgentType: "gemini", WorkingDirectory: "/b",
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
	err := s.ReplaceChangedFiles(ctx, "my-repo", "/tmp/repo", files, &sid)
	require.NoError(t, err)

	result, err := s.GetChangedFiles(ctx, "my-repo", &sid)
	require.NoError(t, err)
	assert.Len(t, result, 2)

	// Replace with new set
	files2 := []ChangedFile{
		{Filepath: "only.go", Additions: 1, Deletions: 1, Status: "M"},
	}
	err = s.ReplaceChangedFiles(ctx, "my-repo", "/tmp/repo", files2, &sid)
	require.NoError(t, err)

	result, err = s.GetChangedFiles(ctx, "my-repo", &sid)
	require.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, "only.go", result[0].Filepath)

	// File counts
	counts, err := s.GetAllChangedFileCounts(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, counts["sess-1"])
}
