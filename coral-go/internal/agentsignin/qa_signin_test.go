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

// A status command that hangs is killed when the check gives up: no process is left behind holding anything.
func TestAHungStatusCommandIsKilledWhenTheCheckGivesUp(t *testing.T) {
	claudestub.Isolate(t)
	stub := claudestub.New(t, claudestub.HangingAuth)
	require.True(t, IsNotSignedIn(Require(context.Background(), "claude", stub.Path())))
	checks := stub.AuthChecks()
	require.NotEmpty(t, checks)
	pid := checks[0].PID
	gone := false
	for i := 0; i < 40 && !gone; i++ { // up to 4 s: the kill, then the wait for its pipes
		gone = syscall.Kill(pid, 0) != nil
		if !gone {
			time.Sleep(100 * time.Millisecond) // sleep-ok: polling for a process to be gone
		}
	}
	assert.True(t, gone, "the hung status process %d is still running", pid)
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
