package gitutil

import (
	"context"
	"os/exec"
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
		{"a branch named is used as it is", "master", "release", "release"},
		{"a named branch with spaces around it", "master", "  release ", "release"},
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
	got, err := ResolveBase(context.Background(), dir, "release")
	require.NoError(t, err)
	assert.Equal(t, "release", got, "a named branch is the caller's to check")
}

func TestResolveBaseOnADetachedHead(t *testing.T) {
	dir := baseRepo(t, "master", true)
	out, err := exec.Command("git", "-C", dir, "checkout", "-q", "--detach").CombinedOutput()
	require.NoError(t, err, string(out))
	got, err := ResolveBase(context.Background(), dir, "")
	require.NoError(t, err)
	assert.Equal(t, "HEAD", got)
}
