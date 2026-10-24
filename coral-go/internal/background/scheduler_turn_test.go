package background

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// #7090: a scheduled prompt run finishes on its agent's own accepted turn end, once, for live and adopted runs.

// endingRuntime is a runtime whose kill ends the agent (unless it is stubborn, a kill that does not take).
type endingRuntime struct {
	aliveRuntime
	stubborn bool
}

func (r *endingRuntime) KillAgent(ctx context.Context, name string) error {
	_ = r.killRecorder.KillAgent(ctx, name)
	if !r.stubborn {
		r.set(name, false)
	}
	return nil
}

// turns is the stored turn of each session, as the test sets it.
type turns struct {
	mu sync.Mutex
	m  map[string]store.RunTurn
}

func (t *turns) set(sid string, turn store.RunTurn) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = map[string]store.RunTurn{}
	}
	t.m[sid] = turn
}

func (t *turns) read(_ context.Context, sid string) (store.RunTurn, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.m[sid], nil
}

// setTurnTimings sets the package timings a test needs and restores them when the test is over. Register it BEFORE the cleanup that stops the scheduler's watchers: cleanups run last-in first-out, so the
// watchers are gone (and no longer reading the variables) when they are restored (QA's race on #7090).
func setTurnTimings(t *testing.T, adopt, poll, settle time.Duration) {
	t.Helper()
	oa, op, os := adoptedRunPoll, turnPoll, turnSettle
	t.Cleanup(func() { adoptedRunPoll, turnPoll, turnSettle = oa, op, os })
	if adopt > 0 {
		adoptedRunPoll = adopt
	}
	if poll > 0 {
		turnPoll = poll
	}
	if settle >= 0 {
		turnSettle = settle
	}
}

func longAgo() string { return time.Now().Add(-time.Minute).UTC().Format(store.ISOFormat) }

func turnEnv(t *testing.T, name string) (*exitEnv, *endingRuntime, *turns) {
	e := newExitEnv(t, name)
	rt := &endingRuntime{aliveRuntime: aliveRuntime{alive: map[string]bool{}}}
	e.s.SetRuntime(rt)
	e.s.parentCtx = context.Background()
	tr := &turns{}
	e.s.SetSessionTurn(tr.read)
	return e, rt, tr
}

func reasonOf(t *testing.T, r *store.ScheduledRun) string {
	t.Helper()
	require.NotNil(t, r.ExitReason)
	return *r.ExitReason
}

// An adopted run whose agent is alive and whose turn ended normally is completed (turn_complete), its agent ended, and the next slot is free; a turn still going stays adopted; one that ended in an error is
// failed; a second look changes nothing.
func TestAdoptedRunFinishesOnItsAcceptedTurnEnd(t *testing.T) {
	setTurnTimings(t, time.Hour, 0, -1)
	ctx, cancel := context.WithCancel(context.Background())
	e, rt, tr := turnEnv(t, "turn-adopt")
	t.Cleanup(func() { cancel(); e.s.waitWatchers() })
	done := e.leftRun(t, time.Minute, "sid-done")
	working := e.leftRun(t, time.Minute, "sid-working")
	failed := e.leftRun(t, time.Minute, "sid-failed")
	never := e.leftRun(t, time.Minute, "sid-never") // no prompt submitted: no turn
	for _, sid := range []string{"sid-done", "sid-working", "sid-failed", "sid-never"} {
		rt.set("claude-"+sid, true)
	}
	tr.set("sid-done", store.RunTurn{State: store.TurnDone, At: longAgo()})
	tr.set("sid-working", store.RunTurn{State: store.TurnWorking})
	tr.set("sid-failed", store.RunTurn{State: store.TurnFailed, At: longAgo()})

	e.s.reapStaleRuns(ctx)

	d := e.runOf(t, done)
	assert.Equal(t, "completed", d.Status)
	assert.Equal(t, "turn_complete", reasonOf(t, d))
	assert.NotNil(t, d.FinishedAt)
	f := e.runOf(t, failed)
	assert.Equal(t, "failed", f.Status)
	assert.Equal(t, "turn_failed", reasonOf(t, f))
	require.NotNil(t, f.ErrorMsg)
	assert.Contains(t, *f.ErrorMsg, "turn ended with an error")
	require.NotNil(t, f.ErrorCode)
	assert.Equal(t, "agent_turn_failed", *f.ErrorCode, "a fixed code, like the other failure reasons")
	assert.Nil(t, d.ErrorCode, "a turn that ended well has none")
	assert.Equal(t, "running", e.runOf(t, working).Status, "a turn still going stays adopted")
	assert.Equal(t, "running", e.runOf(t, never).Status, "no turn started: nothing to finish")
	assert.ElementsMatch(t, []string{"claude-sid-done", "claude-sid-failed"}, rt.kills(), "only the finished runs' agents are ended")
	assert.False(t, rt.IsAlive(ctx, "claude-sid-done"), "its agent is gone")
	assert.True(t, rt.IsAlive(ctx, "claude-sid-working"))

	// Looking again settles nothing twice.
	e.s.reapStaleRuns(ctx)
	assert.Len(t, rt.kills(), 2)
	assert.Equal(t, "turn_complete", reasonOf(t, e.runOf(t, done)))
}

// A stop that is not yet old enough is not an end: the stop hook that stored it may still send the agent on (turnSettle).
func TestAStopStillSettlingIsNotYetTheEnd(t *testing.T) {
	setTurnTimings(t, time.Hour, 0, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	e, rt, tr := turnEnv(t, "turn-settle")
	t.Cleanup(func() { cancel(); e.s.waitWatchers() })
	id := e.leftRun(t, time.Minute, "sid-s")
	rt.set("claude-sid-s", true)
	tr.set("sid-s", store.RunTurn{State: store.TurnDone, At: time.Now().UTC().Format(store.ISOFormat)})
	e.s.reapStaleRuns(ctx)
	assert.Equal(t, "running", e.runOf(t, id).Status)
	assert.Empty(t, rt.kills())
}

// A run that is not a scheduled prompt run of Claude or Codex is never finished by a turn: the one-shot sentinel, a workflow job, another agent, and a scheduler with no turn reader.
func TestOnlyScheduledPromptRunsOfClaudeAndCodexFinishOnATurn(t *testing.T) {
	e, _, _ := turnEnv(t, "turn-scope")
	for name, tc := range map[string]struct {
		job  store.ScheduledJob
		want bool
	}{
		"a claude job":    {store.ScheduledJob{Name: "n", AgentType: "claude"}, true},
		"a codex job":     {store.ScheduledJob{Name: "n", AgentType: "codex"}, true},
		"no type":         {store.ScheduledJob{Name: "n"}, true},
		"gemini":          {store.ScheduledJob{Name: "n", AgentType: "gemini"}, false},
		"a workflow":      {store.ScheduledJob{Name: "n", AgentType: "claude", JobType: "workflow"}, false},
		"the one-off job": {store.ScheduledJob{Name: "__oneshot__", AgentType: "claude"}, false},
	} {
		assert.Equal(t, tc.want, e.s.finishesOnTurn(tc.job), name)
	}
	e.s.SetSessionTurn(nil)
	assert.False(t, e.s.finishesOnTurn(store.ScheduledJob{Name: "n", AgentType: "claude"}), "no turn reader")
}

// The claim: a turn end, a user's kill, the deadline and a repeated event finish a run once, and none overwrites another.
func TestTheRunIsFinishedOnceWhateverEndsItFirst(t *testing.T) {
	ctx := context.Background()
	e, rt, _ := turnEnv(t, "turn-once")
	job := store.ScheduledJob{Name: "n", AgentType: "claude"}

	// Settled by its turn: a kill that comes after changes nothing, and a second settle is refused.
	id := e.leftRun(t, time.Minute, "sid-a")
	rt.set("claude-sid-a", true)
	run := *e.runOf(t, id)
	ok, gone := e.s.settleOnTurn(ctx, run, job, false)
	assert.True(t, ok)
	assert.True(t, gone)
	ok, _ = e.s.settleOnTurn(ctx, run, job, false)
	assert.False(t, ok, "a repeated event settles nothing")
	assert.False(t, e.s.KillRun(ctx, id), "the run is over: the kill finds nothing")
	assert.Equal(t, "completed", e.runOf(t, id).Status)
	assert.Equal(t, "turn_complete", reasonOf(t, e.runOf(t, id)))

	// Killed by the user first: the turn end that follows does not overwrite the kill.
	id2 := e.leftRun(t, time.Minute, "sid-b")
	rt.set("claude-sid-b", true)
	run2 := *e.runOf(t, id2)
	require.True(t, e.s.KillRun(ctx, id2))
	ok, _ = e.s.settleOnTurn(ctx, run2, job, false)
	assert.False(t, ok)
	assert.Equal(t, "killed", e.runOf(t, id2).Status)
	assert.Equal(t, "user_cancelled", reasonOf(t, e.runOf(t, id2)))

	// The deadline's finish after a turn end does not overwrite it (finishRun is the claim's other user).
	id3 := e.leftRun(t, time.Minute, "sid-c")
	rt.set("claude-sid-c", true)
	run3 := *e.runOf(t, id3)
	ok, _ = e.s.settleOnTurn(ctx, run3, job, true)
	require.True(t, ok)
	e.s.finishRun(ctx, id3, job, "killed", "timeout", false)
	assert.Equal(t, "failed", e.runOf(t, id3).Status)
	assert.Equal(t, "turn_failed", reasonOf(t, e.runOf(t, id3)))
}

// An agent that does not end when asked is reported as not gone, so its worktree is left alone: the run is recorded finished either way.
func TestAFinishedRunWhoseAgentWontEndKeepsItsWorktreeSignal(t *testing.T) {
	ctx := context.Background()
	e, rt, _ := turnEnv(t, "turn-stubborn")
	rt.stubborn = true
	id := e.leftRun(t, time.Minute, "sid-x")
	rt.set("claude-sid-x", true)
	ok, gone := e.s.settleOnTurn(ctx, *e.runOf(t, id), store.ScheduledJob{Name: "n", AgentType: "claude"}, false)
	assert.True(t, ok)
	assert.False(t, gone, "the agent is still there: nothing is removed from under it")
	assert.Equal(t, "completed", e.runOf(t, id).Status)
}

// The live path: the launch blocks as the interactive CLI does; the agent's turn ends (stored as a stop after a prompt); the run is completed at once, the launch is released, the agent is ended, and a
// deadline that comes later changes nothing.
func TestLiveRunFinishesOnItsTurnEndWithoutWaitingForTheCLI(t *testing.T) {
	setTurnTimings(t, 0, 20*time.Millisecond, 0)
	e, rt, tr := turnEnv(t, "turn-live")
	t.Cleanup(e.s.waitWatchers)
	rt.set("claude-turn-live", true)
	released := make(chan struct{})
	e.launch(t, func(ctx context.Context) error {
		go func() {
			time.Sleep(150 * time.Millisecond)
			tr.set("turn-live", store.RunTurn{State: store.TurnWorking})
			time.Sleep(100 * time.Millisecond)
			tr.set("turn-live", store.RunTurn{State: store.TurnDone, At: longAgo()})
		}()
		<-ctx.Done() // the CLI does not exit by itself
		close(released)
		return ctx.Err()
	})
	start := time.Now()
	e.s.launchAndWatch(e.runID, store.ScheduledJob{Name: "n", AgentType: "claude"}, runConfig{tag: "scheduled", maxDurationS: 60})
	assert.Less(t, time.Since(start), 10*time.Second, "it did not wait for the deadline")
	<-released
	r := e.run(t)
	assert.Equal(t, "completed", r.Status)
	assert.Equal(t, "turn_complete", reasonOf(t, r))
	assert.Equal(t, []string{"claude-turn-live"}, rt.kills())
	e.s.waitWatchers()
}

// A live run's turn that ended in an error is failed, and a run of a task (tag "task") is not watched for a turn at all.
func TestLiveRunFailedTurnAndTaskRunsAreNotFinishedByOne(t *testing.T) {
	setTurnTimings(t, 0, 20*time.Millisecond, 0)
	e, rt, tr := turnEnv(t, "turn-fail")
	t.Cleanup(e.s.waitWatchers)
	rt.set("claude-turn-fail", true)
	tr.set("turn-fail", store.RunTurn{State: store.TurnFailed, At: longAgo()})
	e.launch(t, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	e.s.launchAndWatch(e.runID, store.ScheduledJob{Name: "n", AgentType: "claude"}, runConfig{tag: "scheduled", maxDurationS: 60})
	r := e.run(t)
	assert.Equal(t, "failed", r.Status)
	assert.Equal(t, "turn_failed", reasonOf(t, r))
	require.NotNil(t, r.ErrorCode)
	assert.Equal(t, "agent_turn_failed", *r.ErrorCode)

	// A one-shot task run ends as before, by its deadline here.
	e2, rt2, tr2 := turnEnv(t, "turn-task")
	t.Cleanup(e2.s.waitWatchers)
	rt2.set("claude-turn-task", true)
	tr2.set("turn-task", store.RunTurn{State: store.TurnDone, At: longAgo()})
	e2.launch(t, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	e2.s.launchAndWatch(e2.runID, store.ScheduledJob{Name: "n", AgentType: "claude"}, runConfig{tag: "task", maxDurationS: 1})
	r2 := e2.run(t)
	assert.Equal(t, "killed", r2.Status)
	assert.Equal(t, "timeout", reasonOf(t, r2))
}

// An adopted run whose turn ends after it was adopted is finished by the watcher's own check, not at the next restart.
func TestAdoptedRunWatcherFinishesWhenTheTurnEndsLater(t *testing.T) {
	setTurnTimings(t, 20*time.Millisecond, 0, -1)
	ctx, cancel := context.WithCancel(context.Background())
	e, rt, tr := turnEnv(t, "turn-later")
	t.Cleanup(func() { cancel(); e.s.waitWatchers() })
	id := e.leftRun(t, time.Minute, "sid-later")
	rt.set("claude-sid-later", true)
	tr.set("sid-later", store.RunTurn{State: store.TurnWorking})
	e.s.reapStaleRuns(ctx)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, "running", e.runOf(t, id).Status)

	tr.set("sid-later", store.RunTurn{State: store.TurnDone, At: longAgo()})
	require.Eventually(t, func() bool { return e.runOf(t, id).Status == "completed" }, 3*time.Second, 20*time.Millisecond)
	assert.Equal(t, "turn_complete", reasonOf(t, e.runOf(t, id)))
	// The run is claimed first and its agent ended right after: the kill may land a moment later than the status.
	require.Eventually(t, func() bool { return len(rt.kills()) == 1 }, 3*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"claude-sid-later"}, rt.kills())
}

// The worktree of a run finished on its turn is removed by the no-force rule once the agent is gone: clean, it goes with its empty branch; with unsaved work it stays; and while the agent is still there
// (a kill that did not take) nothing is removed from under it.
func TestWorktreeOfARunFinishedOnItsTurnGoesOnlyWhenCleanAndTheAgentIsGone(t *testing.T) {
	for _, c := range []struct {
		name            string
		dirty, stubborn bool
		removed         bool
	}{{"clean, agent gone", false, false, true}, {"unsaved work", true, false, false}, {"agent will not end", false, true, false}} {
		ctx, cancel := context.WithCancel(context.Background())
		e, rt, tr := turnEnv(t, "turn-wt-"+c.name)
		rt.stubborn = c.stubborn
		repo := exitGitRepo(t)
		id := e.leftRun(t, time.Minute, "sid-wt")
		rt.set("claude-sid-wt", true)
		tr.set("sid-wt", store.RunTurn{State: store.TurnDone, At: longAgo()})
		_, err := e.ss.UpdateScheduledJob(ctx, e.runOf(t, id).JobID, map[string]interface{}{"repo_path": repo, "cleanup_worktree": 1})
		require.NoError(t, err)
		wt := filepath.Join(e.s.worktreeRoot, fmt.Sprintf("job-run-%d-%s", id, filepath.Base(repo)))
		out, err := exec.Command("git", "-C", repo, "worktree", "add", "-b", fmt.Sprintf("coral/job-run-%d", id), "--", wt, "main").CombinedOutput()
		require.NoError(t, err, string(out))
		require.NoError(t, e.ss.UpdateScheduledRun(ctx, id, map[string]interface{}{"worktree_path": wt}))
		if c.dirty {
			require.NoError(t, os.WriteFile(filepath.Join(wt, "unsaved.txt"), []byte("work"), 0o644))
		}

		e.s.reapStaleRuns(ctx)

		assert.Equal(t, "completed", e.runOf(t, id).Status, c.name)
		if c.removed {
			assert.NoDirExists(t, wt, c.name)
		} else {
			assert.DirExists(t, wt, c.name)
		}
		cancel()
		e.s.waitWatchers()
	}
}
