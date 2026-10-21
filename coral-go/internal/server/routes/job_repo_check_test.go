package routes

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/linktest"
)

// A job's repo_path goes through the same directory check as a workflow's
// (canonicalizeRepoPath), plus git (audit LOW-02).
func TestJobRepoErrorUsesTheWorkflowDirectoryCheck(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	require.NoError(t, os.Mkdir(repo, 0o755))
	git := exec.Command("git", "init", "-q", repo)
	git.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	require.NoError(t, git.Run())
	plain := filepath.Join(base, "plain")
	require.NoError(t, os.Mkdir(plain, 0o755))
	file := filepath.Join(base, "file")
	require.NoError(t, os.WriteFile(file, nil, 0o644))
	linkToRepo := filepath.Join(base, "link-repo")
	linktest.Symlink(t, repo, linkToRepo)
	linkToFile := filepath.Join(base, "link-file")
	linktest.Symlink(t, file, linkToFile)

	assert.Empty(t, jobRepoError(ctx, repo))
	assert.Empty(t, jobRepoError(ctx, linkToRepo), "a symlink to a repository resolves like a workflow's")
	assert.Contains(t, jobRepoError(ctx, file), "not an existing directory")
	assert.Contains(t, jobRepoError(ctx, linkToFile), "not an existing directory")
	assert.Contains(t, jobRepoError(ctx, filepath.Join(base, "missing")), "not an existing directory")
	assert.Contains(t, jobRepoError(ctx, plain), "not a git repository")
}
