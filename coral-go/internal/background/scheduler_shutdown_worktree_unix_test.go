//go:build !windows

package background

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// slowAddGit puts a git first on PATH that does a real `worktree add` and then hangs, as git does when it is killed halfway: the folder, the registration and the branch exist when the shutdown cancels the add.
func slowAddGit(t *testing.T) {
	t.Helper()
	real, err := exec.LookPath("git")
	require.NoError(t, err)
	dir := t.TempDir()
	script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = add ]; then\n    '" + real + "' \"$@\"\n    exec sleep 30\n  fi\ndone\nexec '" + real + "' \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// Security's #6925 LOW (#6927): a shutdown during `git worktree add` kills the add; the rollback ran on the cancelled context, so the half-made folder, its registration and the branch stayed, with no worktree_path on the row.
// The rollback now outlives the shutdown, and a run the shutdown interrupted is not written as failed: the next start settles it.
func TestShutdownDuringWorktreeAddLeavesNoFolderRegistrationOrBranch(t *testing.T) {
	e := newExitEnv(t, "exit-shutdown-add")
	repo := exitGitRepo(t)
	slowAddGit(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.s.parentCtx = ctx
	want := e.s.runWorktreeDir(repo, e.runID)
	go func() { // the server stops while git is adding
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if _, err := os.Stat(want); err == nil {
				cancel()
				return
			}
		}
		cancel()
	}()
	e.s.launchAndWatch(e.runID, store.ScheduledJob{AgentType: "claude"}, runConfig{repoPath: repo, createWorktree: true, cleanupWorktree: true})

	assert.NoDirExists(t, want, "the half-made folder is removed")
	list, err := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain").CombinedOutput()
	require.NoError(t, err)
	assert.NotContains(t, string(list), filepath.Base(want), "and its registration")
	branches, err := exec.Command("git", "-C", repo, "branch", "--list", "coral/job-run-*").CombinedOutput()
	require.NoError(t, err)
	assert.Empty(t, string(branches), "and its branch")
	assert.NotEqual(t, "failed", e.run(t).Status, "a run the shutdown interrupted is not written as failed")
}

// cleanupWorktree makes its own context, so once the scheduler is shutting down it must not remove anything: an agent that outlives Coral is still working there (#6927, Security's #6930 row 2).
func TestCleanupWorktreeDoesNothingOnceTheSchedulerIsShuttingDown(t *testing.T) {
	e := newExitEnv(t, "exit-cleanup-shutdown")
	repo := exitGitRepo(t)
	wt := e.s.runWorktreeDir(repo, e.runID)
	out, err := exec.Command("git", "-C", repo, "worktree", "add", "-b", "coral/job-run-x", "--", wt, "main").CombinedOutput()
	require.NoError(t, err, string(out))
	ctx, cancel := context.WithCancel(context.Background())
	e.s.parentCtx = ctx
	cancel()
	e.s.cleanupWorktree(repo, wt, "coral/job-run-x", "main")
	assert.DirExists(t, wt, "not removed while shutting down")
	e.s.parentCtx = context.Background()
	e.s.cleanupWorktree(repo, wt, "coral/job-run-x", "main")
	assert.NoDirExists(t, wt, "removed as before when it is not")
}

// Security's #6927 MUST: the rollback removes only what this add made. A run id that repeats (a database restored, a counter reset) finds the folder or the branch of an older run: the add is not attempted, the run fails with
// a plain sentence, and neither the folder nor the branch with its commits is touched.
func TestWorktreeAddNeverRollsBackAFolderOrBranchThatWasThereBefore(t *testing.T) {
	for _, c := range []string{"folder", "branch"} {
		t.Run(c, func(t *testing.T) {
			e := newExitEnv(t, "exit-existing-"+c)
			repo := exitGitRepo(t)
			dir := e.s.runWorktreeDir(repo, e.runID)
			branch := "coral/job-run-" + fmt.Sprint(e.runID)
			if c == "folder" {
				require.NoError(t, os.MkdirAll(dir, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "precious.txt"), []byte("p"), 0o644))
			} else {
				out, err := exec.Command("git", "-C", repo, "checkout", "-q", "-b", branch).CombinedOutput()
				require.NoError(t, err, string(out))
				out, err = exec.Command("git", "-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "an older run's work").CombinedOutput()
				require.NoError(t, err, string(out))
				out, err = exec.Command("git", "-C", repo, "checkout", "-q", "main").CombinedOutput()
				require.NoError(t, err, string(out))
			}
			e.s.launchAndWatch(e.runID, store.ScheduledJob{AgentType: "claude"}, runConfig{repoPath: repo, createWorktree: true, cleanupWorktree: true})

			r := e.run(t)
			assert.Equal(t, "failed", r.Status)
			require.NotNil(t, r.ErrorMsg)
			assert.Contains(t, *r.ErrorMsg, "already exists")
			if c == "folder" {
				assert.FileExists(t, filepath.Join(dir, "precious.txt"), "the folder that was there is not removed")
			} else {
				log, err := exec.Command("git", "-C", repo, "log", "--format=%s", branch).CombinedOutput()
				require.NoError(t, err, "the branch that was there is not deleted")
				assert.Contains(t, string(log), "an older run's work")
				assert.NoDirExists(t, dir)
			}
		})
	}
}

// Security's #6934 LOW (#6936): a git that cannot say whether the run's branch exists (here: rev-parse fails with a status other than "not found", as it does when the shutdown cancelled it) is not "the branch is not there": the add is refused,
// so the rollback's branch -D can never run on a branch this add did not make.
func TestWorktreeAddIsRefusedWhenGitCannotSayWhetherTheBranchExists(t *testing.T) {
	e := newExitEnv(t, "exit-revparse-error")
	repo := exitGitRepo(t)
	real, err := exec.LookPath("git")
	require.NoError(t, err)
	dir := t.TempDir()
	script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = rev-parse ]; then for b in \"$@\"; do case \"$b\" in refs/heads/coral/job-run-*) exit 2;; esac; done; fi\ndone\nexec '" + real + "' \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	wt := e.s.runWorktreeDir(repo, e.runID)

	e.s.launchAndWatch(e.runID, store.ScheduledJob{AgentType: "claude"}, runConfig{repoPath: repo, createWorktree: true, cleanupWorktree: true})

	r := e.run(t)
	assert.Equal(t, "failed", r.Status)
	require.NotNil(t, r.ErrorMsg)
	assert.Contains(t, *r.ErrorMsg, "could not tell")
	assert.NoDirExists(t, wt, "no add was attempted")
	branches, err := exec.Command("git", "-C", repo, "branch", "--list", "coral/job-run-*").CombinedOutput()
	require.NoError(t, err)
	assert.Empty(t, string(branches))
}
