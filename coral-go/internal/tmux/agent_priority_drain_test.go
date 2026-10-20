//go:build !windows

package tmux

import (
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// goroutinesIn counts the live goroutines whose stack mentions fn.
func goroutinesIn(fn string) int {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	n := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, fn) {
			n++
		}
	}
	return n
}

func startSleeper(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd.Process.Pid
}

// #5217: the second look at a pane's priority is a timer that runs a ps. A test that ended just before it fired (or while its ps ran) ended with a live goroutine, which the package's leak
// check reported on some -shuffle runs. drainAgentPriority stops what has not fired and waits for what is running, so nothing of it is left afterwards.
func TestDrainAgentPriorityLeavesNoRecheckRunning(t *testing.T) {
	was := agentPriorityRecheck
	t.Cleanup(func() { agentPriorityRecheck = was })

	// Fired and running: a recheck whose time is now, drained while it runs or has run.
	agentPriorityRecheck = time.Millisecond
	for i := 0; i < 20; i++ {
		applyAgentPriority(strconv.Itoa(startSleeper(t)))
	}
	drainAgentPriority()
	assert.Zero(t, goroutinesIn("agentprio.ApplyTree"), "no ps of a recheck is still running after the drain")
	assert.Zero(t, goroutinesIn("scheduleAgentPriorityRecheck"), "and no recheck goroutine")

	// Not fired yet: a recheck an hour away is stopped by the drain, which returns at once.
	agentPriorityRecheck = time.Hour
	applyAgentPriority(strconv.Itoa(startSleeper(t)))
	done := make(chan struct{})
	go func() { drainAgentPriority(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the drain waited for a recheck that was never going to run")
	}
	priorityMu.Lock()
	left := len(priorityPending)
	priorityMu.Unlock()
	assert.Zero(t, left, "no pending timer is kept")
}
