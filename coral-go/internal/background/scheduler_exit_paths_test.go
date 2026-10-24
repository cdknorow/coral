package background

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/store"
)

// Every way a job run ends records its outcome and cleans up (#4599 second ranking, item 8, Backend's SHOULD):
// a finished run, a failed launch, a timeout, a user's kill, a Coral shutdown, a worktree that can't be made, and
// a worktree made and removed. launchAndWatch runs here on the test's goroutine.

type exitEnv struct {
	s     *JobScheduler
	ss    *store.ScheduleStore
	sess  *store.SessionStore
	rt    *killRecorder
	runID int64
	sid   string
	dir   string // the working dir the launch was given
}

func newExitEnv(t *testing.T, sid string) *exitEnv {
	t.Helper()
	ctx := context.Background()
	db := setupTestDB(t)
	e := &exitEnv{ss: store.NewScheduleStore(db), sess: store.NewSessionStore(db), rt: &killRecorder{}, sid: sid}
	e.s = NewJobScheduler(e.ss, time.Minute)
	e.s.SetRuntime(e.rt)
	e.s.SetSessionStore(e.sess)
	// A scheduled run asks its CLI whether it is signed in (#5469): these tests never reach a real claude, a configured path that cannot be run is "not asked".
	require.NoError(t, e.sess.SetSetting(ctx, agent.CLIPathSettingKey("claude"), filepath.Join(t.TempDir(), "no-claude")))
	e.s.SetWorktreeRoot(t.TempDir())
	job, err := e.ss.CreateScheduledJob(ctx, &store.ScheduledJob{Name: "exit", CronExpr: "0 * * * *", AgentType: "claude", Prompt: "p"})
	require.NoError(t, err)
	e.runID, err = e.ss.CreateScheduledRun(ctx, job.ID, time.Now().UTC().Format(store.ISOFormat), "pending")
	require.NoError(t, err)
	t.Cleanup(func() { // the auto-accept maps are global: leave nothing of this session behind
		autoAcceptMu.Lock()
		delete(AutoAcceptSessions, sid)
		delete(AutoAcceptCounts, sid)
		delete(AutoAcceptLimits, sid)
		autoAcceptMu.Unlock()
	})
	return e
}

// launch makes the run's launch record its session, note its working dir, then do f.
func (e *exitEnv) launch(t *testing.T, f func(ctx context.Context) error) {
	e.s.SetLaunchFn(func(ctx context.Context, job store.ScheduledJob, runID int64) error {
		require.NoError(t, e.ss.UpdateScheduledRun(context.Background(), runID, map[string]interface{}{"session_id": e.sid}))
		e.dir = job.RepoPath
		return f(ctx)
	})
}

func (e *exitEnv) run(t *testing.T) *store.ScheduledRun {
	t.Helper()
	r, err := e.ss.GetScheduledRun(context.Background(), e.runID)
	require.NoError(t, err)
	return r
}

func (e *exitEnv) tagged(t *testing.T) []string {
	tags, err := e.sess.GetSessionTags(context.Background(), e.sid)
	require.NoError(t, err)
	var names []string
	for _, tg := range tags {
		names = append(names, tg.Name)
	}
	return names
}

func autoAccepted(sid string) (registered bool, limit int, counted bool) {
	autoAcceptMu.Lock()
	defer autoAcceptMu.Unlock()
	_, registered = AutoAcceptSessions[sid]
	limit = AutoAcceptLimits[sid]
	_, counted = AutoAcceptCounts[sid]
	return
}

func TestRunFinishesTagsAndUnregistersAutoAccept(t *testing.T) {
	e := newExitEnv(t, "exit-done")
	autoAcceptMu.Lock()
	AutoAcceptCounts[e.sid] = 2 // counted during the run
	autoAcceptMu.Unlock()
	e.launch(t, func(context.Context) error { return nil })
	e.s.launchAndWatch(e.runID, store.ScheduledJob{AgentType: "claude"}, runConfig{autoAccept: true, maxAutoAccepts: 7, tag: "scheduled"})
	r := e.run(t)
	assert.Equal(t, "completed", r.Status)
	require.NotNil(t, r.ExitReason)
	assert.Equal(t, "agent_done", *r.ExitReason)
	assert.NotNil(t, r.FinishedAt)
	assert.Equal(t, []string{"scheduled"}, e.tagged(t), "the session is tagged")
	registered, limit, counted := autoAccepted(e.sid)
	assert.False(t, registered, "auto-accept unregistered at the end")
	assert.Zero(t, limit, "its limit too")
	assert.False(t, counted, "and its count")
	assert.Empty(t, e.rt.kills(), "a finished run kills nothing")
}

func TestAfterLaunchRegistersAutoAcceptOnceTheRunHasASession(t *testing.T) {
	e := newExitEnv(t, "exit-during")
	e.s.afterLaunch(context.Background(), e.runID, store.ScheduledJob{AgentType: "claude"}, runConfig{autoAccept: true, maxAutoAccepts: 7})
	registered, _, _ := autoAccepted(e.sid)
	assert.False(t, registered, "no session recorded yet: nothing registered")
	require.NoError(t, e.ss.UpdateScheduledRun(context.Background(), e.runID, map[string]interface{}{"session_id": e.sid}))
	e.s.afterLaunch(context.Background(), e.runID, store.ScheduledJob{AgentType: "claude"}, runConfig{autoAccept: true, maxAutoAccepts: 7})
	registered, limit, _ := autoAccepted(e.sid)
	assert.True(t, registered)
	assert.Equal(t, 7, limit)
	autoAcceptMu.Lock()
	assert.Equal(t, "claude-"+e.sid, AutoAcceptSessions[e.sid])
	autoAcceptMu.Unlock()
}

func TestRunWithAFailedLaunchIsFailedKilledAndNotTagged(t *testing.T) {
	e := newExitEnv(t, "exit-failed")
	e.launch(t, func(context.Context) error { return errors.New("the CLI would not start") })
	e.s.launchAndWatch(e.runID, store.ScheduledJob{AgentType: "codex"}, runConfig{autoAccept: true, maxAutoAccepts: 3, tag: "task"})
	r := e.run(t)
	assert.Equal(t, "failed", r.Status)
	require.NotNil(t, r.ExitReason)
	assert.Equal(t, "the CLI would not start", *r.ExitReason)
	assert.Equal(t, []string{"codex-" + e.sid}, e.rt.kills(), "its session is killed")
	assert.Empty(t, e.tagged(t), "a failed launch is not tagged")
	registered, _, _ := autoAccepted(e.sid)
	assert.False(t, registered)
}

func TestRunThatTimesOutIsKilled(t *testing.T) {
	e := newExitEnv(t, "exit-timeout")
	e.launch(t, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	e.s.launchAndWatch(e.runID, store.ScheduledJob{AgentType: "claude"}, runConfig{maxDurationS: 1})
	r := e.run(t)
	assert.Equal(t, "killed", r.Status)
	require.NotNil(t, r.ExitReason)
	assert.Equal(t, "timeout", *r.ExitReason)
	assert.Equal(t, []string{"claude-" + e.sid}, e.rt.kills())
}

func TestRunTheUserKilledKeepsItsKill(t *testing.T) {
	e := newExitEnv(t, "exit-userkill")
	// KillRun records the kill and kills the agent itself; its cancel then ends the launch with an error.
	e.launch(t, func(context.Context) error {
		require.NoError(t, e.ss.UpdateScheduledRun(context.Background(), e.runID, map[string]interface{}{"status": "killed", "exit_reason": "killed by user"}))
		return context.Canceled
	})
	e.s.launchAndWatch(e.runID, store.ScheduledJob{AgentType: "claude"}, runConfig{})
	r := e.run(t)
	assert.Equal(t, "killed", r.Status, "not overwritten with failed")
	require.NotNil(t, r.ExitReason)
	assert.Equal(t, "killed by user", *r.ExitReason)
	assert.Empty(t, e.rt.kills(), "the user's kill already killed it")
}

func TestRunAtShutdownKillsNothing(t *testing.T) {
	e := newExitEnv(t, "exit-shutdown")
	ctx, cancel := context.WithCancel(context.Background())
	e.s.parentCtx = ctx
	e.launch(t, func(context.Context) error { cancel(); return errors.New("cancelled by shutdown") })
	e.s.launchAndWatch(e.runID, store.ScheduledJob{AgentType: "claude"}, runConfig{})
	assert.Empty(t, e.rt.kills(), "agents outlive a Coral restart: a shutdown kills none")
}

func exitGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "first"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	return dir
}

func TestRunWorktreeIsMadeThenRemoved(t *testing.T) {
	e := newExitEnv(t, "exit-worktree")
	repo := exitGitRepo(t)
	e.launch(t, func(context.Context) error {
		_, err := os.Stat(e.dir)
		require.NoError(t, err, "the agent works in the run's worktree")
		return nil
	})
	e.s.launchAndWatch(e.runID, store.ScheduledJob{AgentType: "claude"}, runConfig{repoPath: repo, createWorktree: true, cleanupWorktree: true})
	assert.NotEqual(t, repo, e.dir, "not in the repository itself")
	r := e.run(t)
	assert.Equal(t, "completed", r.Status)
	require.NotNil(t, r.WorktreePath)
	assert.Equal(t, e.dir, *r.WorktreePath)
	_, err := os.Stat(e.dir)
	assert.True(t, os.IsNotExist(err), "the worktree is removed afterwards: %v", err)
}

func TestRunWithAWorktreeThatCannotBeMadeFailsBeforeLaunching(t *testing.T) {
	launched := false
	e := newExitEnv(t, "exit-noworktree")
	e.launch(t, func(context.Context) error { launched = true; return nil })
	e.s.launchAndWatch(e.runID, store.ScheduledJob{AgentType: "claude"}, runConfig{repoPath: exitGitRepo(t), createWorktree: true, baseBranch: "--upload-pack=evil"})
	r := e.run(t)
	assert.Equal(t, "failed", r.Status)
	require.NotNil(t, r.ErrorMsg)
	assert.Contains(t, *r.ErrorMsg, "base_branch refused")
	assert.False(t, launched, "nothing launched")

	e2 := newExitEnv(t, "exit-noworktree-2")
	e2.launch(t, func(context.Context) error { launched = true; return nil })
	e2.s.launchAndWatch(e2.runID, store.ScheduledJob{AgentType: "claude"}, runConfig{repoPath: filepath.Join(t.TempDir(), "not-a-repo"), createWorktree: true})
	r = e2.run(t)
	assert.Equal(t, "failed", r.Status)
	require.NotNil(t, r.ErrorMsg)
	assert.Contains(t, *r.ErrorMsg, "git worktree add failed")
	assert.False(t, launched)
}

// A job that keeps its worktree (create on, cleanup off) finds it, and its run branch, still there after the run.
func TestRunWorktreeIsKeptWhenTheJobKeepsIt(t *testing.T) {
	e := newExitEnv(t, "exit-keep")
	repo := exitGitRepo(t)
	e.launch(t, func(context.Context) error { return nil })
	e.s.launchAndWatch(e.runID, store.ScheduledJob{AgentType: "claude"}, runConfig{repoPath: repo, createWorktree: true, cleanupWorktree: false})
	require.Equal(t, "completed", e.run(t).Status)
	_, err := os.Stat(e.dir)
	require.NoError(t, err, "the worktree is kept")
	out, err := exec.Command("git", "-C", repo, "branch", "--list", fmt.Sprintf("coral/job-run-%d", e.runID)).Output()
	require.NoError(t, err)
	require.Contains(t, string(out), fmt.Sprintf("coral/job-run-%d", e.runID), "and its branch")
}

// #6701: a job whose repository is on master (or any branch but main) runs: the base branch is the one the job names, else the repository's current branch, and a job stored with the old default
// "main" falls to the current branch where the repository has no main. Before, every tick failed with "invalid reference: main".
func TestRunWorktreeInARepositoryNotOnMain(t *testing.T) {
	for _, base := range []string{"", "main"} {
		e := newExitEnv(t, "exit-notmain-"+base)
		repo := t.TempDir()
		for _, args := range [][]string{{"init", "-q", "-b", "master"}, {"-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "first"}} {
			cmd := exec.Command("git", args...)
			cmd.Dir = repo
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, string(out))
		}
		launched := false
		e.launch(t, func(context.Context) error { launched = true; return nil })
		e.s.launchAndWatch(e.runID, store.ScheduledJob{AgentType: "claude"}, runConfig{repoPath: repo, createWorktree: true, cleanupWorktree: true, baseBranch: base})
		r := e.run(t)
		assert.Equal(t, "completed", r.Status, "base %q: %v", base, r.ErrorMsg)
		assert.True(t, launched, "base %q: the agent launched in the run's worktree", base)
	}
}

// A job run's worktree with unsaved work in it stays after the run, with its branch: removal is without --force (#6701).
func TestRunWorktreeWithUnsavedWorkIsKept(t *testing.T) {
	e := newExitEnv(t, "exit-worktree-dirty")
	repo := exitGitRepo(t)
	e.launch(t, func(context.Context) error {
		return os.WriteFile(filepath.Join(e.dir, "unsaved.txt"), []byte("the agent's work"), 0o644)
	})
	e.s.launchAndWatch(e.runID, store.ScheduledJob{AgentType: "claude"}, runConfig{repoPath: repo, createWorktree: true, cleanupWorktree: true})
	assert.FileExists(t, filepath.Join(e.dir, "unsaved.txt"), "the work is not deleted")
	out, err := exec.Command("git", "-C", repo, "branch", "--list", "coral/job-run-*").CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "coral/job-run-", "its branch stays too")
}

// A run whose base branch is missing fails with one plain sentence and the fixed word a row and the Runs table show (#6701): no raw git text.
func TestRunWithAMissingBaseBranchSaysSoPlainly(t *testing.T) {
	e := newExitEnv(t, "exit-missing-base")
	repo := exitGitRepo(t)
	launched := false
	e.launch(t, func(context.Context) error { launched = true; return nil })
	e.s.launchAndWatch(e.runID, store.ScheduledJob{AgentType: "claude"}, runConfig{repoPath: repo, createWorktree: true, baseBranch: "release"})
	r := e.run(t)
	assert.Equal(t, "failed", r.Status)
	require.NotNil(t, r.ErrorMsg)
	assert.Equal(t, "The base branch release does not exist in "+repo+". Its branches: main.", *r.ErrorMsg)
	require.NotNil(t, r.ErrorCode)
	assert.Equal(t, "base_branch_missing", *r.ErrorCode)
	assert.False(t, launched)

	e2 := newExitEnv(t, "exit-bad-repo")
	e2.launch(t, func(context.Context) error { return nil })
	e2.s.launchAndWatch(e2.runID, store.ScheduledJob{AgentType: "claude"}, runConfig{repoPath: filepath.Join(t.TempDir(), "not-a-repo"), createWorktree: true})
	r = e2.run(t)
	require.NotNil(t, r.ErrorCode)
	assert.Equal(t, "worktree_failed", *r.ErrorCode)
}

// Security's #6741 and the Orchestrator's check: a job that made no worktree never cleans one up: its repository (maybe a person's own linked checkout) is not touched.
func TestRunWithoutAWorktreeNeverRemovesTheJobsOwnCheckout(t *testing.T) {
	e := newExitEnv(t, "exit-own-checkout")
	main := exitGitRepo(t)
	own := filepath.Join(t.TempDir(), "feature")
	out, err := exec.Command("git", "-C", main, "worktree", "add", "-b", "feature", "--", own, "main").CombinedOutput()
	require.NoError(t, err, string(out))
	e.launch(t, func(context.Context) error { return nil })
	e.s.launchAndWatch(e.runID, store.ScheduledJob{AgentType: "claude"}, runConfig{repoPath: own, createWorktree: false, cleanupWorktree: true})
	assert.DirExists(t, own, "the person's checkout is not removed")
	e.s.cleanupWorktree(own, own, "", "") // even called with the repository as the worktree, it does nothing
	assert.DirExists(t, own)
}

func TestIsRunWorktreeFolder(t *testing.T) {
	root := filepath.Join(t.TempDir(), "worktrees")
	repo := filepath.Join(t.TempDir(), "proj")
	assert.True(t, IsRunWorktreeFolder(root, repo, 7, filepath.Join(root, "job-run-7-proj")))
	assert.True(t, IsRunWorktreeFolder(root, repo, 7, repo+"_task_run_7"), "the legacy folder beside the repository")
	assert.False(t, IsRunWorktreeFolder(root, repo, 7, repo), "never the repository itself")
	assert.False(t, IsRunWorktreeFolder(root, repo, 7, filepath.Join(root, "job-run-8-proj")), "another run's")
	assert.False(t, IsRunWorktreeFolder(root, repo, 7, filepath.Join(root, "job-run-7-other")), "another repository's name")
	assert.False(t, IsRunWorktreeFolder(root, repo, 7, filepath.Join(root, "x", "job-run-7-proj")), "not directly in the folder")
	assert.False(t, IsRunWorktreeFolder("", repo, 7, filepath.Join(root, "job-run-7-proj")), "no root, no folder")
	assert.False(t, IsRunWorktreeFolder(root, "", 7, filepath.Join(root, "job-run-7-")))
}

// #6842 (#6839 finding 2): a run whose agent exits within seconds without ever reporting an event is failed with a plain reason and a fixed code, not completed/agent_done. One that reported events (a
// one-shot that ends fast on purpose), one that ran longer than the window, and an agent type that reports no events are left as they were.
func TestRunWhoseAgentExitedBeforeItStartedIsFailed(t *testing.T) {
	cases := []struct {
		name      string
		agentType string
		events    bool
		window    time.Duration
		wantFail  bool
	}{
		{"exits at once, no event", "claude", false, 15 * time.Second, true},
		{"exits at once, codex, no event", "codex", false, 15 * time.Second, true},
		{"exits at once but reported events", "claude", true, 15 * time.Second, false},
		{"ran longer than the window", "claude", false, 0, false},
		{"an agent that reports no events", "agy", false, 15 * time.Second, false},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newExitEnv(t, fmt.Sprintf("exit-early-%d", i))
			defer func(w time.Duration) { agentExitedEarlyWindow = w }(agentExitedEarlyWindow)
			agentExitedEarlyWindow = c.window
			e.s.SetEventCheck(func(context.Context, string) bool { return c.events })
			e.launch(t, func(context.Context) error { time.Sleep(20 * time.Millisecond); return nil })
			e.s.launchAndWatch(e.runID, store.ScheduledJob{AgentType: c.agentType}, runConfig{repoPath: t.TempDir()})
			r := e.run(t)
			if !c.wantFail {
				assert.Equal(t, "completed", r.Status)
				assert.Nil(t, r.ErrorCode)
				return
			}
			assert.Equal(t, "failed", r.Status)
			require.NotNil(t, r.ExitReason)
			assert.Equal(t, "the agent exited before it started: it ended within seconds without reporting anything", *r.ExitReason)
			require.NotNil(t, r.ErrorCode)
			assert.Equal(t, "agent_exited_before_start", *r.ErrorCode)
			require.NotNil(t, r.ErrorMsg)
			assert.Contains(t, *r.ErrorMsg, "exited before it started")
		})
	}
}
