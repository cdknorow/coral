package gitutil

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func baseRepo(t *testing.T, branch string, commit bool) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("init", "-q", "-b", branch)
	if commit {
		run("commit", "-q", "--allow-empty", "-m", "first")
	}
	return dir
}

// #6701: the branch a worktree starts from is the repository's current branch unless one is named; the default "main" stands only where there is a main.
func TestResolveBase(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name      string
		branch    string
		requested string
		want      string
	}{
		{"a repository on main, nothing named", "main", "", "main"},
		{"a repository on main, main named", "main", "main", "main"},
		{"a repository on master, nothing named", "master", "", "master"},
		{"a repository on master, the old default main stored", "master", "main", "master"},
		{"a repository on another branch, nothing named", "trunk", "", "trunk"},
		{"a branch named is used as it is", "master", "master", "master"},
		{"a named branch with spaces around it", "master", "  master ", "master"},
	} {
		got, err := ResolveBase(ctx, baseRepo(t, c.branch, true), c.requested)
		require.NoError(t, err, c.name)
		assert.Equal(t, c.want, got, c.name)
	}
}

func TestResolveBaseKeepsMainWhereThereIsOneAndTheRepoIsElsewhere(t *testing.T) {
	dir := baseRepo(t, "main", true)
	out, err := exec.Command("git", "-C", dir, "checkout", "-q", "-b", "feature").CombinedOutput()
	require.NoError(t, err, string(out))
	got, err := ResolveBase(context.Background(), dir, "main")
	require.NoError(t, err)
	assert.Equal(t, "main", got, "a main that exists is the base when main is asked for")
	got, err = ResolveBase(context.Background(), dir, "")
	require.NoError(t, err)
	assert.Equal(t, "feature", got, "nothing named: the current branch")
}

func TestResolveBaseWithNothingToStartFrom(t *testing.T) {
	dir := baseRepo(t, "master", false)
	for _, requested := range []string{"", "main"} {
		_, err := ResolveBase(context.Background(), dir, requested)
		assert.ErrorIs(t, err, ErrNoCommits, "a repository with no commits: %q", requested)
	}
}

// #6701: a base branch the repository does not have is a plain answer naming the branches that exist, not git's "invalid reference".
func TestResolveBaseNamesTheBranchesThatExist(t *testing.T) {
	dir := baseRepo(t, "master", true)
	out, err := exec.Command("git", "-C", dir, "branch", "develop").CombinedOutput()
	require.NoError(t, err, string(out))
	_, err = ResolveBase(context.Background(), dir, "release")
	require.Error(t, err)
	assert.Equal(t, "The base branch release does not exist in "+dir+". Its branches: develop, master.", err.Error())
	var missing *BranchMissingError
	require.ErrorAs(t, err, &missing)
	assert.Equal(t, []string{"develop", "master"}, missing.Branches)
	assert.Equal(t, "master", missing.Default)
	got, err := ResolveBase(context.Background(), dir, "--upload-pack=evil")
	require.NoError(t, err)
	assert.Equal(t, "--upload-pack=evil", got, "a name ValidRef refuses is the caller's to refuse")
}

func TestResolveBaseOnADetachedHead(t *testing.T) {
	dir := baseRepo(t, "master", true)
	out, err := exec.Command("git", "-C", dir, "checkout", "-q", "--detach").CombinedOutput()
	require.NoError(t, err, string(out))
	got, err := ResolveBase(context.Background(), dir, "")
	assert.ErrorIs(t, err, ErrDetached, "a detached HEAD is no branch: a base is asked for, not the word HEAD (got %q)", got)
}

// Branch names are text from the repository: cleaned, capped at 20 names of 100 characters, the rest counted.
func TestBranchMissingTextIsCleanedAndCapped(t *testing.T) {
	dir := baseRepo(t, "master", true)
	for i := 0; i < 25; i++ {
		out, err := exec.Command("git", "-C", dir, "branch", fmt.Sprintf("b%02d-%s", i, strings.Repeat("x", 120))).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	_, err := ResolveBase(context.Background(), dir, "gone\u202e")
	var missing *BranchMissingError
	require.ErrorAs(t, err, &missing)
	assert.Len(t, missing.Branches, 20)
	for _, b := range missing.Branches {
		assert.LessOrEqual(t, len([]rune(b)), 103, b)
	}
	assert.Contains(t, err.Error(), "and 6 more") // 25 + master, minus the 20 shown
	assert.NotContains(t, err.Error(), "\u202e")
}

// QA's #6722 wording: a bare repository and a folder git cannot read are not "no commits yet".
func TestResolveBaseSaysWhyThereIsNothingToStartFrom(t *testing.T) {
	bare := t.TempDir()
	out, err := exec.Command("git", "init", "-q", "--bare", bare).CombinedOutput()
	require.NoError(t, err, string(out))
	_, err = ResolveBase(context.Background(), bare, "")
	assert.ErrorIs(t, err, ErrBareRepo)

	_, err = ResolveBase(context.Background(), t.TempDir(), "")
	assert.ErrorIs(t, err, ErrNotARepo, "a folder that is no repository")

	_, err = ResolveBase(context.Background(), baseRepo(t, "master", false), "")
	assert.ErrorIs(t, err, ErrNoCommits, "a repository with no commit says so")
}
