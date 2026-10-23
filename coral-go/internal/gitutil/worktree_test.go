package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...).CombinedOutput()
	require.NoError(t, err, string(out))
	return string(out)
}

// #6701: a worktree is removed through the repository that owns it, wherever the caller stands, and only when nothing in it is unsaved.
func TestRemoveWorktree(t *testing.T) {
	ctx := context.Background()
	repo := baseRepo(t, "master", true)
	holder := t.TempDir() // the folder that holds worktrees: not a repository
	wt := filepath.Join(holder, "team")
	add := func() { gitIn(t, repo, "worktree", "add", "-b", "coral-team/team", "--", wt, "master") }

	add()
	got, err := RemoveWorktree(ctx, repo, wt, "coral-team/team")
	require.NoError(t, err)
	assert.Equal(t, WorktreeOutcome{Removed: true}, got)
	assert.NoDirExists(t, wt)
	assert.NotContains(t, gitIn(t, repo, "worktree", "list"), "team")
	assert.Empty(t, gitIn(t, repo, "branch", "--list", "coral-team/team"), "nothing on the branch: it goes")

	// the repository given as the folder that is none: found from the worktree
	add()
	got, err = RemoveWorktree(ctx, holder, wt, "coral-team/team")
	require.NoError(t, err)
	assert.True(t, got.Removed)

	// unsaved work: kept, worktree and branch
	add()
	require.NoError(t, os.WriteFile(filepath.Join(wt, "x.txt"), []byte("x"), 0o644))
	got, err = RemoveWorktree(ctx, repo, wt, "coral-team/team")
	require.NoError(t, err)
	assert.False(t, got.Removed)
	assert.Contains(t, got.Kept, "1 uncommitted or untracked")
	assert.FileExists(t, filepath.Join(wt, "x.txt"))

	// committed, clean: the worktree goes, the branch with its unmerged commit stays
	gitIn(t, wt, "add", "x.txt")
	gitIn(t, wt, "commit", "-q", "-m", "x")
	got, err = RemoveWorktree(ctx, repo, wt, "coral-team/team")
	require.NoError(t, err)
	assert.True(t, got.Removed)
	assert.Contains(t, got.Kept, "merged nowhere")
	assert.Contains(t, gitIn(t, repo, "branch", "--list", "coral-team/team"), "coral-team/team")

	// the folder deleted by hand: the registration goes, and that is not an error
	gitIn(t, repo, "worktree", "add", "--", wt, "coral-team/team")
	require.NoError(t, os.RemoveAll(wt))
	got, err = RemoveWorktree(ctx, repo, wt, "")
	require.NoError(t, err)
	assert.True(t, got.Removed)
	assert.NotContains(t, gitIn(t, repo, "worktree", "list"), "team")

	// something that is no worktree of any repository is said, in one line
	_, err = RemoveWorktree(ctx, "", t.TempDir(), "")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "\n")
}
