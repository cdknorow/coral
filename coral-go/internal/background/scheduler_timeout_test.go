package background

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/terminalinput"
)

// killRecorder is an AgentRuntime that records KillAgent calls.
type killRecorder struct {
	mu     sync.Mutex
	killed []string
}

func (k *killRecorder) SpawnAgent(_ context.Context, _, _, _, _ string) error { return nil }
func (k *killRecorder) SendInput(_ context.Context, _ terminalinput.Origin, _, _ string) error {
	return nil
}
func (k *killRecorder) IsAlive(_ context.Context, _ string) bool          { return true }
func (k *killRecorder) ListAgents(_ context.Context) ([]AgentInfo, error) { return nil, nil }
func (k *killRecorder) KillAgent(_ context.Context, name string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.killed = append(k.killed, name)
	return nil
}
func (k *killRecorder) kills() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.killed...)
}

// oneshotWith starts a one-shot run whose launch records session "sid-<run>"
// and then runs launch; it returns the scheduler, store, runtime and run id.
func oneshotWith(t *testing.T, maxDur float64, launch func(ctx context.Context) error) (*JobScheduler, *store.ScheduleStore, *killRecorder, int64) {
	t.Helper()
	ss := store.NewScheduleStore(setupTestDB(t))
	s := NewJobScheduler(ss, time.Minute)
	rt := &killRecorder{}
	s.SetRuntime(rt)
	s.SetLaunchFn(func(ctx context.Context, _ store.ScheduledJob, runID int64) error {
		require.NoError(t, ss.UpdateScheduledRun(context.Background(), runID, map[string]interface{}{"session_id": "sid-1"}))
		return launch(ctx)
	})
	runID, err := s.FireOneshot(context.Background(), map[string]interface{}{
		"prompt": "p", "agent_type": "claude", "max_duration_s": maxDur,
	})
	require.NoError(t, err)
	return s, ss, rt, runID
}

func waitRunStatus(t *testing.T, ss *store.ScheduleStore, runID int64, want string) *store.ScheduledRun {
	t.Helper()
	var run *store.ScheduledRun
	require.Eventually(t, func() bool {
		r, err := ss.GetScheduledRun(context.Background(), runID)
		if err != nil || r == nil {
			return false
		}
		run = r
		return r.Status == want && r.FinishedAt != nil
	}, 10*time.Second, 20*time.Millisecond, "run never reached %q", want)
	return run
}

// A run that hits max_duration_s is recorded as killed by timeout AND its agent
// is killed (audit HIGH-02): before, only the record said "killed".
func TestJobTimeoutKillsTheAgent(t *testing.T) {
	_, ss, rt, runID := oneshotWith(t, 1, func(ctx context.Context) error {
		<-ctx.Done() // the agent never finishes on its own
		return ctx.Err()
	})
	run := waitRunStatus(t, ss, runID, "killed")
	require.NotNil(t, run.ExitReason)
	assert.Equal(t, "timeout", *run.ExitReason)
	require.Eventually(t, func() bool { return len(rt.kills()) == 1 }, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, []string{"claude-sid-1"}, rt.kills())
}

// A launch that fails after its session started kills that session too.
func TestJobLaunchFailureKillsTheAgent(t *testing.T) {
	_, ss, rt, runID := oneshotWith(t, 60, func(context.Context) error { return errors.New("watch failed") })
	waitRunStatus(t, ss, runID, "failed")
	require.Eventually(t, func() bool { return len(rt.kills()) == 1 }, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, []string{"claude-sid-1"}, rt.kills())
}

// A user's kill kills the agent once and stays "killed" (user_cancelled): the
// watchdog's own cancel must not record it again as "failed".
func TestJobUserKillStaysKilled(t *testing.T) {
	started := make(chan struct{})
	s, ss, rt, runID := oneshotWith(t, 60, func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	<-started
	require.Eventually(t, func() bool {
		r, _ := ss.GetScheduledRun(context.Background(), runID)
		return r != nil && r.Status == "running"
	}, 5*time.Second, 20*time.Millisecond)
	require.True(t, s.KillRun(context.Background(), runID))
	// KillRun writes "killed" and drops the run from the running set itself, so neither shows
	// that the watchdog is done; wait for it, then check its write didn't replace the kill (#3788).
	s.waitWatchers()
	run := waitRunStatus(t, ss, runID, "killed")
	require.NotNil(t, run.ExitReason)
	assert.Equal(t, "user_cancelled", *run.ExitReason)
	assert.Equal(t, []string{"claude-sid-1"}, rt.kills(), "killed once, by KillRun")
}

// A run that finishes on its own kills nothing.
func TestJobCompletedKillsNothing(t *testing.T) {
	_, ss, rt, runID := oneshotWith(t, 60, func(context.Context) error { return nil })
	waitRunStatus(t, ss, runID, "completed")
	assert.Empty(t, rt.kills())
}
