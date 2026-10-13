package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowStore_CreateAndGet(t *testing.T) {
	db := openTestDB(t)
	ws := NewWorkflowStore(db)
	ctx := context.Background()

	wf := &Workflow{
		Name:        "test-workflow",
		Description: "A test workflow",
		StepsJSON:   `[{"name":"step1","type":"shell","command":"echo hello"}]`,
		RepoPath:    "/tmp",
		Enabled:     1,
	}
	created, err := ws.CreateWorkflow(ctx, wf)
	require.NoError(t, err)
	assert.Greater(t, created.ID, int64(0))
	assert.Equal(t, "test-workflow", created.Name)
	assert.NotEmpty(t, created.CreatedAt)
	assert.Equal(t, 1, created.StepCount)

	got, err := ws.GetWorkflow(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, created.ID, got.ID)
	assert.Equal(t, "test-workflow", got.Name)
	assert.Equal(t, "A test workflow", got.Description)
	assert.Equal(t, 1, got.StepCount)
	assert.Len(t, got.Steps, 1)
}

func TestWorkflowStore_GetByName(t *testing.T) {
	db := openTestDB(t)
	ws := NewWorkflowStore(db)
	ctx := context.Background()

	wf := &Workflow{
		Name:      "by-name-test",
		StepsJSON: `[{"name":"s","type":"shell","command":"true"}]`,
		Enabled:   1,
	}
	_, err := ws.CreateWorkflow(ctx, wf)
	require.NoError(t, err)

	got, err := ws.GetWorkflowByName(ctx, "by-name-test")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "by-name-test", got.Name)

	missing, err := ws.GetWorkflowByName(ctx, "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, missing)
}

func TestWorkflowStore_GetNotFound(t *testing.T) {
	db := openTestDB(t)
	ws := NewWorkflowStore(db)
	ctx := context.Background()

	got, err := ws.GetWorkflow(ctx, 99999)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestWorkflowStore_UniqueNameConstraint(t *testing.T) {
	db := openTestDB(t)
	ws := NewWorkflowStore(db)
	ctx := context.Background()

	wf1 := &Workflow{Name: "duplicate", StepsJSON: `[]`, Enabled: 1}
	_, err := ws.CreateWorkflow(ctx, wf1)
	require.NoError(t, err)

	wf2 := &Workflow{Name: "duplicate", StepsJSON: `[]`, Enabled: 1}
	_, err = ws.CreateWorkflow(ctx, wf2)
	assert.Error(t, err, "should fail on duplicate name")
}

func TestWorkflowStore_ListWorkflows(t *testing.T) {
	db := openTestDB(t)
	ws := NewWorkflowStore(db)
	ctx := context.Background()

	// Empty list
	workflows, err := ws.ListWorkflows(ctx)
	require.NoError(t, err)
	assert.Empty(t, workflows)

	// Create two workflows
	_, err = ws.CreateWorkflow(ctx, &Workflow{Name: "beta", StepsJSON: `[{"name":"s","type":"shell","command":"true"}]`, Enabled: 1})
	require.NoError(t, err)
	_, err = ws.CreateWorkflow(ctx, &Workflow{Name: "alpha", StepsJSON: `[{"name":"s","type":"shell","command":"true"}]`, Enabled: 1})
	require.NoError(t, err)

	workflows, err = ws.ListWorkflows(ctx)
	require.NoError(t, err)
	assert.Len(t, workflows, 2)
	assert.Equal(t, "alpha", workflows[0].Name, "should be sorted by name")
	assert.Equal(t, "beta", workflows[1].Name)
}

func TestWorkflowStore_ListWorkflows_WithLastRun(t *testing.T) {
	db := openTestDB(t)
	ws := NewWorkflowStore(db)
	ctx := context.Background()

	wf, err := ws.CreateWorkflow(ctx, &Workflow{Name: "with-runs", StepsJSON: `[]`, Enabled: 1})
	require.NoError(t, err)

	// No runs yet
	workflows, err := ws.ListWorkflows(ctx)
	require.NoError(t, err)
	require.Len(t, workflows, 1)
	assert.Nil(t, workflows[0].LastRun)

	// Create a run
	run, err := ws.CreateWorkflowRun(ctx, wf.ID, "api", nil)
	require.NoError(t, err)
	err = ws.SetRunStatus(ctx, run.ID, "completed", nil)
	require.NoError(t, err)

	// Now list should include last_run
	workflows, err = ws.ListWorkflows(ctx)
	require.NoError(t, err)
	require.Len(t, workflows, 1)
	require.NotNil(t, workflows[0].LastRun)
	assert.Equal(t, "completed", workflows[0].LastRun.Status)
}

func TestWorkflowStore_UpdateWorkflow(t *testing.T) {
	db := openTestDB(t)
	ws := NewWorkflowStore(db)
	ctx := context.Background()

	wf, err := ws.CreateWorkflow(ctx, &Workflow{
		Name: "to-update", StepsJSON: `[]`, Description: "original", Enabled: 1,
	})
	require.NoError(t, err)

	updated, err := ws.UpdateWorkflow(ctx, wf.ID, map[string]interface{}{
		"description": "updated desc",
		"enabled":     false,
	})
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, "updated desc", updated.Description)
	assert.Equal(t, 0, updated.Enabled)
}

func TestWorkflowStore_DeleteWorkflow(t *testing.T) {
	db := openTestDB(t)
	ws := NewWorkflowStore(db)
	ctx := context.Background()

	wf, err := ws.CreateWorkflow(ctx, &Workflow{Name: "to-delete", StepsJSON: `[]`, Enabled: 1})
	require.NoError(t, err)

	err = ws.DeleteWorkflow(ctx, wf.ID)
	require.NoError(t, err)

	got, err := ws.GetWorkflow(ctx, wf.ID)
	require.NoError(t, err)
	assert.Nil(t, got, "should be deleted")
}

func TestWorkflowStore_DeleteCascadesRuns(t *testing.T) {
	db := openTestDB(t)
	ws := NewWorkflowStore(db)
	ctx := context.Background()

	wf, err := ws.CreateWorkflow(ctx, &Workflow{Name: "cascade-test", StepsJSON: `[]`, Enabled: 1})
	require.NoError(t, err)

	run, err := ws.CreateWorkflowRun(ctx, wf.ID, "api", nil)
	require.NoError(t, err)

	err = ws.DeleteWorkflow(ctx, wf.ID)
	require.NoError(t, err)

	// Run should be gone too (FK cascade)
	got, err := ws.GetWorkflowRunDirect(ctx, run.ID)
	require.NoError(t, err)
	assert.Nil(t, got)
}

// ── Workflow Runs ─────────────────────────────────────────────────────

func TestWorkflowStore_CreateAndGetRun(t *testing.T) {
	db := openTestDB(t)
	ws := NewWorkflowStore(db)
	ctx := context.Background()

	wf, err := ws.CreateWorkflow(ctx, &Workflow{Name: "run-test", StepsJSON: `[]`, Enabled: 1})
	require.NoError(t, err)

	triggerCtx := `{"source":"test"}`
	run, err := ws.CreateWorkflowRun(ctx, wf.ID, "api", &triggerCtx)
	require.NoError(t, err)
	assert.Greater(t, run.ID, int64(0))
	assert.Equal(t, "pending", run.Status)
	assert.Equal(t, "api", run.TriggerType)

	got, err := ws.GetWorkflowRun(ctx, run.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "run-test", *got.WorkflowName)
	assert.Equal(t, "pending", got.Status)
}

func TestWorkflowStore_SetRunStatus(t *testing.T) {
	db := openTestDB(t)
	ws := NewWorkflowStore(db)
	ctx := context.Background()

	wf, err := ws.CreateWorkflow(ctx, &Workflow{Name: "status-test", StepsJSON: `[]`, Enabled: 1})
	require.NoError(t, err)

	run, err := ws.CreateWorkflowRun(ctx, wf.ID, "api", nil)
	require.NoError(t, err)

	// Set to running — should set started_at
	err = ws.SetRunStatus(ctx, run.ID, "running", nil)
	require.NoError(t, err)

	got, err := ws.GetWorkflowRunDirect(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, "running", got.Status)
	assert.NotNil(t, got.StartedAt)
	startedAt := *got.StartedAt

	// Set to running again — started_at should NOT change (COALESCE)
	err = ws.SetRunStatus(ctx, run.ID, "running", nil)
	require.NoError(t, err)

	got, err = ws.GetWorkflowRunDirect(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, startedAt, *got.StartedAt, "started_at should not change on repeated running")

	// Set to completed — should set finished_at
	err = ws.SetRunStatus(ctx, run.ID, "completed", nil)
	require.NoError(t, err)

	got, err = ws.GetWorkflowRunDirect(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, "completed", got.Status)
	assert.NotNil(t, got.FinishedAt)
}

func TestWorkflowStore_SetRunStatus_WithError(t *testing.T) {
	db := openTestDB(t)
	ws := NewWorkflowStore(db)
	ctx := context.Background()

	wf, err := ws.CreateWorkflow(ctx, &Workflow{Name: "error-test", StepsJSON: `[]`, Enabled: 1})
	require.NoError(t, err)

	run, err := ws.CreateWorkflowRun(ctx, wf.ID, "api", nil)
	require.NoError(t, err)

	errMsg := "step 2 failed"
	err = ws.SetRunStatus(ctx, run.ID, "failed", &errMsg)
	require.NoError(t, err)

	got, err := ws.GetWorkflowRunDirect(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, "failed", got.Status)
	require.NotNil(t, got.ErrorMsg)
	assert.Equal(t, "step 2 failed", *got.ErrorMsg)
}

func TestWorkflowStore_UpdateStepResults(t *testing.T) {
	db := openTestDB(t)
	ws := NewWorkflowStore(db)
	ctx := context.Background()

	wf, err := ws.CreateWorkflow(ctx, &Workflow{Name: "steps-test", StepsJSON: `[]`, Enabled: 1})
	require.NoError(t, err)

	run, err := ws.CreateWorkflowRun(ctx, wf.ID, "api", nil)
	require.NoError(t, err)

	results := `[{"index":0,"name":"test","status":"completed"}]`
	err = ws.UpdateStepResults(ctx, run.ID, 1, results)
	require.NoError(t, err)

	got, err := ws.GetWorkflowRunDirect(ctx, run.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, got.CurrentStep)
	require.Len(t, got.Steps, 1)
}

func TestWorkflowStore_ListRunsForWorkflow(t *testing.T) {
	db := openTestDB(t)
	ws := NewWorkflowStore(db)
	ctx := context.Background()

	wf, err := ws.CreateWorkflow(ctx, &Workflow{Name: "list-runs", StepsJSON: `[]`, Enabled: 1})
	require.NoError(t, err)

	// Create 3 runs
	for i := 0; i < 3; i++ {
		_, err = ws.CreateWorkflowRun(ctx, wf.ID, "api", nil)
		require.NoError(t, err)
	}

	runs, err := ws.ListRunsForWorkflow(ctx, wf.ID, 10, 0, nil)
	require.NoError(t, err)
	assert.Len(t, runs, 3)

	// With limit
	runs, err = ws.ListRunsForWorkflow(ctx, wf.ID, 2, 0, nil)
	require.NoError(t, err)
	assert.Len(t, runs, 2)

	// With status filter
	status := "completed"
	runs, err = ws.ListRunsForWorkflow(ctx, wf.ID, 10, 0, &status)
	require.NoError(t, err)
	assert.Empty(t, runs)
}

func TestWorkflowStore_ListRecentRuns(t *testing.T) {
	db := openTestDB(t)
	ws := NewWorkflowStore(db)
	ctx := context.Background()

	wf1, err := ws.CreateWorkflow(ctx, &Workflow{Name: "recent-1", StepsJSON: `[]`, Enabled: 1})
	require.NoError(t, err)
	wf2, err := ws.CreateWorkflow(ctx, &Workflow{Name: "recent-2", StepsJSON: `[]`, Enabled: 1})
	require.NoError(t, err)

	_, err = ws.CreateWorkflowRun(ctx, wf1.ID, "api", nil)
	require.NoError(t, err)
	_, err = ws.CreateWorkflowRun(ctx, wf2.ID, "cli", nil)
	require.NoError(t, err)

	runs, err := ws.ListRecentRuns(ctx, 10, 0, nil)
	require.NoError(t, err)
	assert.Len(t, runs, 2)
	// Should have workflow names
	assert.NotNil(t, runs[0].WorkflowName)
	assert.NotNil(t, runs[1].WorkflowName)
}

func TestWorkflowStore_GetActiveRuns(t *testing.T) {
	db := openTestDB(t)
	ws := NewWorkflowStore(db)
	ctx := context.Background()

	wf, err := ws.CreateWorkflow(ctx, &Workflow{Name: "active-test", StepsJSON: `[]`, Enabled: 1})
	require.NoError(t, err)

	run1, err := ws.CreateWorkflowRun(ctx, wf.ID, "api", nil)
	require.NoError(t, err)
	run2, err := ws.CreateWorkflowRun(ctx, wf.ID, "api", nil)
	require.NoError(t, err)

	// Mark one as completed
	err = ws.SetRunStatus(ctx, run1.ID, "completed", nil)
	require.NoError(t, err)

	active, err := ws.GetActiveRunsForWorkflow(ctx, wf.ID)
	require.NoError(t, err)
	assert.Len(t, active, 1)
	assert.Equal(t, run2.ID, active[0].ID)
}

func TestWorkflowStore_Defaults(t *testing.T) {
	db := openTestDB(t)
	ws := NewWorkflowStore(db)
	ctx := context.Background()

	wf, err := ws.CreateWorkflow(ctx, &Workflow{
		Name: "defaults", StepsJSON: `[]`, Enabled: 1,
	})
	require.NoError(t, err)

	assert.Equal(t, "main", wf.BaseBranch, "default base_branch")
	assert.Equal(t, 3600, wf.MaxDurationS, "default max_duration_s")
}

func TestWorkflowStore_HydrateStepsJSON(t *testing.T) {
	db := openTestDB(t)
	ws := NewWorkflowStore(db)
	ctx := context.Background()

	steps := []json.RawMessage{
		json.RawMessage(`{"name":"s1","type":"shell","command":"echo 1"}`),
		json.RawMessage(`{"name":"s2","type":"agent","prompt":"do stuff"}`),
	}
	stepsJSON, err := StepsJSONFromRaw(steps)
	require.NoError(t, err)

	defaultAgent := `{"agent_type":"claude","model":"sonnet"}`

	wf, err := ws.CreateWorkflow(ctx, &Workflow{
		Name:             "hydrate-test",
		StepsJSON:        stepsJSON,
		DefaultAgentJSON: defaultAgent,
		Enabled:          1,
	})
	require.NoError(t, err)

	assert.Len(t, wf.Steps, 2)
	assert.Equal(t, 2, wf.StepCount)
	assert.NotNil(t, wf.DefaultAgent)
}
