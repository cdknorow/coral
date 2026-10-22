package agentmodels

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agentsignin"
	"github.com/subgentic/coral/internal/claudestub"
)

// #5469 / #5472: the model list is read by running the claude CLI in print mode (an initialize request, no prompt), once a day and whenever the dashboard asks. While
// Claude is not signed in that run takes the CLI's login flow and blocks the user's own login, so the list must not start it: Get answers at once (it never runs a CLI on
// the caller's goroutine), the background fetch ends within a few seconds, no print-mode run or login flow was started (the stand-in's call log), and the result says Claude
// is not signed in.
func fetched(t *testing.T, path string) (Result, time.Duration) {
	t.Helper()
	svc := NewService(func(string) (string, error) { return path, nil })
	start := time.Now()
	svc.Get("claude")
	done := make(chan struct{})
	go func() { svc.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(12 * time.Second): // a run that holds the login (the bug) ends later than this; the test says so and moves on
	}
	took := time.Since(start)
	return svc.Get("claude"), took
}

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

// grossly is the one wall-clock ceiling kept in these tests (#5924): a call that took a minute is broken however busy the machine. Holds are found by what the stand-in recorded (no print run,
// no login grab) and by claudestub.NoSlower, not by seconds.
const grossly = 60 * time.Second

func TestModelListNeverStartsAClaudeRunWhileNotSignedIn(t *testing.T) {
	for _, state := range []claudestub.State{claudestub.SignedOut, claudestub.LoginInProgress, claudestub.HangingAuth} {
		t.Run(string(state), func(t *testing.T) {
			claudestub.Isolate(t)
			patientCheck(t)
			shortHang(t, state)
			stub := claudestub.New(t, state)
			res, took := fetched(t, stub.Path())
			// Not a measure of a hold: a hold is claudestub.NoSlower's and agentsignin's (against a signed-in control, #5924); what is asserted here is that nothing took so long it is a gross
			// regression (the status command's limit is shortened to 1 s for the hanging one; production 6 s, #5469), on a machine that may be thrashing under a whole-module run.
			assert.Less(t, took, grossly, "the background fetch ends")
			assert.Empty(t, stub.PrintRuns(), "no print-mode run was started")
			assert.Zero(t, stub.Grabs(), "no login flow was started or held")
			lower := strings.ToLower(res.Error)
			if state == claudestub.HangingAuth {
				assert.Contains(t, lower, "did not answer the sign-in check", "a status command that hung is not 'signed out': %q", res.Error)
				assert.NotContains(t, lower, "not signed in")
			} else {
				assert.Contains(t, lower, "not signed in", "says Claude is not signed in; got %q", res.Error)
			}
		})
	}
}

func TestModelListStillAsksClaudeWhenSignedIn(t *testing.T) {
	claudestub.Isolate(t)
	patientCheck(t)
	stub := claudestub.New(t, claudestub.SignedIn)
	res, took := fetched(t, stub.Path())
	assert.False(t, strings.Contains(strings.ToLower(res.Error), "not signed in"), "signed in: %q", res.Error)
	assert.Empty(t, res.Error, "signed in: the stub answers the initialize request, so the list is read")
	assert.NotEmpty(t, res.Models, "the stub's one model is listed")
	assert.Less(t, took, 5*time.Second, "the fetch ends when the reply is read, not when the test gives up (#5591: it used to wait out 12 s)")
	runs := stub.PrintRuns()
	require.Len(t, runs, 1, "one print-mode run")
	assert.False(t, runs[0].TTY)
	assert.Zero(t, stub.Grabs())
}
