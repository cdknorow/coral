package routes

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// QA #7031: the git read that finds a kept job-run worktree's own repository (its job's repository changed) is part of the list, so a git that never answers there must not hold the list past its budget (#6754).
func TestListOfKeptWorktreesReturnsAtItsBudgetWhenGitStallsOnARepoChangedRunFolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake git is a shell script")
	}
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	url, cfg, h := teamServerH(t)
	repo, git := repoOn(t, "master", true)
	other, _ := repoOn(t, "master", true)
	sched := store.NewScheduleStore(h.db)
	job, err := sched.CreateScheduledJob(t.Context(), &store.ScheduledJob{Name: "nightly", CronExpr: "0 9 * * *", RepoPath: repo, Prompt: "p", AgentType: "claude"})
	require.NoError(t, err)
	runID, err := sched.CreateScheduledRun(t.Context(), job.ID, "2026-10-23T09:00:00Z", "completed")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(cfg.CoralDir(), "worktrees"), 0o755))
	wt := filepath.Join(cfg.CoralDir(), "worktrees", fmt.Sprintf("job-run-%d-%s", runID, filepath.Base(repo)))
	git("worktree", "add", "-b", fmt.Sprintf("coral/job-run-%d", runID), "--", wt, "master")
	require.NoError(t, sched.UpdateScheduledRun(t.Context(), runID, map[string]interface{}{"worktree_path": wt}))
	_, err = sched.UpdateScheduledJob(t.Context(), job.ID, map[string]interface{}{"repo_path": other})
	require.NoError(t, err)

	fakeDir := t.TempDir() // every git command goes to the real one, except the read of a worktree's repository, which never answers
	script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = --git-common-dir ]; then exec sleep 30; fi\ndone\nexec " + realGit + " \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(fakeDir, "git"), []byte(script), 0o755))
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	defer func(b time.Duration) { worktreeListBudget = b }(worktreeListBudget)
	worktreeListBudget = 1500 * time.Millisecond

	start := time.Now()
	list := listWorktrees(t, url)
	assert.Less(t, time.Since(start), 5*time.Second, "the list returns at its budget, though git never answered for this folder")
	assert.Len(t, list, 1)
}
