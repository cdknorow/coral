package background

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/agentsignin"
	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/claudestub"
	"github.com/cdknorow/coral/internal/store"
)

// #5469: a background caller never starts a signed-out Claude: its sign-in flow would hold the login. The stand-in "claude" answers "auth status --json" from a state file and
// logs every other start ("RAN ...") to a log, so a test can see that the CLI was asked and never run.
type signinCLI struct {
	bin, state, log string
}

func newSigninCLI(t *testing.T, signedIn bool) *signinCLI {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary")
	}
	claudestub.Isolate(t)
	dir := t.TempDir()
	c := &signinCLI{bin: filepath.Join(dir, "claude-fake"), state: filepath.Join(dir, "state"), log: filepath.Join(dir, "log")}
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then echo 9.9.9; exit 0; fi\n" +
		"if [ \"$1\" = auth ] && [ \"$2\" = status ]; then\n" +
		"  echo \"ASKED\" >> '" + c.log + "'\n" +
		"  if [ \"$(cat '" + c.state + "')\" = in ]; then echo '{\"loggedIn\":true}'; exit 0; fi\n" +
		"  echo '{\"loggedIn\":false}'; exit 1\n" +
		"fi\n" +
		"echo \"RAN $*\" >> '" + c.log + "'\n" +
		"cat > /dev/null\n" +
		"echo '{\"type\":\"result\",\"result\":\"a goal\",\"is_error\":false}'\n"
	require.NoError(t, os.WriteFile(c.bin, []byte(script), 0o755))
	c.set(signedIn)
	// Nothing learned in another test stands in this one.
	agentsignin.Forget(at.Claude, c.bin)
	t.Cleanup(func() { agentsignin.Forget(at.Claude, c.bin) })
	return c
}

func (c *signinCLI) set(signedIn bool) {
	v := "out"
	if signedIn {
		v = "in"
	}
	os.WriteFile(c.state, []byte(v), 0o600)
}

func (c *signinCLI) lines() []string {
	b, _ := os.ReadFile(c.log)
	return strings.Fields(strings.ReplaceAll(string(b), "\n", " "))
}

func (c *signinCLI) ran() bool {
	b, _ := os.ReadFile(c.log)
	return strings.Contains(string(b), "RAN")
}

func TestBackgroundCallersNeverStartASignedOutClaude(t *testing.T) {
	c := newSigninCLI(t, false)
	ctx := context.Background()

	_, err := callClaudeForKnowledge(ctx, c.bin, 10*time.Second, "system", "content")
	assert.True(t, agentsignin.IsNotSignedIn(err), "knowledge: %v", err)

	_, err = runGoalCLI(ctx, c.bin, "prompt")
	assert.True(t, agentsignin.IsNotSignedIn(err), "goal: %v", err)

	_, err = callClaude(ctx, map[string]string{agent.CLIPathSettingKey(at.Claude): c.bin}, "transcript")
	require.Error(t, err)
	assert.Equal(t, "Claude is not signed in", err.Error(), "summary")

	_, err = checkClaudeCLI(ctx, map[string]string{agent.CLIPathSettingKey(at.Claude): c.bin})
	require.Error(t, err)
	assert.Equal(t, "Claude is not signed in", err.Error(), "distill")

	assert.False(t, c.ran(), "the CLI was asked, never run")
	assert.NotEmpty(t, c.lines())
}

// Callers ask rarely: ten jobs in a row ask the CLI once, and they run once Claude is signed in.
func TestBackgroundCallersAskRarelyAndRunOnceSignedIn(t *testing.T) {
	c := newSigninCLI(t, false)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		_, err := runGoalCLI(ctx, c.bin, "prompt")
		require.True(t, agentsignin.IsNotSignedIn(err))
	}
	assert.Equal(t, []string{"ASKED"}, c.lines(), "one question for ten jobs")
	c.set(true)
	agentsignin.Forget(at.Claude, c.bin) // what a fresh ask (a workflow run or a distill) does for itself
	res, err := runGoalCLI(ctx, c.bin, "prompt")
	require.NoError(t, err)
	assert.Equal(t, "a goal", res.Text)
	assert.True(t, c.ran())
}

// The batch summarizer leaves a session pending (never marks it failed) while Claude is signed out.
func TestBatchSummarizerLeavesSessionsPendingWhenSignedOut(t *testing.T) {
	db := setupTestDB(t)
	ss := store.NewSessionStore(db)
	_, err := db.ExecContext(context.Background(), "INSERT INTO summarizer_queue (session_id, status) VALUES ('s-1', 'pending')")
	require.NoError(t, err)
	old := signedOutWait
	signedOutWait = 20 * time.Millisecond
	t.Cleanup(func() { signedOutWait = old })

	calls := make(chan struct{}, 100)
	bs := NewBatchSummarizer(ss, func(ctx context.Context, id string) error {
		calls <- struct{}{}
		return &agentsignin.NotSignedInError{Agent: at.Claude}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	bs.Run(ctx)
	assert.GreaterOrEqual(t, len(calls), 2, "it asked again after the wait")
	pending, err := ss.GetPendingSummaries(context.Background(), 5)
	require.NoError(t, err)
	assert.Equal(t, []string{"s-1"}, pending, "still pending, not marked failed")
}

func signInWorkflow(t *testing.T, wfStore *store.WorkflowStore, steps []StepDef) *store.Workflow {
	t.Helper()
	stepsJSON, _ := json.Marshal(steps)
	return createTestWorkflow(t, wfStore, t.TempDir(), string(stepsJSON))
}

func runToEnd(t *testing.T, runner *WorkflowRunner, wfStore *store.WorkflowStore, wf *store.Workflow) (*store.WorkflowRun, []StepResult) {
	t.Helper()
	run, err := runner.TriggerWorkflow(context.Background(), wf, "api", nil)
	require.NoError(t, err)
	waitForRun(t, wfStore, run.ID, 20*time.Second)
	final, err := wfStore.GetWorkflowRunDirect(context.Background(), run.ID)
	require.NoError(t, err)
	var results []StepResult
	require.NoError(t, json.Unmarshal([]byte(final.StepResults), &results))
	return final, results
}

// The operator's rule: the check runs before the run's first step; signed out fails the whole run at once with the sentence, no step starts, the CLI is never run.
func TestWorkflowRunFailsAtOnceWhenClaudeIsSignedOut(t *testing.T) {
	c := newSigninCLI(t, false)
	db := setupTestDB(t)
	require.NoError(t, store.NewSessionStore(db).SetSetting(context.Background(), agent.CLIPathSettingKey(at.Claude), c.bin))
	wfStore := store.NewWorkflowStore(db)
	rt := newWfMockRuntime()
	runner := NewWorkflowRunner(wfStore, &AgentLauncher{runtime: rt}, rt, nil, nil, t.TempDir(), "localhost", 8420)

	marker := filepath.Join(t.TempDir(), "first-step-ran")
	wf := signInWorkflow(t, wfStore, []StepDef{
		{Name: "first", Type: "shell", Command: "echo ran > '" + marker + "'"},
		{Name: "ask", Type: "agent", Prompt: "hi", Agent: &AgentStepConfig{AgentType: at.Claude}},
	})
	final, results := runToEnd(t, runner, wfStore, wf)
	assert.Equal(t, "failed", final.Status)
	require.NotNil(t, final.ErrorMsg)
	assert.Equal(t, "Claude is not signed in", *final.ErrorMsg)
	require.NotNil(t, final.ErrorCode)
	assert.Equal(t, "not_signed_in", *final.ErrorCode)
	require.NotNil(t, final.AgentType)
	assert.Equal(t, "claude", *final.AgentType)
	require.Len(t, results, 2)
	for _, r := range results {
		assert.Equal(t, "skipped", r.Status, "no step started: %s", r.Name)
	}
	_, err := os.Stat(marker)
	assert.True(t, os.IsNotExist(err), "the shell step before the agent step did not run")
	assert.False(t, c.ran())

	// Signed in: the very next run is not refused on the old answer, and runs.
	c.set(true)
	final, results = runToEnd(t, runner, wfStore, wf)
	assert.Equal(t, "completed", final.Status, "%v", final.ErrorMsg)
	assert.Nil(t, final.ErrorCode, "a run that ran has no error code")
	assert.Equal(t, "completed", results[1].Status)
	assert.True(t, c.ran())
}

// A workflow with no agent step never touches the CLI, and an agent step of a CLI with no status command (agy) is not checked.
func TestWorkflowWithoutAClaudeStepDoesNotAskTheCLI(t *testing.T) {
	c := newSigninCLI(t, false)
	db := setupTestDB(t)
	require.NoError(t, store.NewSessionStore(db).SetSetting(context.Background(), agent.CLIPathSettingKey(at.Claude), c.bin))
	wfStore := store.NewWorkflowStore(db)
	rt := newWfMockRuntime()
	runner := NewWorkflowRunner(wfStore, &AgentLauncher{runtime: rt}, rt, nil, nil, t.TempDir(), "localhost", 8420)
	wf := signInWorkflow(t, wfStore, []StepDef{{Name: "echo", Type: "shell", Command: "echo hi"}})
	final, _ := runToEnd(t, runner, wfStore, wf)
	assert.Equal(t, "completed", final.Status)
	assert.Empty(t, c.lines(), "nothing asked the CLI")

	assert.Empty(t, runner.signInPreflight(context.Background(), []StepDef{{Type: "agent", Agent: &AgentStepConfig{AgentType: at.Agy}}}, nil))
	assert.Empty(t, c.lines())
}

// The default agent counts: a step with no agent of its own uses the workflow's.
func TestWorkflowPreflightUsesTheDefaultAgent(t *testing.T) {
	c := newSigninCLI(t, false)
	db := setupTestDB(t)
	require.NoError(t, store.NewSessionStore(db).SetSetting(context.Background(), agent.CLIPathSettingKey(at.Claude), c.bin))
	runner := NewWorkflowRunner(store.NewWorkflowStore(db), nil, nil, nil, nil, t.TempDir(), "localhost", 8420)
	nsi := runner.signInPreflight(context.Background(), []StepDef{{Name: "a", Type: "agent"}}, &AgentStepConfig{AgentType: at.Claude})
	require.NotNil(t, nsi)
	assert.Equal(t, "Claude is not signed in", nsi.Error())
	assert.Equal(t, at.Claude, nsi.Agent)
	assert.Empty(t, runner.signInPreflight(context.Background(), []StepDef{{Name: "s", Type: "shell"}}, &AgentStepConfig{AgentType: at.Claude}))
}

// A scheduled prompt job whose CLI is signed out is refused before a worktree or a launch: failed, the sentence, the fixed code and the CLI named; and such a run does not count
// toward the three-failures pause.
func TestScheduledRunIsRefusedWhenItsCLIIsSignedOut(t *testing.T) {
	c := newSigninCLI(t, false)
	e := newExitEnv(t, "sess-signin")
	require.NoError(t, e.sess.SetSetting(context.Background(), agent.CLIPathSettingKey(at.Claude), c.bin))
	launched := false
	e.launch(t, func(ctx context.Context) error { launched = true; return nil })
	repo := exitGitRepo(t)
	e.s.launchAndWatch(e.runID, store.ScheduledJob{Name: "exit", AgentType: at.Claude}, runConfig{repoPath: repo, createWorktree: true, tag: "scheduled"})

	assert.False(t, launched, "the agent was never launched")
	run := e.run(t)
	assert.Equal(t, "failed", run.Status)
	require.NotNil(t, run.ErrorMsg)
	assert.Equal(t, "Claude is not signed in", *run.ErrorMsg)
	require.NotNil(t, run.ErrorCode)
	assert.Equal(t, "not_signed_in", *run.ErrorCode)
	require.NotNil(t, run.AgentType)
	assert.Equal(t, "claude", *run.AgentType)
	assert.False(t, c.ran())
	_, err := os.Stat(e.s.runWorktreeDir(repo, e.runID))
	assert.True(t, os.IsNotExist(err), "no worktree was made")

	// Three such runs in a row do not pause the job.
	ctx := context.Background()
	job, err := e.ss.CreateScheduledJob(ctx, &store.ScheduledJob{Name: "pause", CronExpr: "0 * * * *", AgentType: at.Claude, Prompt: "p"})
	require.NoError(t, err)
	for i := 0; i < jobFailurePauseAfter; i++ {
		id, err := e.ss.CreateScheduledRun(ctx, job.ID, time.Now().UTC().Add(time.Duration(i)*time.Minute).Format(store.ISOFormat), "pending")
		require.NoError(t, err)
		require.NoError(t, e.ss.UpdateScheduledRun(ctx, id, map[string]interface{}{"status": "failed", "error_code": "not_signed_in", "agent_type": "claude", "error_msg": "Claude is not signed in"}))
	}
	assert.False(t, e.s.pauseIfFailing(ctx, *job), "signed-out runs are not the job's failures")
}

// A signed-in CLI launches as before (a "task" run is never checked).
func TestScheduledRunLaunchesWhenSignedIn(t *testing.T) {
	c := newSigninCLI(t, true)
	e := newExitEnv(t, "sess-signin-ok")
	require.NoError(t, e.sess.SetSetting(context.Background(), agent.CLIPathSettingKey(at.Claude), c.bin))
	launched := false
	e.launch(t, func(ctx context.Context) error { launched = true; return nil })
	e.s.launchAndWatch(e.runID, store.ScheduledJob{Name: "exit", AgentType: at.Claude}, runConfig{tag: "scheduled"})
	assert.True(t, launched)
	assert.Equal(t, "completed", e.run(t).Status)
}

// A scheduled workflow job whose run was refused carries the workflow run's code and CLI into its own run record (the scheduler polls the workflow run every five seconds).
func TestScheduledWorkflowJobCarriesTheWorkflowRunsSignInCode(t *testing.T) {
	c := newSigninCLI(t, false)
	e := newExitEnv(t, "sess-wf-signin")
	require.NoError(t, e.sess.SetSetting(context.Background(), agent.CLIPathSettingKey(at.Claude), c.bin))
	db := setupTestDB(t)
	require.NoError(t, store.NewSessionStore(db).SetSetting(context.Background(), agent.CLIPathSettingKey(at.Claude), c.bin))
	wfStore := store.NewWorkflowStore(db)
	rt := newWfMockRuntime()
	runner := NewWorkflowRunner(wfStore, &AgentLauncher{runtime: rt}, rt, nil, nil, t.TempDir(), "localhost", 8420)
	e.s.SetWorkflowRunner(runner, wfStore)
	wf := signInWorkflow(t, wfStore, []StepDef{{Name: "ask", Type: "agent", Prompt: "hi", Agent: &AgentStepConfig{AgentType: at.Claude}}})

	e.s.triggerWorkflowForJob(e.runID, store.ScheduledJob{Name: "wf", JobType: "workflow", WorkflowID: &wf.ID})
	run := e.run(t)
	assert.Equal(t, "failed", run.Status)
	require.NotNil(t, run.ErrorMsg)
	assert.Equal(t, "Claude is not signed in", *run.ErrorMsg)
	require.NotNil(t, run.ErrorCode)
	assert.Equal(t, "not_signed_in", *run.ErrorCode)
	require.NotNil(t, run.AgentType)
	assert.Equal(t, "claude", *run.AgentType)
	assert.False(t, c.ran())
}
