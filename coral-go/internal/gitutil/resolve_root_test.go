package gitutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveGitRootChildren(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	for i := 0; i < 100; i++ {
		require.NoError(t, os.Mkdir(filepath.Join(root, fmt.Sprintf("dir-%03d", i)), 0700))
	}
	require.Equal(t, root, ResolveGitRoot(context.Background(), root))
	repo := filepath.Join(root, "repo")
	out, err := exec.Command("git", "init", repo).CombinedOutput()
	require.NoError(t, err, string(out))
	canonical, err := filepath.EvalSymlinks(repo)
	require.NoError(t, err)
	require.Equal(t, canonical, ResolveGitRoot(context.Background(), root))
	// A separate git directory leaves a gitfile, as linked worktrees do.
	require.NoError(t, os.RemoveAll(filepath.Join(repo, ".git")))
	out, err = exec.Command("git", "init", "--separate-git-dir", filepath.Join(t.TempDir(), "metadata"), repo).CombinedOutput()
	require.NoError(t, err, string(out))
	require.Equal(t, canonical, ResolveGitRoot(context.Background(), root))
}

func BenchmarkResolveGitRootNonRepo(b *testing.B) {
	root := b.TempDir()
	for i := 0; i < 200; i++ {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("dir-%03d", i)), 0700); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ResolveGitRoot(context.Background(), root)
	}
}
