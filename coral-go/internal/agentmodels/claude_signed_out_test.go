package agentmodels

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func TestModelListNeverStartsAClaudeRunWhileNotSignedIn(t *testing.T) {
	for _, state := range []claudestub.State{claudestub.SignedOut, claudestub.LoginInProgress, claudestub.HangingAuth} {
		t.Run(string(state), func(t *testing.T) {
			claudestub.Isolate(t)
			stub := claudestub.New(t, state)
			res, took := fetched(t, stub.Path())
			bound := 3 * time.Second
			if state == claudestub.HangingAuth {
				bound = 8 * time.Second // the status command's first ask waits 6 s (#5469)
			}
			assert.Less(t, took, bound, "the background fetch ends fast")
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
	stub := claudestub.New(t, claudestub.SignedIn)
	res, _ := fetched(t, stub.Path())
	assert.False(t, strings.Contains(strings.ToLower(res.Error), "not signed in"), "signed in: %q", res.Error) // the stub's plain answer lists no models: that error is fine
	runs := stub.PrintRuns()
	require.Len(t, runs, 1, "one print-mode run")
	assert.False(t, runs[0].TTY)
	assert.Zero(t, stub.Grabs())
}
