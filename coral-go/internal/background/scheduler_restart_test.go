package background

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// aliveRuntime answers IsAlive from a map the test changes; kills are recorded as killRecorder does.
type aliveRuntime struct {
	killRecorder
	am    sync.Mutex
	alive map[string]bool
}

func (a *aliveRuntime) IsAlive(_ context.Context, name string) bool {
	a.am.Lock()
	defer a.am.Unlock()
	return a.alive[name]
}

func (a *aliveRuntime) set(name string, v bool) {
	a.am.Lock()
	defer a.am.Unlock()
	a.alive[name] = v
}

// leftRun makes a run that a previous Coral left in the table: running since started ago, with a session when sid is not empty.
func (e *exitEnv) leftRun(t *testing.T, started time.Duration, sid string) int64 {
	t.Helper()
	ctx := context.Background()
	base, err := e.ss.GetScheduledRun(ctx, e.runID)
	require.NoError(t, err)
	id, err := e.ss.CreateScheduledRun(ctx, base.JobID, time.Now().UTC().Format(store.ISOFormat), "running")
	require.NoError(t, err)
	f := map[string]interface{}{"started_at": time.Now().Add(-started).UTC().Format(store.ISOFormat)}
	if sid != "" {
		f["session_id"] = sid
	}
	require.NoError(t, e.ss.UpdateScheduledRun(ctx, id, f))
	return id
}

func (e *exitEnv) runOf(t *testing.T, id int64) *store.ScheduledRun {
	t.Helper()
	r, err := e.ss.GetScheduledRun(context.Background(), id)
	require.NoError(t, err)
	return r
}

func restartEnv(t *testing.T, name string) (*exitEnv, *aliveRuntime) {
	e := newExitEnv(t, name)
	rt := &aliveRuntime{alive: map[string]bool{}}
	e.s.SetRuntime(rt)
	e.s.parentCtx = context.Background()
	defer func(p time.Duration) { adoptedRunPoll = p }(adoptedRunPoll)
	return e, rt
}

// #6898 (#6881): a run left 'running' by a previous Coral is settled at startup, so its job's next slots are not skipped as "already running": an agent that ended while Coral was down is completed, one that
// never launched is failed, one older than twice its longest run is killed.
func TestRunsLeftByAPreviousCoralAreSettledAtStartup(t *testing.T) {
	ctx := context.Background()
	e, rt := restartEnv(t, "left-settle")
	gone := e.leftRun(t, time.Minute, "sid-gone")   // its agent ended while Coral was down
	never := e.leftRun(t, time.Minute, "")          // never launched an agent
	ancient := e.leftRun(t, 5*time.Hour, "sid-old") // past twice the longest run
	rt.set("claude-sid-old", true)
	// e.runID (pending, nothing started) is the fourth left-over

	e.s.reapStaleRuns(ctx)

	g := e.runOf(t, gone)
	assert.Equal(t, "completed", g.Status)
	require.NotNil(t, g.ExitReason)
	assert.Equal(t, "agent_done", *g.ExitReason)
	n := e.runOf(t, never)
	assert.Equal(t, "failed", n.Status)
	require.NotNil(t, n.ExitReason)
	assert.Equal(t, "interrupted_by_restart", *n.ExitReason)
	require.NotNil(t, n.ErrorMsg)
	assert.Contains(t, *n.ErrorMsg, "Coral restarted")
	a := e.runOf(t, ancient)
	assert.Equal(t, "killed", a.Status)
	assert.Equal(t, []string{"claude-sid-old"}, rt.kills(), "its agent is ended with it")
	active, err := e.ss.GetActiveRunForJob(ctx, e.runOf(t, gone).JobID)
	require.NoError(t, err)
	assert.Nil(t, active, "nothing is left to block the job's next slot")
}

// A run whose agent is still alive is adopted: it stays running until the agent ends, then is completed; when its time is up it is killed with its agent.
func TestARunWhoseAgentOutlivedTheOldCoralIsAdopted(t *testing.T) {
	ctx := context.Background()
	e, rt := restartEnv(t, "left-adopt")
	adoptedRunPoll = 20 * time.Millisecond
	defer func() { adoptedRunPoll = 30 * time.Second }()
	live := e.leftRun(t, time.Minute, "sid-live")
	rt.set("claude-sid-live", true)

	e.s.reapStaleRuns(ctx)
	assert.Equal(t, "running", e.runOf(t, live).Status, "alive: left running, not finished on a guess")

	rt.set("claude-sid-live", false) // the agent ends
	require.Eventually(t, func() bool { return e.runOf(t, live).Status == "completed" }, 3*time.Second, 20*time.Millisecond)
	r := e.runOf(t, live)
	require.NotNil(t, r.ExitReason)
	assert.Equal(t, "agent_done", *r.ExitReason)
	require.NotNil(t, r.FinishedAt)
	e.s.waitWatchers()

	// its time is up while the agent still lives: killed, timeout, the agent ended
	jobRun := e.runOf(t, live)
	_, err := e.ss.UpdateScheduledJob(ctx, jobRun.JobID, map[string]interface{}{"max_duration_s": 2})
	require.NoError(t, err)
	late := e.leftRun(t, 3*time.Second, "sid-late") // started 3 s ago, 2 s allowed, under twice that
	rt.set("claude-sid-late", true)
	e.s.reapStaleRuns(ctx)
	require.Eventually(t, func() bool { return e.runOf(t, late).Status == "killed" }, 3*time.Second, 20*time.Millisecond)
	assert.Contains(t, rt.kills(), "claude-sid-late")
	lr := e.runOf(t, late)
	require.NotNil(t, lr.ExitReason)
	assert.Equal(t, "timeout", *lr.ExitReason)
	e.s.waitWatchers()
}

// A shutdown while a run is adopted leaves it as it is for the next start: the watcher ends without writing anything.
func TestAnAdoptedRunIsLeftAloneWhenCorlShutsDown(t *testing.T) {
	e, rt := restartEnv(t, "left-shutdown")
	adoptedRunPoll = 20 * time.Millisecond
	defer func() { adoptedRunPoll = 30 * time.Second }()
	live := e.leftRun(t, time.Minute, "sid-live2")
	rt.set("claude-sid-live2", true)
	ctx, cancel := context.WithCancel(context.Background())
	e.s.reapStaleRuns(ctx)
	cancel()
	e.s.waitWatchers()
	assert.Equal(t, "running", e.runOf(t, live).Status)
}
