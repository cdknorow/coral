//go:build !windows

package background

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// alive reports whether pid still exists (kill 0 succeeds).
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// readPID waits for a pid written to path by a test command.
func readPID(t *testing.T, path string) int {
	t.Helper()
	var pid int
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil && pid > 0
	}, 5*time.Second, 10*time.Millisecond, "the command never wrote its pid")
	// Only this test's recorded process, if anything goes wrong.
	t.Cleanup(func() {
		if alive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return pid
}

// A shell step that times out takes its whole process group with it: a
// backgrounded grandchild doesn't survive (audit HIGH-03). Before, only the
// step's /bin/sh was signalled and `sleep 600` kept running.
func TestShellStepTimeoutKillsTheProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	wr := NewWorkflowRunner(store.NewWorkflowStore(setupTestDB(t)), nil, nil, nil, nil, dir, "localhost", 8420)
	step := StepDef{Name: "hang", Type: "shell", TimeoutS: 1,
		Command: "sleep 600 & echo $! > '" + pidFile + "'; wait"}

	var res StepResult
	start := time.Now()
	err := wr.executeShellStep(context.Background(), 1, step, testStepFiles(t), dir, nil, &res)
	require.Error(t, err, "a timed-out step fails")
	assert.Less(t, time.Since(start), 30*time.Second, "the step returned at its timeout")

	pid := readPID(t, pidFile)
	assert.Eventually(t, func() bool { return !alive(pid) }, 10*time.Second, 50*time.Millisecond,
		"the backgrounded sleep (pid %d) survived the step's timeout", pid)
}

// Hook commands run in the directory they're given, in their own process
// group (audit LOW-01/02).
func TestHooksRunInTheirDirectoryAndProcessGroup(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(t.TempDir(), "hook.out")
	wr := NewWorkflowRunner(store.NewWorkflowStore(setupTestDB(t)), nil, nil, nil, nil, t.TempDir(), "localhost", 8420)
	hooks := map[string]interface{}{"StepComplete": []interface{}{map[string]interface{}{
		"hooks": []interface{}{map[string]interface{}{"type": "command",
			"command": "{ pwd -P; ps -o pgid= -p $$; echo $$; } > '" + out + "'"}},
	}}}
	wr.fireHooks(context.Background(), hooks, "StepComplete", dir, nil)

	b, err := os.ReadFile(out)
	require.NoError(t, err)
	lines := strings.Fields(string(b))
	require.Len(t, lines, 3, "pwd, pgid and pid: %q", b)
	want, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	assert.Equal(t, want, lines[0], "the hook ran in its directory")
	assert.Equal(t, lines[2], lines[1], "the hook's shell leads its own process group")
}

// groupGone reports whether no process is left in process group pgid.
func groupGone(pgid int) bool { return syscall.Kill(-pgid, 0) != nil }

// stepGroupsGone is what a workflow test asserts when it ends: every step process group it
// started is gone (#3997).
func stepGroupsGone(t *testing.T, pgids []int) {
	t.Helper()
	for _, g := range pgids {
		assert.Eventually(t, func() bool { return groupGone(g) }, 10*time.Second, 20*time.Millisecond,
			"the step's process group %d is still running", g)
	}
}

// startHangingRun starts a run whose shell step backgrounds a sleep and waits; it returns the
// run, the step's process group and the sleep's pid.
func startHangingRun(t *testing.T) (*WorkflowRunner, *store.WorkflowStore, *store.WorkflowRun, int, int) {
	t.Helper()
	db := setupTestDB(t)
	wfStore := store.NewWorkflowStore(db)
	rt := newWfMockRuntime()
	runner := NewWorkflowRunner(wfStore, &AgentLauncher{runtime: rt}, rt, nil, nil, t.TempDir(), "localhost", 8420)
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	steps := `[{"name":"hang","type":"shell","command":"sleep 600 & echo $! > '` + pidFile + `'; wait"}]`
	wf := createTestWorkflow(t, wfStore, t.TempDir(), steps)
	run, err := runner.TriggerWorkflow(context.Background(), wf, "api", nil)
	require.NoError(t, err)
	t.Cleanup(func() { runner.Shutdown(context.Background()) })
	grandchild := readPID(t, pidFile)
	var pgid int
	require.Eventually(t, func() bool {
		runner.mu.Lock()
		defer runner.mu.Unlock()
		if c := runner.activeChildren[run.ID]; c != nil && c.cmd != nil && c.cmd.Process != nil {
			pgid = c.cmd.Process.Pid
		}
		return pgid > 0
	}, 5*time.Second, 10*time.Millisecond)
	// A safety net if the test fails: only this step's recorded group, never by name.
	t.Cleanup(func() {
		if !groupGone(pgid) {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
	})
	return runner, wfStore, run, pgid, grandchild
}

// #3997: killing a run takes the step's whole process group, a backgrounded grandchild too.
func TestKillRunKillsTheStepsProcessGroup(t *testing.T) {
	runner, _, run, pgid, grandchild := startHangingRun(t)
	require.True(t, runner.KillRun(context.Background(), run.ID))
	assert.Eventually(t, func() bool { return !alive(grandchild) }, 10*time.Second, 20*time.Millisecond, "the backgrounded sleep survived KillRun")
	stepGroupsGone(t, []int{pgid})
}

// #3997: Coral stopping ends every run in progress with its process group (before, a step in its
// own process group outlived the server), records them killed, and starts no new run.
func TestShutdownKillsRunningSteps(t *testing.T) {
	runner, wfStore, run, pgid, grandchild := startHangingRun(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	runner.Shutdown(ctx)
	assert.Less(t, time.Since(start), 15*time.Second, "Shutdown returned once the run ended")
	// Killed with its group, the sleep can still answer kill(pid, 0) for a moment: once its shell is gone it is
	// reparented and reaped by init, after Shutdown has returned (#4827: an immediate check failed now and then).
	// It must be gone soon after, as in the KillRun test above.
	assert.Eventually(t, func() bool { return !alive(grandchild) }, 10*time.Second, 20*time.Millisecond, "the backgrounded sleep survived Shutdown")
	stepGroupsGone(t, []int{pgid})
	got, err := wfStore.GetWorkflowRunDirect(context.Background(), run.ID)
	require.NoError(t, err)
	assert.Equal(t, "killed", got.Status)
	wf, err := wfStore.GetWorkflow(context.Background(), got.WorkflowID)
	require.NoError(t, err)
	late, err := runner.TriggerWorkflow(context.Background(), wf, "api", nil)
	assert.ErrorIs(t, err, ErrRunnerStopped)
	if late != nil { // a run that should not have started ends here
		runner.KillRun(context.Background(), late.ID)
	}
}
