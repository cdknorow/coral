package background

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// #3601 (weak site W24): a stored base branch that git would read as an option never reaches it:
// branchHasCommits answers "keep the branch" without running git.
func TestBranchHasCommitsNeverPassesAnOption(t *testing.T) {
	repo := t.TempDir()
	out := filepath.Join(t.TempDir(), "written")
	assert.True(t, branchHasCommits(context.Background(), repo, "--output="+out, "coral/job-run-1"))
	assert.True(t, branchHasCommits(context.Background(), repo, "main", "-x"))
	_, err := os.Stat(out)
	assert.True(t, os.IsNotExist(err), "git was never given --output")
}

// A stored job whose base branch is an option fails its run at use time, before git runs, with
// a message that says why (Security's NIT on #3601: the use-time check, pinned).
func TestJobRunRefusesAStoredOptionAsBaseBranch(t *testing.T) {
	ss := store.NewScheduleStore(setupTestDB(t))
	ctx := context.Background()
	job, err := ss.CreateScheduledJob(ctx, &store.ScheduledJob{Name: "nightly", CronExpr: "0 9 * * *", Timezone: "UTC", RepoPath: t.TempDir(), Prompt: "p", Enabled: 1})
	require.NoError(t, err)
	runID, err := ss.CreateScheduledRun(ctx, job.ID, time.Now().UTC().Format(store.ISOFormat), "pending")
	require.NoError(t, err)
	s := NewJobScheduler(ss, time.Minute)
	root := t.TempDir()
	s.SetWorktreeRoot(root)
	launched := false
	s.SetLaunchFn(func(context.Context, store.ScheduledJob, int64) error { launched = true; return nil })

	s.launchAndWatch(runID, *job, runConfig{repoPath: job.RepoPath, baseBranch: "--upload-pack=touch x", createWorktree: true, maxDurationS: 5})

	run, err := ss.GetScheduledRun(ctx, runID)
	require.NoError(t, err)
	assert.Equal(t, "failed", run.Status)
	require.NotNil(t, run.ErrorMsg)
	assert.Contains(t, *run.ErrorMsg, "base_branch refused")
	assert.False(t, launched, "no agent was launched")
	entries, _ := os.ReadDir(root)
	assert.Empty(t, entries, "no worktree was made")
}
