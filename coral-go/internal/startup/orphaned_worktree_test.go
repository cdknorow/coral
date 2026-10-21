package startup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// #3595: an orphaned team's worktree is cleaned up only when that loses nothing. Startup
// never deletes it when git fails, and never removes one with uncommitted work.
func TestOrphanedTeamWorktreeKeepsItsWork(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	ts := store.NewTeamStore(db)
	root := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+root)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	repo := filepath.Join(root, "repo")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	git(repo, "init", "-q", "-b", "main")
	git(repo, "config", "user.email", "t@example.invalid")
	git(repo, "config", "user.name", "t")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644))
	git(repo, "add", "-A")
	git(repo, "commit", "-qm", "init")

	// 1. Git can't find the repository (a broken .git file): before, the folder was RemoveAll'ed.
	broken := filepath.Join(root, "worktrees", "broken")
	require.NoError(t, os.MkdirAll(broken, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(broken, ".git"), []byte("gitdir: /nonexistent/.git/worktrees/broken\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(broken, "work.txt"), []byte("uncommitted work"), 0o644))
	// 2. A real worktree with uncommitted and untracked work: before, --force removed it.
	dirty := filepath.Join(root, "worktrees", "dirty")
	git(repo, "worktree", "add", "-q", "-b", "dirty", dirty, "main")
	require.NoError(t, os.WriteFile(filepath.Join(dirty, "a.txt"), []byte("changed\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dirty, "new.txt"), []byte("untracked"), 0o644))
	// 3. A clean worktree: removed, as before.
	clean := filepath.Join(root, "worktrees", "clean")
	git(repo, "worktree", "add", "-q", "-b", "clean", clean, "main")

	for _, wd := range []string{broken, dirty, clean} {
		_, err := ts.CreateTeam(ctx, &store.Team{Name: filepath.Base(wd), Status: "running", WorkingDir: wd, IsWorktree: 1})
		require.NoError(t, err)
	}
	reconcileOrphanedTeams(ctx, db)

	b, err := os.ReadFile(filepath.Join(broken, "work.txt"))
	require.NoError(t, err, "a worktree git can't place is left in place")
	assert.Equal(t, "uncommitted work", string(b))
	for f, want := range map[string]string{"a.txt": "changed\n", "new.txt": "untracked"} {
		b, err := os.ReadFile(filepath.Join(dirty, f))
		require.NoError(t, err, "a worktree with uncommitted work is left in place")
		assert.Equal(t, want, string(b))
	}
	_, err = os.Stat(clean)
	assert.True(t, os.IsNotExist(err), "a clean worktree is still removed")

	teams, err := ts.ListTeams(ctx, "running")
	require.NoError(t, err)
	assert.Empty(t, teams, "every orphaned team is marked stopped")
}
