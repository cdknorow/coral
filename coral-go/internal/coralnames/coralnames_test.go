package coralnames

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

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

// The folder and branch names, in one place (#6891). Paths are written with slashes and made the platform's with filepath.FromSlash: the functions join with filepath.Join, so Windows answers with backslashes.
func TestNamesOfWorktreesAndBranches(t *testing.T) {
	p := filepath.FromSlash
	assert.Equal(t, "coral/job-run-42", JobRunBranch(42))
	assert.True(t, IsCoralBranch("coral/job-run-42"))
	assert.True(t, IsCoralBranch(TeamBranch("b-abc")))
	assert.False(t, IsCoralBranch("main"))
	assert.False(t, IsCoralBranch("feature/coral-team/x"), "only the prefix of a branch name")
	assert.Equal(t, p("/data/worktrees"), WorktreesRoot(p("/data")))
	assert.True(t, InWorktreesRoot(p("/data"), p("/data/worktrees/b-1")))
	assert.False(t, InWorktreesRoot(p("/data"), p("/data/worktrees/b-1/sub")))
	assert.False(t, InWorktreesRoot(p("/data"), p("/elsewhere/worktrees/b-1")))
	assert.Equal(t, p("/data/worktrees/job-run-7-proj"), JobRunWorktree(p("/data/worktrees"), p("/home/u/proj"), 7))
	assert.Equal(t, p("/home/u/proj_task_run_7"), JobRunWorktree("", p("/home/u/proj"), 7), "the legacy folder beside the repository")
}

// The exact folder of a run, never the repository, another run's or another repository's.
func TestIsJobRunWorktree(t *testing.T) {
	p := filepath.FromSlash
	root, repo := p("/data/worktrees"), p("/home/u/proj")
	assert.True(t, IsJobRunWorktree(root, repo, 7, p(root+"/job-run-7-proj")))
	assert.True(t, IsJobRunWorktree(root, repo, 7, repo+"_task_run_7"), "the legacy folder beside the repository")
	assert.False(t, IsJobRunWorktree(root, repo, 7, repo), "never the repository itself")
	assert.False(t, IsJobRunWorktree(root, repo, 7, p(root+"/job-run-8-proj")), "another run's")
	assert.False(t, IsJobRunWorktree(root, repo, 7, p(root+"/job-run-7-other")), "another repository's name")
	assert.False(t, IsJobRunWorktree(root, repo, 7, p(root+"/x/job-run-7-proj")), "not directly in the folder")
	assert.False(t, IsJobRunWorktree("", repo, 7, p(root+"/job-run-7-proj")), "no root, no folder")
	assert.False(t, IsJobRunWorktree(root, "", 7, p(root+"/job-run-7-")))
}

// The shape of a run's folder, whatever repository its job named: job-run-<id>-<name> directly in the worktrees folder (#7026).
func TestIsJobRunFolder(t *testing.T) {
	p := filepath.FromSlash
	root := p("/data/worktrees")
	assert.True(t, IsJobRunFolder(root, 7, p(root+"/job-run-7-proj")))
	assert.True(t, IsJobRunFolder(root, 7, p(root+"/job-run-7-an old repo")), "any repository name")
	assert.False(t, IsJobRunFolder(root, 7, p(root+"/job-run-8-proj")), "another run's")
	assert.False(t, IsJobRunFolder(root, 7, p(root+"/job-run-70-proj")), "run 70 is not run 7")
	assert.False(t, IsJobRunFolder(root, 7, p(root+"/job-run-7-")), "a name is needed")
	assert.False(t, IsJobRunFolder(root, 7, p(root+"/x/job-run-7-proj")), "not directly in the folder")
	assert.False(t, IsJobRunFolder(root, 7, p("/elsewhere/job-run-7-proj")), "not in the worktrees folder")
	assert.False(t, IsJobRunFolder("", 7, p(root+"/job-run-7-proj")), "no root, no folder")
}
