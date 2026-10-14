package background

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// Starting a run keeps a workflow's newest WorkflowRunsKept runs and removes
// older finished ones with their directories; a run still in progress is kept
// however old it is (audit MED-04).
func TestTriggerKeepsTheNewestRunsAndTheirDirectories(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	ws := store.NewWorkflowStore(setupTestDB(t))
	wr := NewWorkflowRunner(ws, nil, nil, nil, nil, dataDir, "localhost", 8420)
	wf, err := ws.CreateWorkflow(ctx, &store.Workflow{Name: "keep", StepsJSON: `[{"name":"one","type":"shell","command":"true"}]`,
		DefaultAgentJSON: "{}", BaseBranch: "main", MaxDurationS: 60, Enabled: 1})
	require.NoError(t, err)
	other, err := ws.CreateWorkflow(ctx, &store.Workflow{Name: "other", StepsJSON: `[]`, DefaultAgentJSON: "{}", BaseBranch: "main", MaxDurationS: 60, Enabled: 1})
	require.NoError(t, err)

	runDir := func(id int64) string { return filepath.Join(dataDir, "workflows", "runs", strconv.FormatInt(id, 10)) }
	newRun := func(wfID int64, status string) int64 {
		run, err := ws.CreateWorkflowRun(ctx, wfID, "api", nil)
		require.NoError(t, err)
		require.NoError(t, ws.SetRunStatus(ctx, run.ID, status, nil))
		require.NoError(t, os.MkdirAll(runDir(run.ID), 0o755))
		return run.ID
	}
	stillRunning := newRun(wf.ID, "running") // the oldest run, but in progress
	var finished []int64
	for i := 0; i < WorkflowRunsKept+5; i++ {
		finished = append(finished, newRun(wf.ID, "completed"))
	}
	otherRun := newRun(other.ID, "completed")

	run, err := wr.TriggerWorkflow(ctx, wf, "api", nil)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !wr.IsRunActive(run.ID) }, 10*time.Second, 20*time.Millisecond)

	ids, err := ws.WorkflowRunIDs(ctx, wf.ID)
	require.NoError(t, err)
	assert.Contains(t, ids, stillRunning, "a run in progress is never pruned")
	assert.Contains(t, ids, run.ID)
	assert.Len(t, ids, WorkflowRunsKept+1, "the newest %d plus the one in progress", WorkflowRunsKept)

	// The newest run and the 49 before it are kept, so the oldest 6 finished go.
	pruned := finished[:6]
	for _, id := range pruned {
		assert.NotContains(t, ids, id)
		assert.NoDirExists(t, runDir(id), "a pruned run's directory is removed")
	}
	for _, id := range finished[6:] {
		assert.DirExists(t, runDir(id))
	}
	assert.DirExists(t, runDir(stillRunning))
	assert.DirExists(t, runDir(otherRun), "another workflow's runs are untouched")
}
