package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScheduledJobsCRUD(t *testing.T) {
	db := openTestDB(t)
	s := NewScheduleStore(db)
	ctx := context.Background()

	job, err := s.CreateScheduledJob(ctx, &ScheduledJob{
		Name:     "nightly-tests",
		CronExpr: "0 0 * * *",
		RepoPath: "/tmp/repo",
		Prompt:   "Run all tests",
		Enabled:  1,
	})
	require.NoError(t, err)
	assert.Equal(t, "nightly-tests", job.Name)
	assert.Equal(t, "UTC", job.Timezone)
	assert.Equal(t, 3600, job.MaxDurationS)

	// List
	jobs, err := s.ListScheduledJobs(ctx, false)
	require.NoError(t, err)
	assert.Len(t, jobs, 1)

	// List enabled only
	jobs, err = s.ListScheduledJobs(ctx, true)
	require.NoError(t, err)
	assert.Len(t, jobs, 1)

	// Update
	updated, err := s.UpdateScheduledJob(ctx, job.ID, map[string]interface{}{
		"enabled": false,
	})
	require.NoError(t, err)
	assert.Equal(t, 0, updated.Enabled)

	// Enabled-only should now be empty
	jobs, err = s.ListScheduledJobs(ctx, true)
	require.NoError(t, err)
	assert.Empty(t, jobs)

	// Delete
	err = s.DeleteScheduledJob(ctx, job.ID)
	require.NoError(t, err)
	jobs, _ = s.ListScheduledJobs(ctx, false)
	assert.Empty(t, jobs)
}

func TestScheduledRuns(t *testing.T) {
	db := openTestDB(t)
	s := NewScheduleStore(db)
	ctx := context.Background()

	job, _ := s.CreateScheduledJob(ctx, &ScheduledJob{
		Name: "test-job", CronExpr: "* * * * *",
		RepoPath: "/tmp", Prompt: "test", Enabled: 1,
	})

	// Create run
	runID, err := s.CreateScheduledRun(ctx, job.ID, "2026-01-01T00:00:00Z", "pending")
	require.NoError(t, err)
	assert.Greater(t, runID, int64(0))

	// Get run
	run, err := s.GetScheduledRun(ctx, runID)
	require.NoError(t, err)
	assert.Equal(t, "pending", run.Status)

	// Update run
	err = s.UpdateScheduledRun(ctx, runID, map[string]interface{}{
		"status":     "running",
		"started_at": "2026-01-01T00:01:00Z",
		"session_id": "sess-abc",
	})
	require.NoError(t, err)

	// Active run
	active, err := s.GetActiveRunForJob(ctx, job.ID)
	require.NoError(t, err)
	require.NotNil(t, active)
	assert.Equal(t, "running", active.Status)

	// Running count
	count, err := s.GetRunningCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Complete the run
	err = s.UpdateScheduledRun(ctx, runID, map[string]interface{}{
		"status":      "completed",
		"finished_at": "2026-01-01T01:00:00Z",
	})
	require.NoError(t, err)

	// Last run
	lastRun, err := s.GetLastRunForJob(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, "completed", lastRun.Status)

	// All job session IDs
	ids, err := s.GetAllJobSessionIDs(ctx)
	require.NoError(t, err)
	assert.True(t, ids["sess-abc"])

	// Recent runs (with job name)
	recent, err := s.ListAllRecentRuns(ctx, 50)
	require.NoError(t, err)
	assert.Len(t, recent, 1)
}

func TestOneshotRuns(t *testing.T) {
	db := openTestDB(t)
	s := NewScheduleStore(db)
	ctx := context.Background()

	displayName := "My Task"
	runID, err := s.CreateOneshotRun(ctx, "2026-01-01T00:00:00Z", &displayName, nil)
	require.NoError(t, err)
	assert.Greater(t, runID, int64(0))

	// List oneshot runs
	runs, err := s.ListOneshotRuns(ctx, 50, nil)
	require.NoError(t, err)
	assert.Len(t, runs, 1)

	// Sentinel job should exist
	sentinelID, err := s.GetOrCreateSentinelJob(ctx)
	require.NoError(t, err)
	assert.Greater(t, sentinelID, int64(0))

	// __oneshot__ should NOT appear in regular job list
	jobs, err := s.ListScheduledJobs(ctx, false)
	require.NoError(t, err)
	assert.Empty(t, jobs) // sentinel excluded
}
