package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #7090 (Security Windows's LOW): FinishScheduledRunIfActive keeps an earlier error_code and error_msg when it is given none (the never-started and sign-in codes are written before the run is finished), settles
// a run once, and never rewrites one already settled.
func TestFinishScheduledRunIfActiveKeepsAnEarlierCodeAndSettlesOnce(t *testing.T) {
	db := openTestDB(t)
	s := NewScheduleStore(db)
	ctx := t.Context()
	job, err := s.CreateScheduledJob(ctx, &ScheduledJob{Name: "j", CronExpr: "0 * * * *", AgentType: "claude", Prompt: "p"})
	require.NoError(t, err)
	id, err := s.CreateScheduledRun(ctx, job.ID, time.Now().UTC().Format(ISOFormat), "running")
	require.NoError(t, err)
	require.NoError(t, s.UpdateScheduledRun(ctx, id, map[string]interface{}{"error_code": "agent_exited_before_start", "error_msg": "ended within seconds"}))

	won, err := s.FinishScheduledRunIfActive(ctx, id, "failed", "agent_exited_before_start", "", "", "2026-10-24T10:00:00Z")
	require.NoError(t, err)
	assert.True(t, won)
	run, err := s.GetScheduledRun(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "failed", run.Status)
	require.NotNil(t, run.ErrorCode)
	assert.Equal(t, "agent_exited_before_start", *run.ErrorCode, "finishing with no code keeps the earlier one")
	require.NotNil(t, run.ErrorMsg)
	assert.Equal(t, "ended within seconds", *run.ErrorMsg, "and no message")

	// A second finish changes nothing, whatever it carries.
	won, err = s.FinishScheduledRunIfActive(ctx, id, "killed", "timeout", "later", "other_code", "2026-10-24T11:00:00Z")
	require.NoError(t, err)
	assert.False(t, won)
	run, _ = s.GetScheduledRun(ctx, id)
	assert.Equal(t, "failed", run.Status)
	assert.Equal(t, "agent_exited_before_start", *run.ErrorCode)

	// A code given is recorded, replacing an earlier one.
	id2, err := s.CreateScheduledRun(ctx, job.ID, time.Now().UTC().Format(ISOFormat), "running")
	require.NoError(t, err)
	won, err = s.FinishScheduledRunIfActive(ctx, id2, "failed", "turn_failed", "the agent's turn ended with an error", "agent_turn_failed", "2026-10-24T10:00:00Z")
	require.NoError(t, err)
	require.True(t, won)
	run2, _ := s.GetScheduledRun(ctx, id2)
	assert.Equal(t, "agent_turn_failed", *run2.ErrorCode)
	assert.Equal(t, "turn_failed", *run2.ExitReason)
}
