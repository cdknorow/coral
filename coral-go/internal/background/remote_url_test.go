package background

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3558: the poller records a remote URL without the credential embedded in it.
func TestQueryGitStripsRemoteCredentials(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.email=t@example.test", "-c", "user.name=t"}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "f"), []byte("x"), 0o644))
	run("add", ".")
	run("commit", "-q", "-m", "c")
	run("remote", "add", "origin", "https://user:ghp_SECRET3558@github.com/org/repo.git")
	info, err := queryGit(context.Background(), repo)
	require.NoError(t, err)
	require.NotNil(t, info.RemoteURL)
	assert.Equal(t, "https://github.com/org/repo.git", *info.RemoteURL)
}
