package background

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// jobWithRuns creates a daily job whose past runs ended with statuses (oldest
// first); each failure carries error "boom-<n>".
func jobWithRuns(t *testing.T, statuses ...string) (*JobScheduler, *store.ScheduleStore, store.ScheduledJob) {
	t.Helper()
	ss := store.NewScheduleStore(setupTestDB(t))
	ctx := context.Background()
	job, err := ss.CreateScheduledJob(ctx, &store.ScheduledJob{Name: "nightly", CronExpr: "0 9 * * *", Timezone: "UTC", RepoPath: "/tmp", Prompt: "p", Enabled: 1})
	require.NoError(t, err)
	base := time.Now().UTC().Add(-time.Duration(len(statuses)+1) * time.Hour)
	for i, st := range statuses {
		id, err := ss.CreateScheduledRun(ctx, job.ID, base.Add(time.Duration(i)*time.Hour).Format(store.ISOFormat), "pending")
		require.NoError(t, err)
		fields := map[string]interface{}{"status": st}
		if st == "failed" {
			fields["error_msg"] = fmt.Sprintf("boom-%d", i+1)
		}
		require.NoError(t, ss.UpdateScheduledRun(ctx, id, fields))
	}
	s := NewJobScheduler(ss, time.Minute)
	s.SetNextFireTimeFn(NextFireTime)
	j, err := ss.GetScheduledJob(ctx, job.ID)
	require.NoError(t, err)
	return s, ss, *j
}

// A job whose last three runs all failed pauses itself with the newest error
// as the reason, instead of starting a fourth run (#3171).
func TestJobPausesAfterThreeFailuresInARow(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		statuses []string
		paused   bool
	}{
		{"three failures", []string{"failed", "failed", "failed"}, true},
		{"older success, then three failures", []string{"completed", "failed", "failed", "failed"}, true},
		{"two failures", []string{"failed", "failed"}, false},
		{"a success between", []string{"failed", "completed", "failed", "failed"}, false},
		{"last run succeeded", []string{"failed", "failed", "failed", "completed"}, false},
		{"killed is not a failure", []string{"failed", "killed", "failed"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ss, job := jobWithRuns(t, tc.statuses...)
			require.NoError(t, s.evaluateJob(ctx, job, time.Now().UTC()))
			got, err := ss.GetScheduledJob(ctx, job.ID)
			require.NoError(t, err)
			if tc.paused {
				assert.Equal(t, 0, got.Enabled, "paused")
				assert.Equal(t, fmt.Sprintf("Paused after 3 failed runs in a row: boom-%d", len(tc.statuses)), got.PausedReason)
				runs, err := ss.GetRunsForJob(ctx, job.ID, 10)
				require.NoError(t, err)
				assert.Len(t, runs, len(tc.statuses), "no new run is started")
			} else {
				assert.Equal(t, 1, got.Enabled, "still enabled")
				assert.Empty(t, got.PausedReason)
			}
		})
	}
}

// Run worktrees go under Coral's data dir, not beside the user's repository;
// without a root (tests, embedders) the old sibling path is kept (#3171).
func TestRunWorktreeDir(t *testing.T) {
	s := NewJobScheduler(nil, time.Minute)
	assert.Equal(t, "/src/myrepo_task_run_7", s.runWorktreeDir("/src/myrepo", 7))
	root := filepath.Join(t.TempDir(), "worktrees")
	s.SetWorktreeRoot(root)
	assert.Equal(t, filepath.Join(root, "job-run-7-myrepo"), s.runWorktreeDir("/src/myrepo/", 7))
	assert.DirExists(t, root, "the root is created")
}

// Listings never show the internal __oneshot__ job name for a one-off run.
func TestOneOffRunsHaveNoSentinelName(t *testing.T) {
	ss := store.NewScheduleStore(setupTestDB(t))
	ctx := context.Background()
	_, err := ss.CreateOneshotRun(ctx, time.Now().UTC().Format(store.ISOFormat), nil)
	require.NoError(t, err)
	active, err := ss.ListActiveRuns(ctx)
	require.NoError(t, err)
	require.Len(t, active, 1)
	assert.Nil(t, active[0].JobName, "no __oneshot__ name")
	require.NotNil(t, active[0].TriggerType)
	assert.Equal(t, "api", *active[0].TriggerType)
	recent, err := ss.ListAllRecentRuns(ctx, 10)
	require.NoError(t, err)
	require.Len(t, recent, 1)
	assert.Nil(t, recent[0].JobName)
}

// QA's #3171 review: after the user resumes a job that was auto-paused, the
// scheduler must give it a chance to run again instead of re-pausing it on the
// next evaluation, whichever way it was enabled (Toggle, PUT enabled:1, the store).
func TestResumedJobIsNotImmediatelyRepaused(t *testing.T) {
	ctx := context.Background()
	s, ss, job := jobWithRuns(t, "failed", "failed", "failed")
	require.NoError(t, s.evaluateJob(ctx, job, time.Now().UTC()))
	got, _ := ss.GetScheduledJob(ctx, job.ID)
	require.Equal(t, 0, got.Enabled, "paused first")

	// what ToggleJob / PUT enabled:1 do
	_, err := ss.UpdateScheduledJob(ctx, job.ID, map[string]interface{}{"enabled": 1, "paused_reason": ""})
	require.NoError(t, err)
	got, _ = ss.GetScheduledJob(ctx, job.ID)
	require.Equal(t, 1, got.Enabled)
	require.NotEmpty(t, got.ResumedAt, "the resume is recorded")

	require.NoError(t, s.evaluateJob(ctx, *got, time.Now().UTC()))
	again, _ := ss.GetScheduledJob(ctx, job.ID)
	require.Equal(t, 1, again.Enabled, "resumed job was re-paused at once without getting a run")
}

// After a resume the count starts over: three new failures pause it again, and
// enabling a job that was already on does not reset the count.
func TestResumedJobPausesAgainAfterThreeNewFailures(t *testing.T) {
	ctx := context.Background()
	s, ss, job := jobWithRuns(t, "failed", "failed", "failed")
	require.True(t, s.pauseIfFailing(ctx, job))
	resumed, err := ss.UpdateScheduledJob(ctx, job.ID, map[string]interface{}{"enabled": true})
	require.NoError(t, err)
	require.False(t, s.pauseIfFailing(ctx, *resumed), "only failures from before the resume")

	start := time.Now().UTC().Add(time.Minute)
	for i := 0; i < 3; i++ {
		id, err := ss.CreateScheduledRun(ctx, job.ID, start.Add(time.Duration(i)*time.Minute).Format(store.ISOFormat), "pending")
		require.NoError(t, err)
		require.NoError(t, ss.UpdateScheduledRun(ctx, id, map[string]interface{}{"status": "failed", "error_msg": "still broken"}))
		if i < 2 {
			require.False(t, s.pauseIfFailing(ctx, *resumed), "%d new failure(s)", i+1)
		}
	}
	require.True(t, s.pauseIfFailing(ctx, *resumed), "three failures after the resume")

	// Enabling an enabled job is not a resume: resumed_at stays.
	again, err := ss.UpdateScheduledJob(ctx, job.ID, map[string]interface{}{"enabled": 1})
	require.NoError(t, err)
	_, err = ss.UpdateScheduledJob(ctx, job.ID, map[string]interface{}{"enabled": 1})
	require.NoError(t, err)
	same, _ := ss.GetScheduledJob(ctx, job.ID)
	require.Equal(t, again.ResumedAt, same.ResumedAt)
}
