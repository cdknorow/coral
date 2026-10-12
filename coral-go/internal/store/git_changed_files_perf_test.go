package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func changedFilesFixture(n int) []ChangedFile {
	files := make([]ChangedFile, n)
	for i := range files {
		files[i] = ChangedFile{Filepath: fmt.Sprintf("pkg/module%03d/file%04d.go", i%100, i), Additions: i % 50, Deletions: i % 7, Status: "M"}
	}
	return files
}

// TestChangedFilesWritePerf times one git poller pass of changed-file writes
// for 100 agents sharing a monorepo with 2000 changed files. Skipped unless
// CORAL_PERF_GIT is set.
func TestChangedFilesWritePerf(t *testing.T) {
	if os.Getenv("CORAL_PERF_GIT") == "" {
		t.Skip("set CORAL_PERF_GIT=1 to run")
	}
	db := openTestDB(t)
	gs := NewGitStore(db)
	ctx := context.Background()
	files := changedFilesFixture(2000)
	pass := func() time.Duration {
		start := time.Now()
		for a := 0; a < 100; a++ {
			sid := fmt.Sprintf("sess-%03d", a)
			require.NoError(t, gs.ReplaceChangedFiles(ctx, fmt.Sprintf("agent-%d", a), "/repo", files, &sid, "branch_point"))
		}
		return time.Since(start)
	}
	pass()
	t.Logf("replace pass (100 agents x 2000 files): %s", pass().Round(time.Millisecond))
	// An unchanged pass: each agent's list only has its recorded_at refreshed.
	stamps := make([]string, 100)
	for a := range stamps {
		sid := fmt.Sprintf("sess-%03d", a)
		var err error
		stamps[a], err = gs.ReplaceChangedFilesStamped(ctx, fmt.Sprintf("agent-%d", a), "/repo", files, &sid, "branch_point")
		require.NoError(t, err)
	}
	start := time.Now()
	for a := range stamps {
		sid := fmt.Sprintf("sess-%03d", a)
		_, ok, err := gs.TouchChangedFiles(ctx, fmt.Sprintf("agent-%d", a), &sid, "branch_point", stamps[a], len(files))
		require.NoError(t, err)
		require.True(t, ok)
	}
	t.Logf("unchanged pass (touch only):                %s", time.Since(start).Round(time.Millisecond))
}

func TestReplaceChangedFilesBatchedRoundTrip(t *testing.T) {
	gs := NewGitStore(openTestDB(t))
	ctx := context.Background()
	sid := "s1"
	files := changedFilesFixture(450) // spans several insert chunks
	require.NoError(t, gs.ReplaceChangedFiles(ctx, "a", "/repo", files, &sid, "branch_point"))
	got, found, err := gs.GetChangedFiles(ctx, "a", &sid, "branch_point")
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, got, 450)
	byPath := map[string]ChangedFile{}
	for _, f := range got {
		byPath[f.Filepath] = f
	}
	for _, f := range files {
		g := byPath[f.Filepath]
		require.Equal(t, [3]any{f.Additions, f.Deletions, f.Status}, [3]any{g.Additions, g.Deletions, g.Status})
	}
	// Agent-scoped (no session) lists and an empty replacement still work.
	require.NoError(t, gs.ReplaceChangedFiles(ctx, "b", "/repo", files[:3], nil, "branch_point"))
	got, _, err = gs.GetChangedFiles(ctx, "b", nil, "branch_point")
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.NoError(t, gs.ReplaceChangedFiles(ctx, "a", "/repo", nil, &sid, "branch_point"))
	_, found, err = gs.GetChangedFiles(ctx, "a", &sid, "branch_point")
	require.NoError(t, err)
	require.False(t, found)
}

func TestTouchChangedFiles(t *testing.T) {
	gs := NewGitStore(openTestDB(t))
	ctx := context.Background()
	sid := "s1"
	files := changedFilesFixture(5)
	stamp, err := gs.ReplaceChangedFilesStamped(ctx, "a", "/repo", files, &sid, "branch_point")
	require.NoError(t, err)

	time.Sleep(2 * time.Millisecond)
	newStamp, ok, err := gs.TouchChangedFiles(ctx, "a", &sid, "branch_point", stamp, 5)
	require.NoError(t, err)
	require.True(t, ok)
	require.Greater(t, newStamp, stamp)
	got, _, err := gs.GetChangedFiles(ctx, "a", &sid, "branch_point")
	require.NoError(t, err)
	require.Len(t, got, 5)
	for _, f := range got {
		require.Equal(t, newStamp, f.RecordedAt, "every row is fresh again")
	}

	// A stale stamp or count (another writer replaced the list) changes nothing.
	_, ok, err = gs.TouchChangedFiles(ctx, "a", &sid, "branch_point", stamp, 5)
	require.NoError(t, err)
	require.False(t, ok)
	_, ok, err = gs.TouchChangedFiles(ctx, "a", &sid, "branch_point", newStamp, 4)
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, gs.ReplaceChangedFiles(ctx, "a", "/repo", files[:2], &sid, "branch_point")) // e.g. the Files refresh
	_, ok, err = gs.TouchChangedFiles(ctx, "a", &sid, "branch_point", newStamp, 5)
	require.NoError(t, err)
	require.False(t, ok)
	// Other diff modes and agent-scoped rows are separate caches.
	_, ok, err = gs.TouchChangedFiles(ctx, "a", &sid, "working_tree", newStamp, 2)
	require.NoError(t, err)
	require.False(t, ok)
	_, ok, err = gs.TouchChangedFiles(ctx, "a", nil, "branch_point", newStamp, 2)
	require.NoError(t, err)
	require.False(t, ok)
}
