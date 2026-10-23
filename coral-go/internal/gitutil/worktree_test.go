package gitutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	assert.Equal(t, WorktreeOutcome{State: "removed", Path: wt, Branch: "coral-team/team"}, got)
	assert.NoDirExists(t, wt)
	assert.NotContains(t, gitIn(t, repo, "worktree", "list"), "team")
	assert.Empty(t, gitIn(t, repo, "branch", "--list", "coral-team/team"), "nothing on the branch: it goes")

	// the repository given as the folder that is none: found from the worktree
	add()
	got, err = RemoveWorktree(ctx, holder, wt, "coral-team/team")
	require.NoError(t, err)
	assert.Equal(t, "removed", got.State)

	// unsaved work: kept, worktree and branch
	add()
	require.NoError(t, os.WriteFile(filepath.Join(wt, "x.txt"), []byte("x"), 0o644))
	got, err = RemoveWorktree(ctx, repo, wt, "coral-team/team")
	require.NoError(t, err)
	assert.Equal(t, "kept", got.State)
	assert.Equal(t, "it has uncommitted changes", got.Reason)
	assert.FileExists(t, filepath.Join(wt, "x.txt"))

	// committed, clean: the worktree goes, the branch with its unmerged commit stays
	gitIn(t, wt, "add", "x.txt")
	gitIn(t, wt, "commit", "-q", "-m", "x")
	got, err = RemoveWorktree(ctx, repo, wt, "coral-team/team")
	require.NoError(t, err)
	assert.Equal(t, "removed", got.State)
	assert.Equal(t, "its branch is kept: it has commits that are not merged", got.Reason)
	assert.Contains(t, gitIn(t, repo, "branch", "--list", "coral-team/team"), "coral-team/team")

	// the folder deleted by hand: the registration goes, and that is not an error
	gitIn(t, repo, "worktree", "add", "--", wt, "coral-team/team")
	require.NoError(t, os.RemoveAll(wt))
	got, err = RemoveWorktree(ctx, repo, wt, "")
	require.NoError(t, err)
	assert.Equal(t, "removed", got.State)
	assert.NotContains(t, gitIn(t, repo, "worktree", "list"), "team")

	// something that is no worktree of any repository is said, in one line
	_, err = RemoveWorktree(ctx, "", t.TempDir(), "")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "\n")
}

// #6701: adding a team's worktree meets what an earlier run left: a branch that exists is checked out, a registration without a folder is pruned, a worktree already there is reused, and a branch
// held by another worktree is one plain line.
func TestAddTeamWorktree(t *testing.T) {
	ctx := context.Background()
	repo := baseRepo(t, "master", true)
	wt := filepath.Join(t.TempDir(), "team")

	created, err := AddTeamWorktree(ctx, repo, wt, "coral-team/team", "master")
	require.NoError(t, err)
	assert.True(t, created)
	require.NoError(t, os.WriteFile(filepath.Join(wt, "w.txt"), []byte("w"), 0o644))
	gitIn(t, wt, "add", "w.txt")
	gitIn(t, wt, "commit", "-q", "-m", "w")

	reused, err := AddTeamWorktree(ctx, repo, wt, "coral-team/team", "master")
	require.NoError(t, err, "a worktree already there is reused")
	assert.False(t, reused)

	// the folder deleted by hand, then launched again: pruned, the branch with its commit checked out
	require.NoError(t, os.RemoveAll(wt))
	created, err = AddTeamWorktree(ctx, repo, wt, "coral-team/team", "master")
	require.NoError(t, err)
	assert.False(t, created, "the branch existed")
	assert.FileExists(t, filepath.Join(wt, "w.txt"))

	// the branch is held by that worktree: another folder cannot have it, said in one line
	_, err = AddTeamWorktree(ctx, repo, filepath.Join(t.TempDir(), "other"), "coral-team/team", "master")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "\n")

	// a folder that is empty is used; one with something in it that is no worktree is refused
	empty := filepath.Join(t.TempDir(), "empty")
	require.NoError(t, os.MkdirAll(empty, 0o755))
	_, err = AddTeamWorktree(ctx, repo, empty, "coral-team/empty", "master")
	require.NoError(t, err)
	full := filepath.Join(t.TempDir(), "full")
	require.NoError(t, os.MkdirAll(full, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(full, "s.txt"), []byte("s"), 0o644))
	_, err = AddTeamWorktree(ctx, repo, full, "coral-team/full", "master")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not empty")
	assert.FileExists(t, filepath.Join(full, "s.txt"))
}

// #6701 (Security's #6708 C1): coral-team/<board> was an invalid branch for a board named with a space, "..", "~", "^", "?", "*", "[", "@{" or a trailing ".", and every worktree launch for that team failed.
func TestTeamBranchIsAlwaysAValidBranchName(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	plain := []string{"b-noxgu2afvg", "my_team", "team.v2", "équipe", "a-b"}
	for _, name := range plain {
		assert.Equal(t, "coral-team/"+name, TeamBranch(name), "a valid name is used as it is")
	}
	seen := map[string]string{}
	for _, name := range []string{"my team", "a..b", "x~1", "x^", "x?", "x*", "x[0]", "x@{y}", "trailing.", ".leading", "-dash", "a:b", `a\b`, "x.lock", "..", " ", "", "a b", "a_b ", "tab\there", "new\nline", "a//b", "@"} {
		branch := TeamBranch(name)
		out, err := exec.Command("git", "check-ref-format", "--branch", branch).CombinedOutput()
		assert.NoError(t, err, "%q -> %q: %s", name, branch, out)
		assert.NotContains(t, seen, branch, "%q and %q share a branch", name, seen[branch])
		seen[branch] = name
		assert.Equal(t, branch, TeamBranch(name), "the same name, the same branch")
	}
	assert.NotEqual(t, TeamBranch("a b"), TeamBranch("a_b"), "names that differ only where it was replaced get their own")
}

// #6701 (Security's #6708 D): the paths a worktree is added at are cleaned and checked before git runs.
func TestAddTeamWorktreeChecksThePathFirst(t *testing.T) {
	ctx := context.Background()
	repo := baseRepo(t, "master", true)
	holder := t.TempDir()

	long := filepath.Join(holder, strings.Repeat("d", 220))
	_, err := AddTeamWorktree(ctx, repo, long, "coral-team/long", "master")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "longer than")

	file := filepath.Join(holder, "afile")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))
	_, err = AddTeamWorktree(ctx, repo, file, "coral-team/afile", "master")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is a file")

	_, err = AddTeamWorktree(ctx, repo, filepath.Join(holder, "Team"), "coral-team/Team", "master")
	require.NoError(t, err)
	_, err = AddTeamWorktree(ctx, repo, filepath.Join(holder, "team"), "coral-team/team", "master")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "differs only in case")
	assert.Empty(t, gitIn(t, repo, "branch", "--list", "coral-team/team"), "nothing was made for the second")

	// a path with a dot segment is the same worktree
	_, err = AddTeamWorktree(ctx, repo, filepath.Join(holder, "x", "..", "dotted"), "coral-team/dotted", "master")
	require.NoError(t, err)
	assert.DirExists(t, filepath.Join(holder, "dotted"))
}
