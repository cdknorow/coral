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

// #6701: a worktree is removed through the repository that owns it, wherever the caller stands.
func TestRemoveWorktree(t *testing.T) {
	ctx := context.Background()
	repo := baseRepo(t, "master", true)
	holder := t.TempDir() // the folder that holds worktrees: not a repository
	wt := filepath.Join(holder, "team")
	gitIn(t, repo, "worktree", "add", "-b", "coral-team/team", "--", wt, "master")

	require.NoError(t, RemoveWorktree(ctx, repo, wt))
	assert.NoDirExists(t, wt)
	assert.NotContains(t, gitIn(t, repo, "worktree", "list"), "team")
	assert.Contains(t, gitIn(t, repo, "branch", "--list", "coral-team/team"), "coral-team/team", "the branch stays")

	// the repository not given, or given as the folder that is none: found from the worktree
	gitIn(t, repo, "worktree", "add", "--", wt, "coral-team/team")
	require.NoError(t, RemoveWorktree(ctx, holder, wt))
	assert.NoDirExists(t, wt)

	// the folder deleted by hand: the registration goes, and that is not an error
	gitIn(t, repo, "worktree", "add", "--", wt, "coral-team/team")
	require.NoError(t, os.RemoveAll(wt))
	require.NoError(t, RemoveWorktree(ctx, repo, wt))
	assert.NotContains(t, gitIn(t, repo, "worktree", "list"), "team")
	assert.NoError(t, RemoveWorktree(ctx, repo, wt), "already gone")

	// something that is no worktree of any repository is said, in one line
	stray := t.TempDir()
	err := RemoveWorktree(ctx, "", stray)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "\n")
}
