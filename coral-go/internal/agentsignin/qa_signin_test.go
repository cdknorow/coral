//go:build !windows

// These checks use signals and process groups (syscall.Kill): the Unix platforms only.

package agentsignin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/claudestub"
)

// QA's checks of the shared sign-in check against the claude stand-in (#5469 review), the ones the package's own tests do not make: many callers at once, a hung status
// command left running, and what the status process is given.

// Twenty background callers arrive together while the status command hangs: they share one check (not twenty processes), all are refused, and none waits longer than the check's deadline.
func TestManyCallersAtOnceShareOneCheck(t *testing.T) {
	claudestub.Isolate(t)
	stub := claudestub.New(t, claudestub.HangingAuth)
	var wg sync.WaitGroup
	errs := make([]error, 20)
	start := time.Now()
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = Require(context.Background(), "claude", stub.Path())
		}()
	}
	wg.Wait()
	assert.Less(t, time.Since(start), checkTimeout+2*time.Second, "all 20 are answered within the check's deadline and a bit") // the first deadline is 6 s (#5469), not the 5 s it was
	for i, err := range errs {
		assert.True(t, IsNotSignedIn(err), "caller %d: %v", i, err)
	}
	assert.LessOrEqual(t, len(stub.AuthChecks()), 2, "twenty callers made %d status processes", len(stub.AuthChecks()))
	assert.Empty(t, stub.PrintRuns())
	assert.Zero(t, stub.Grabs())
}

// waitGone waits for pid to be gone (reaped: a zombie still answers signal 0).
func waitGone(t *testing.T, pid int, within time.Duration) {
	t.Helper()
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) != nil }, within, 50*time.Millisecond, "the hung status process %d is still running", pid)
}

// A status command that hangs is killed when the caller gives up. The stand-in is waited for (it has logged its status call) before the caller gives up, not slept for: the earlier form let
// the check's own six-second limit run from before /bin/sh had started, so a machine that took that long to start the stand-in (a full go test ./... under load) gave up on a process that did not
// exist yet, and the test failed with no check logged (#6212). The kill is the same one the limit makes: the command runs under the check's context.
func TestAHungStatusCommandIsKilledWhenTheCallerGivesUp(t *testing.T) {
	claudestub.Isolate(t)
	stub := claudestub.New(t, claudestub.HangingAuth)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := make(chan error, 1)
	go func() { res <- Require(ctx, "claude", stub.Path()) }()
	require.Eventually(t, func() bool { return len(stub.AuthChecks()) > 0 }, time.Minute, 25*time.Millisecond, "the stand-in never ran its status command")
	pid := stub.AuthChecks()[0].PID
	cancel()
	// The stand-in holds for 20 s: a check that returns long before that was ended by the kill, not by the stand-in finishing (which would leave the process gone too).
	select {
	case <-res:
	case <-time.After(15 * time.Second):
		t.Fatal("the check did not return soon after its caller gave up: the hung command was not killed")
	}
	waitGone(t, pid, 10*time.Second) // the kill, then the wait for its pipes
}

// The limit ends a hung status command too, and the answer is the "did not answer" one that skips jobs. Whether the stand-in had started before a short limit passed depends on the machine, so
// the process is checked only when it did (the caller-gives-up test above waits for it and always checks); the verdict is checked either way.
func TestAHungStatusCommandIsKilledWhenTheLimitPasses(t *testing.T) {
	claudestub.Isolate(t)
	defer SetCheckTimeoutForTests(3 * time.Second)()
	stub := claudestub.New(t, claudestub.HangingAuth)
	start := time.Now()
	require.True(t, IsNotSignedIn(Require(context.Background(), "claude", stub.Path())), "a check that gave up is an answer that skips jobs")
	assert.Less(t, time.Since(start), 15*time.Second, "the stand-in holds for 20 s: a check that returned before that was ended by the kill")
	if checks := stub.AuthChecks(); len(checks) > 0 {
		waitGone(t, checks[0].PID, 10*time.Second)
	} else {
		t.Log("the stand-in had not started within the limit on this machine: the kill is checked by the caller-gives-up test")
	}
}

// What the status command is given: nothing on stdin and no terminal, the temp folder as its working directory, and no Coral session token in its environment (the check must never carry a credential to a CLI it is only asking a question).
func TestTheStatusProcessGetsNoStdinNoCoralSecretsAndTheTempFolder(t *testing.T) {
	claudestub.Isolate(t)
	dir := t.TempDir()
	report := filepath.Join(dir, "report")
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n{ pwd; env | sed 's/=.*//' | sort | tr '\\n' ','; echo; if [ -t 0 ]; then echo TTY; else echo NOTTY; fi; cat | wc -c; } > '" + report + "'\necho '{\"loggedIn\":true}'\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	t.Setenv("CORAL_SESSION_TOKEN", "sentinel-token-value")
	assert.Equal(t, SignedIn, Check(context.Background(), "claude", bin))
	b, err := os.ReadFile(report)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	require.GreaterOrEqual(t, len(lines), 4, string(b))
	cwd, _ := filepath.EvalSymlinks(lines[0])
	tmp, _ := filepath.EvalSymlinks(os.TempDir())
	assert.Equal(t, tmp, cwd, "runs from the temp folder")
	// the token never reaches it; the session NAME and TMUX still do (agentenv.Filter drops secrets, not Coral's session variables, unlike runGoalCLI): harmless for a status
	// command, and not asserted either way
	assert.NotContains(t, lines[1], "CORAL_SESSION_TOKEN,", "a Coral session token reached the status process")
	assert.Equal(t, "NOTTY", lines[2], "no terminal")
	assert.Equal(t, "0", strings.TrimSpace(lines[3]), "nothing on stdin")
}

// #5604: other packages' tests of a hanging status command shorten the limit through SetCheckTimeoutForTests (an atomic: checks run in background goroutines, -race), so what ships must still
// be 6 s, and the override must come off again. This is what keeps those tests from hiding a change of the production limit.
func TestTheStatusCommandsLimitIsSixSecondsUnlessATestShortensIt(t *testing.T) {
	assert.Equal(t, 6*time.Second, checkTimeout, "the shipped limit")
	assert.Equal(t, 6*time.Second, effectiveCheckTimeout())
	restore := SetCheckTimeoutForTests(time.Second)
	assert.Equal(t, time.Second, effectiveCheckTimeout(), "the override is used")
	inner := SetCheckTimeoutForTests(2 * time.Second)
	assert.Equal(t, 2*time.Second, effectiveCheckTimeout())
	inner()
	assert.Equal(t, time.Second, effectiveCheckTimeout(), "an inner restore puts back the outer override")
	restore()
	assert.Equal(t, 6*time.Second, effectiveCheckTimeout(), "and the last puts back the shipped limit")
}
