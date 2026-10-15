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
	// C:\Users\... loses its backslashes and the loop never ends (#3631). Opened on cleanup too,
	// so a failed assertion never leaves the loops running.
	t.Cleanup(func() { _ = os.WriteFile(gate, nil, 0o644) })
	steps, _ := json.Marshal([]StepDef{{Name: "wait", Type: "shell", Command: "while [ ! -e '" + filepath.ToSlash(gate) + "' ]; do sleep 0.05; done"}})
	wf := createTestWorkflow(t, wfStore, t.TempDir(), string(steps))
	ctx := context.Background()
	var runs []*store.WorkflowRun
	for i := 0; i < maxConcurrentWorkflowRuns; i++ {
		run, err := runner.TriggerWorkflow(ctx, wf, "api", nil)
		require.NoError(t, err, i)
		runs = append(runs, run)
	}
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
	require.Eventually(t, func() bool {
		_, err := runner.TriggerWorkflow(ctx, wf, "api", nil)
		return err == nil
	}, 5*time.Second, 50*time.Millisecond, "a slot frees when a run finishes")
}
