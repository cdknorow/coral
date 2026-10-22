package background

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/agentsignin"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/claudestub"
	"github.com/subgentic/coral/internal/store"
)

// #5469 / #5472: a background run of `claude --print` must not start while Claude is not signed in, because the process it starts takes the CLI's login flow and
// blocks the user's own `claude` login. Each background caller of this package is run against the stand-in CLI (claudestub: never the real one) in each state it can
// meet, and must: come back within a few seconds, never start a print-mode run, never start the login flow, and say the CLI is not signed in. The stand-in's call
// log is what says what was started; the tests name no helper, so they hold for whatever shape the shared check takes.
const failFast = 3 * time.Second

// grossly is the one wall-clock ceiling kept in these tests (#5924): a call that took a minute is broken however busy the machine. Holds are found by what the stand-in recorded (no print run, no
// login grab) and by claudestub.NoSlower / agentsignin's differential test, not by seconds.
const grossly = 60 * time.Second

// hangBound is the ceiling when the status command does not answer (its limit, shortened to 1 s by shortHang; production 6 s, #5469): the run is then skipped as "did not answer", its own state, never as signed out.
const hangBound = 4 * time.Second

// shortHang makes the status command's limit one second for this test when the stand-in hangs on purpose (claudestub.HangingAuth): what is tested, a run skipped as "did not answer" and never
// as signed out, does not need the production 6 s (#5604; agentsignin's own test pins the 6 s).
// patientCheck gives the sign-in status command a minute (production: 6 s) for these tests: a busy machine starts the stand-in script slowly, and a check that ran past its limit is "did not answer",
// not what the test is about (#5924: in whole-module runs "says not signed in; got did not answer the sign-in check"). shortHang then shortens it for the stand-in that hangs on purpose.
func patientCheck(t *testing.T) { t.Cleanup(agentsignin.SetCheckTimeoutForTests(time.Minute)) }

func shortHang(t *testing.T, state claudestub.State) {
	t.Helper()
	if state == claudestub.HangingAuth {
		t.Cleanup(agentsignin.SetCheckTimeoutForTests(time.Second))
	}
}

// expectation is what a refused run must show in a state: how fast it comes back and what it says.
type expectation struct {
	bound time.Duration
	says  func(error) bool
	what  string
}

func expect(state claudestub.State) expectation {
	if state == claudestub.HangingAuth {
		return expectation{hangBound, func(err error) bool {
			return err != nil && strings.Contains(strings.ToLower(err.Error()), "did not answer the sign-in check")
		}, "Claude did not answer the sign-in check"}
	}
	return expectation{failFast, notSignedIn, "Claude is not signed in"}
}

// notSignedIn is the words a skipped run must carry (the operator's: "Claude is not signed in").
func notSignedIn(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "not signed in")
}

// caller is one background caller: run it against the stub at path (settings point at it too) and return what it returned.
type caller struct {
	name string
	run  func(t *testing.T, ctx context.Context, path string) (out string, err error)
	// fallback: a caller that falls back to a plain result instead of an error when it cannot ask Claude (the session summarizer); the test accepts either.
	fallback func(out string) bool
}

// workflowStep runs a workflow agent step with cfg against the stub at path, the way the runner does.
func workflowStep(cfg *AgentStepConfig) func(t *testing.T, ctx context.Context, path string) (string, error) {
	return func(t *testing.T, ctx context.Context, path string) (string, error) {
		db := setupTestDB(t)
		require.NoError(t, store.NewSessionStore(db).SetSetting(ctx, agent.CLIPathSettingKey(at.Claude), path))
		runner := NewWorkflowRunner(store.NewWorkflowStore(db), nil, nil, nil, nil, t.TempDir(), "localhost", 8420)
		err := runner.executeAgentPrint(ctx, 1, StepDef{Name: "agent-step"}, testStepFiles(t), t.TempDir(), os.Environ(), cfg, "say hi", nil, &StepResult{})
		return "", err
	}
}

func callers() []caller {
	settings := func(path string) map[string]string {
		return map[string]string{agent.CLIPathSettingKey(at.Claude): path}
	}
	return []caller{
		{name: "workflow agent step", run: workflowStep(&AgentStepConfig{AgentType: at.Claude})},
		// the same executor, its other branch: tools make it build a --settings file (agent.PrintArgs)
		{name: "workflow agent step with tools", run: workflowStep(&AgentStepConfig{AgentType: at.Claude, Tools: []string{"Read"}, Model: "haiku"})},
		{name: "goal", run: func(t *testing.T, ctx context.Context, path string) (string, error) {
			res, err := runGoalCLI(ctx, path, "user: fix the bug")
			return res.Text, err
		}},
		{name: "knowledge distill", run: func(t *testing.T, ctx context.Context, path string) (string, error) {
			return callClaudeForKnowledge(ctx, path, 10*time.Second, "system prompt", "some content")
		}},
		{name: "session summary", run: func(t *testing.T, ctx context.Context, path string) (string, error) {
			return callClaude(ctx, settings(path), "user: fix the bug")
		}, fallback: func(out string) bool { return out == fallbackSummary("user: fix the bug") }},
	}
}

// ran is one call, timed, with a ceiling well above failFast so a run that does hold (the bug: 2 s signed out, 20 s waiting behind a login) shows as slow, not as a hang.
func ran(t *testing.T, c caller, path string) (out string, err error, took time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), grossly)
	defer cancel()
	start := time.Now()
	out, err = c.run(t, ctx, path)
	return out, err, time.Since(start)
}

func TestBackgroundCallersNeverStartAClaudeRunWhileNotSignedIn(t *testing.T) {
	for _, state := range []claudestub.State{claudestub.SignedOut, claudestub.LoginInProgress, claudestub.HangingAuth} {
		for _, c := range callers() {
			t.Run(string(state)+"/"+c.name, func(t *testing.T) {
				claudestub.Isolate(t)
				patientCheck(t)
				shortHang(t, state)
				stub := claudestub.New(t, state)
				want := expect(state)
				out, err, took := ran(t, c, stub.Path())
				assert.Less(t, took, grossly, "comes back") // a hold is claudestub.NoSlower's and agentsignin's to find, against a signed-in control (#5924)
				assert.Empty(t, stub.PrintRuns(), "no print-mode run was started")
				assert.Zero(t, stub.Grabs(), "no login flow was started or held")
				if !(c.fallback != nil && err == nil && c.fallback(out)) {
					assert.True(t, want.says(err), "says %q; got %v", want.what, err)
					if state == claudestub.HangingAuth {
						assert.False(t, notSignedIn(err), "a check that did not answer is not 'signed out': %v", err)
					}
				}
			})
		}
	}
}

// The control: signed in, the same callers do ask Claude (one print-mode run each, stdin not a terminal) and are not refused for being signed out.
func TestBackgroundCallersStillAskClaudeWhenSignedIn(t *testing.T) {
	for _, c := range callers() {
		t.Run(c.name, func(t *testing.T) {
			claudestub.Isolate(t)
			patientCheck(t)
			stub := claudestub.New(t, claudestub.SignedIn)
			_, err, took := ran(t, c, stub.Path())
			// Signed in, speed is not the property (the fast-fail bound of the signed-out tests is): what is checked is that nothing HELD the call (the bug waited 15-20 s behind a login). A fresh stub script
			// can take seconds to run for the first time on a machine that scans new executables, so the bound is the hold's, not failFast (#5695: 3 of 10 alone on a loaded Mac).
			assert.Less(t, took, hangBound+5*time.Second, "a signed-in call is not held")
			// What failFast stood for here is "a background caller does not sit on the sign-in check": the event for that is the count of checks, one before the run, not a retry loop or a back-off (which the
			// wall clock could only see by being slow).
			assert.LessOrEqual(t, len(stub.AuthChecks()), 1, "one sign-in check, then the run")
			assert.False(t, notSignedIn(err), "signed in is not 'not signed in': %v", err)
			runs := stub.PrintRuns()
			require.Len(t, runs, 1, "exactly one print-mode run")
			assert.False(t, runs[0].TTY, "no terminal on stdin: nothing to prompt on")
			assert.Zero(t, stub.Grabs())
		})
	}
}

// Signing in after a skip works at once, and signing out again skips again: the verdict is not kept past its welcome.
func TestBackgroundCallersFollowTheSignInState(t *testing.T) {
	for _, c := range callers() {
		t.Run(c.name, func(t *testing.T) {
			claudestub.Isolate(t)
			patientCheck(t)
			stub := claudestub.New(t, claudestub.SignedOut)
			ran(t, c, stub.Path())
			require.Empty(t, stub.PrintRuns(), "skipped while signed out")
			stub.Set(claudestub.SignedIn)
			// a back-off may hold the next run back for a while; the check is that after it a signed-in run goes through: give the caller 3 tries 1.5 s apart
			var runs int
			for i := 0; i < 3 && runs == 0; i++ {
				ran(t, c, stub.Path())
				runs = len(stub.PrintRuns())
				if runs == 0 {
					time.Sleep(1500 * time.Millisecond) // sleep-ok: a back-off between calls is real time; there is no event for it
				}
			}
			assert.Equal(t, 1, runs, "once signed in, a run is made (a back-off of up to a few seconds is allowed, one longer is a finding)")
		})
	}
}

// No retry storm: ten calls in a row while signed out start nothing, and (reported, not asserted: a design may ask each time or remember) how many times the
// check itself ran is logged.
func TestBackgroundCallersDoNotStormWhileNotSignedIn(t *testing.T) {
	for _, c := range callers() {
		t.Run(c.name, func(t *testing.T) {
			claudestub.Isolate(t)
			patientCheck(t)
			stub := claudestub.New(t, claudestub.SignedOut)
			for i := 0; i < 10; i++ {
				_, _, took := ran(t, c, stub.Path())
				assert.Less(t, took, failFast, "call %d", i+1)
			}
			assert.Empty(t, stub.PrintRuns())
			assert.Zero(t, stub.Grabs())
			t.Logf("%s: 10 calls, %d `auth status` checks", c.name, len(stub.AuthChecks()))
			assert.LessOrEqual(t, len(stub.AuthChecks()), 10, "at most one check per call")
		})
	}
}
