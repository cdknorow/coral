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

// Orchestrator's point 3 (#6898): a run whose agent ended while Coral was down is "agent_done" only when its last event shows it finished; otherwise it is recorded with its own reason, not a success nobody saw.
func TestARunWhoseAgentEndedWhileCoralWasDownClaimsSuccessOnlyWithProof(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name, last, want string
		fn, failed       bool
	}{
		{"its last event is a stop", "stop", "agent_done", true, false},
		{"its last event is a stop that was a failed turn", "stop", "ended_while_coral_was_down", true, true},
		{"its last event is the session's end", "session_end", "agent_done", true, false},
		{"its last event is a tool call", "tool_use", "ended_while_coral_was_down", true, false},
		{"it reported nothing", "", "ended_while_coral_was_down", true, false},
		{"the last event is not known here", "", "agent_done", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, _ := restartEnv(t, "left-proof-"+c.last+fmt.Sprint(c.failed))
			if c.fn {
				e.s.SetLastEventType(func(context.Context, string) (string, bool) { return c.last, c.failed })
			}
			id := e.leftRun(t, time.Minute, "sid-proof")
			e.s.reapStaleRuns(ctx)
			r := e.runOf(t, id)
			assert.Equal(t, "completed", r.Status)
			require.NotNil(t, r.ExitReason)
			assert.Equal(t, c.want, *r.ExitReason)
		})
	}
}

// Orchestrator's point 2 (#6898): the worktree of a run settled at startup is removed as an in-process run's is (no --force): clean, it goes with its empty branch; with unsaved work it stays.
func TestTheWorktreeOfARunSettledAtStartupIsRemovedWithoutForce(t *testing.T) {
	ctx := context.Background()
	for _, dirty := range []bool{false, true} {
		e, _ := restartEnv(t, fmt.Sprintf("left-wt-%v", dirty))
		repo := exitGitRepo(t)
		id := e.leftRun(t, time.Minute, "sid-wt")
		jobID := e.runOf(t, id).JobID
		_, err := e.ss.UpdateScheduledJob(ctx, jobID, map[string]interface{}{"repo_path": repo, "cleanup_worktree": 1})
		require.NoError(t, err)
		wt := filepath.Join(e.s.worktreeRoot, fmt.Sprintf("job-run-%d-%s", id, filepath.Base(repo)))
		out, err := exec.Command("git", "-C", repo, "worktree", "add", "-b", fmt.Sprintf("coral/job-run-%d", id), "--", wt, "main").CombinedOutput()
		require.NoError(t, err, string(out))
		require.NoError(t, e.ss.UpdateScheduledRun(ctx, id, map[string]interface{}{"worktree_path": wt}))
		if dirty {
			require.NoError(t, os.WriteFile(filepath.Join(wt, "unsaved.txt"), []byte("work"), 0o644))
		}

		e.s.reapStaleRuns(ctx)

		assert.Equal(t, "completed", e.runOf(t, id).Status)
		if dirty {
			assert.FileExists(t, filepath.Join(wt, "unsaved.txt"), "unsaved work: the worktree stays")
			continue
		}
		assert.NoDirExists(t, wt, "a clean worktree goes")
		branches, err := exec.Command("git", "-C", repo, "branch", "--list", fmt.Sprintf("coral/job-run-%d", id)).CombinedOutput()
		require.NoError(t, err)
		assert.Empty(t, string(branches), "with its empty branch")
	}
}

// A run that is not the folder the scheduler makes for it (the job's own repository as its worktree path, a plain run) is never removed.
func TestASettledRunNeverRemovesTheJobsOwnRepository(t *testing.T) {
	ctx := context.Background()
	e, _ := restartEnv(t, "left-own")
	repo := exitGitRepo(t)
	id := e.leftRun(t, time.Minute, "sid-own")
	_, err := e.ss.UpdateScheduledJob(ctx, e.runOf(t, id).JobID, map[string]interface{}{"repo_path": repo, "cleanup_worktree": 1})
	require.NoError(t, err)
	require.NoError(t, e.ss.UpdateScheduledRun(ctx, id, map[string]interface{}{"worktree_path": repo}))
	e.s.reapStaleRuns(ctx)
	assert.DirExists(t, repo)
	assert.DirExists(t, filepath.Join(repo, ".git"))
}

// Security's LOW 2 on #6904: a run row that names another registered worktree of the job's repository (a person's own linked checkout, not the folder the scheduler makes for the run) is neither removed nor
// its branch deleted when the run is settled at startup: only IsRunWorktreeFolder's folder is Coral's. (The kept-worktrees list applies the same rule: TestAJobsOwnCheckoutIsNeverListedOrRemoved in server/routes.)
func TestASettledRunNeverRemovesAnotherLinkedCheckoutOfTheRepository(t *testing.T) {
	ctx := context.Background()
	e, _ := restartEnv(t, "left-linked")
	repo := exitGitRepo(t)
	own := filepath.Join(t.TempDir(), "feature") // a normal `git worktree add ../feature`
	out, err := exec.Command("git", "-C", repo, "worktree", "add", "-b", "feature", "--", own, "main").CombinedOutput()
	require.NoError(t, err, string(out))
	id := e.leftRun(t, time.Minute, "sid-linked")
	_, err = e.ss.UpdateScheduledJob(ctx, e.runOf(t, id).JobID, map[string]interface{}{"repo_path": repo, "cleanup_worktree": 1})
	require.NoError(t, err)
	require.NoError(t, e.ss.UpdateScheduledRun(ctx, id, map[string]interface{}{"worktree_path": own}))

	e.s.reapStaleRuns(ctx)

	assert.Equal(t, "completed", e.runOf(t, id).Status, "the run is settled")
	assert.DirExists(t, own, "the person's linked checkout is not removed")
	list, err := exec.Command("git", "-C", repo, "worktree", "list").CombinedOutput()
	require.NoError(t, err)
	assert.Contains(t, string(list), "feature", "and still registered")
	branches, err := exec.Command("git", "-C", repo, "branch", "--list", "feature").CombinedOutput()
	require.NoError(t, err)
	assert.Contains(t, string(branches), "feature", "with its branch")
}

// Security's LOW on #6904: the run's agent type is the one its session row says, not the job's current one: a job edited from claude to codex since the run started must not make a live agent look gone.
func TestALiveAgentIsNotReadAsGoneWhenTheJobsAgentTypeWasEdited(t *testing.T) {
	ctx := context.Background()
	e, rt := restartEnv(t, "left-typed")
	adoptedRunPoll = time.Hour
	defer func() { adoptedRunPoll = 30 * time.Second }()
	id := e.leftRun(t, time.Minute, "sid-typed")
	require.NoError(t, e.sess.RegisterLiveSession(ctx, &store.LiveSession{SessionID: "sid-typed", AgentType: "claude", AgentName: "job", WorkingDir: "/tmp"}))
	_, err := e.ss.UpdateScheduledJob(ctx, e.runOf(t, id).JobID, map[string]interface{}{"agent_type": "codex"}) // edited since the run started
	require.NoError(t, err)
	rt.set("claude-sid-typed", true) // the session that really exists

	wctx, cancel := context.WithCancel(ctx)
	e.s.reapStaleRuns(wctx)
	assert.Equal(t, "running", e.runOf(t, id).Status, "alive, found by its own agent type: adopted, not finished as gone")
	cancel() // the adopted run's watcher ends with the context
	e.s.waitWatchers()
}
