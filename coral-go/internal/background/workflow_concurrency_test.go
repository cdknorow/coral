package background

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// #3424 (MED-02): at most 5 workflow runs at once; the 6th is refused with
// ConcurrencyLimitError until one finishes.
func TestWorkflowRunsAreCapped(t *testing.T) {
	db := setupTestDB(t)
	wfStore := store.NewWorkflowStore(db)
	rt := newWfMockRuntime()
	runner := NewWorkflowRunner(wfStore, &AgentLauncher{runtime: rt}, rt, nil, nil, t.TempDir(), "localhost", 8420)
	gate := filepath.Join(t.TempDir(), "go")
	// Quoted, with forward slashes: on Windows the step runs in Git's sh, where an unquoted
	// C:\Users\... loses its backslashes and the loop never ends (#3631).
	steps, _ := json.Marshal([]StepDef{{Name: "wait", Type: "shell", Command: "while [ ! -e '" + filepath.ToSlash(gate) + "' ]; do sleep 0.05; done"}})
	wf := createTestWorkflow(t, wfStore, t.TempDir(), string(steps))
	// Every step this test starts ends with it, whatever happens (#3997: a failed run left the
	// loops polling forever, because the gate's folder was removed before they saw the gate).
	// Registered after the temp folders, so it runs before they are removed.
	var stepPIDs []int
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		runner.Shutdown(ctx)
		stepGroupsGone(t, stepPIDs)
	})
	ctx := context.Background()
	var runs []*store.WorkflowRun
	for i := 0; i < maxConcurrentWorkflowRuns; i++ {
		run, err := runner.TriggerWorkflow(ctx, wf, "api", nil)
		require.NoError(t, err, i)
		runs = append(runs, run)
	}
	require.Eventually(t, func() bool {
		runner.mu.Lock()
		defer runner.mu.Unlock()
		stepPIDs = stepPIDs[:0]
		for _, c := range runner.activeChildren {
			if c.cmd != nil && c.cmd.Process != nil {
				stepPIDs = append(stepPIDs, c.cmd.Process.Pid)
			}
		}
		return len(stepPIDs) == maxConcurrentWorkflowRuns
	}, 10*time.Second, 10*time.Millisecond, "every run's step started")
	_, err := runner.TriggerWorkflow(ctx, wf, "api", nil)
	var limit *ConcurrencyLimitError
	require.True(t, errors.As(err, &limit), "the 6th run: %v", err)
	assert.Equal(t, maxConcurrentWorkflowRuns, limit.Limit)

	require.NoError(t, os.WriteFile(gate, nil, 0o644))
	require.Eventually(t, func() bool {
		for _, r := range runs {
			got, _ := wfStore.GetWorkflowRunDirect(ctx, r.ID)
			if got == nil || (got.Status != "completed" && got.Status != "failed") {
				return false
			}
		}
		return true
	}, 20*time.Second, 50*time.Millisecond)
	var last *store.WorkflowRun
	require.Eventually(t, func() bool {
		last, err = runner.TriggerWorkflow(ctx, wf, "api", nil)
		return err == nil
	}, 5*time.Second, 50*time.Millisecond, "a slot frees when a run finishes")
	// Wait for that run too: on Windows its shell still holding a file makes the TempDir cleanup fail.
	require.Eventually(t, func() bool {
		got, _ := wfStore.GetWorkflowRunDirect(ctx, last.ID)
		return got != nil && (got.Status == "completed" || got.Status == "failed")
	}, 20*time.Second, 50*time.Millisecond)
}
