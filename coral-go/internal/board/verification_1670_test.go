package board

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTask1670_StoreAmendmentsAndHistory(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// 1. Create task with initial body and instructions (initial revision = 1)
	task, err := s.CreateTaskWithOpts(ctx, "team", "Build Component", "Original body", "medium", "Operator", nil, "worker")
	require.NoError(t, err)
	require.Equal(t, 1, task.Revision)
	require.Equal(t, "Original body", *task.Body)

	// 2. First amendment with base_revision 1
	amended1, err := s.AmendTask(ctx, "team", task.ID, "Orchestrator", 1, "Clarify acceptance criteria", map[string]interface{}{
		"body":                  "Amended body v2",
		"workflow_instructions": "Execute test fixture v2.",
	})
	require.NoError(t, err)
	require.Equal(t, 2, amended1.Revision)
	require.Equal(t, "Amended body v2", *amended1.Body)
	require.Contains(t, amended1.Workflow.EffectiveInstructions, "Execute test fixture v2.")
	require.Len(t, amended1.Workflow.Amendments, 1)

	am1 := amended1.Workflow.Amendments[0]
	assert.Equal(t, 2, am1.Revision)
	assert.Equal(t, "Orchestrator", am1.Actor)
	assert.Equal(t, "Clarify acceptance criteria", am1.Reason)
	assert.Equal(t, "Amended body v2", am1.Changes["body"])
	assert.Equal(t, "Original body", am1.PreviousSnapshot["body"])
	assert.Equal(t, "Amended body v2", am1.EffectiveSnapshot["body"])

	// 3. Second amendment with base_revision 2
	amended2, err := s.AmendTask(ctx, "team", task.ID, "Orchestrator", 2, "Second clarification", map[string]interface{}{
		"body": "Amended body v3",
	})
	require.NoError(t, err)
	require.Equal(t, 3, amended2.Revision)
	require.Equal(t, "Amended body v3", *amended2.Body)
	require.Len(t, amended2.Workflow.Amendments, 2)
}

func TestTask1670_StoreCompareAndSwapStaleRevision(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	task, err := s.CreateTaskWithOpts(ctx, "team", "Task A", "Body A", "medium", "Operator", nil, "worker")
	require.NoError(t, err)

	// Amend to revision 2
	_, err = s.AmendTask(ctx, "team", task.ID, "Orchestrator", 1, "Change 1", map[string]interface{}{"body": "Body v2"})
	require.NoError(t, err)

	// Attempting to amend with stale base_revision = 1 must fail
	_, err = s.AmendTask(ctx, "team", task.ID, "Orchestrator", 1, "Stale change", map[string]interface{}{"body": "Body stale"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "has revision 2; reread task detail before amending")

	// Verify amendments history is append-only and unmodified by failed attempt
	detail, err := s.GetTask(ctx, "team", task.ID)
	require.NoError(t, err)
	require.Equal(t, 2, detail.Revision)
	require.Equal(t, "Body v2", *detail.Body)
	require.Len(t, detail.Workflow.Amendments, 1)
}

func TestTask1670_StoreTerminalAndReviewPendingRejection(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Completed task rejection
	taskComp, err := s.CreateTask(ctx, "team", "Done", "body", "medium", "Operator", "worker")
	require.NoError(t, err)
	_, err = s.CompleteTask(ctx, "team", taskComp.ID, "worker", nil)
	require.NoError(t, err)
	_, err = s.AmendTask(ctx, "team", taskComp.ID, "Orchestrator", 1, "try amend completed", map[string]interface{}{"body": "new"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be amended (status: completed)")

	// Skipped task rejection
	taskSkip, err := s.CreateTask(ctx, "team", "Skipped", "body", "medium", "Operator", "worker")
	require.NoError(t, err)
	_, err = s.CancelTask(ctx, "team", taskSkip.ID, "Operator", nil)
	require.NoError(t, err)
	_, err = s.AmendTask(ctx, "team", taskSkip.ID, "Orchestrator", 1, "try amend skipped", map[string]interface{}{"body": "new"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be amended (status: skipped)")

	// Review pending rejection
	_, err = s.Subscribe(ctx, "team", "Orchestrator", "Orchestrator", "orch-sess-1", nil, nil, "all")
	require.NoError(t, err)

	taskReview, err := s.CreateTask(ctx, "team", "Under review", "body", "medium", "Operator", "worker")
	require.NoError(t, err)
	claimed, err := s.ClaimTask(ctx, "team", "worker")
	require.NoError(t, err)
	require.Equal(t, taskReview.ID, claimed.ID)
	_, err = s.SubmitCompletionReview(ctx, "team", taskReview.ID, "worker", "ready for review", "success", "work done", nil)
	require.NoError(t, err)
	released, err := s.ReleaseCompletionReview(ctx, "team", taskReview.ID, "Orchestrator", "release slot pending review")
	require.NoError(t, err)
	require.Equal(t, "review_pending", released.Status)

	_, err = s.AmendTask(ctx, "team", taskReview.ID, "Orchestrator", 1, "try amend review_pending", map[string]interface{}{"body": "new"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot be amended (status: review_pending)")
}

func TestTask1670_StoreUnsupportedFields(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	task, err := s.CreateTask(ctx, "team", "Feature", "body", "medium", "Operator", "worker")
	require.NoError(t, err)

	unsupported := []string{"dependencies", "required_outputs", "assigned_to", "priority", "working_mode"}
	for _, field := range unsupported {
		_, err := s.AmendTask(ctx, "team", task.ID, "Orchestrator", 1, "unsupported", map[string]interface{}{
			field: "value",
		})
		require.Error(t, err, "expected error for field %s", field)
		require.Contains(t, err.Error(), "unsupported in v1", "expected unsupported message for field %s", field)
	}
}

func TestTask1670_StoreCompletionAndReviewExpectedRevision(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// 1. Revision 1 task completes cleanly without expected revision
	t1, err := s.CreateTask(ctx, "team", "Rev 1 Task", "body", "medium", "Operator", "worker")
	require.NoError(t, err)
	claimed1, err := s.ClaimTask(ctx, "team", "worker")
	require.NoError(t, err)
	require.Equal(t, t1.ID, claimed1.ID)

	comp1, err := s.CompleteTaskWithArtifactsAtRevision(ctx, "team", t1.ID, "worker", nil, "success", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "completed", comp1.Status)

	// 2. Amended task (revision 2) rejects stale completion
	t2, err := s.CreateTask(ctx, "team", "Amended Task", "body", "medium", "Operator", "worker")
	require.NoError(t, err)
	claimed2, err := s.ClaimTask(ctx, "team", "worker")
	require.NoError(t, err)
	require.Equal(t, t2.ID, claimed2.ID)

	_, err = s.AmendTask(ctx, "team", t2.ID, "Orchestrator", 1, "Important spec change", map[string]interface{}{"body": "new spec"})
	require.NoError(t, err)

	// Stale completion with expected_revision == nil must fail
	_, err = s.CompleteTaskWithArtifactsAtRevision(ctx, "team", t2.ID, "worker", nil, "success", nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "has revision 2; reread task detail before completing")

	// Stale completion with expected_revision == 1 must fail
	rev1 := 1
	_, err = s.CompleteTaskWithArtifactsAtRevision(ctx, "team", t2.ID, "worker", nil, "success", nil, &rev1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "has revision 2; reread task detail before completing")

	// Matching completion with expected_revision == 2 succeeds
	rev2 := 2
	comp2, err := s.CompleteTaskWithArtifactsAtRevision(ctx, "team", t2.ID, "worker", nil, "success", nil, &rev2)
	require.NoError(t, err)
	require.Equal(t, "completed", comp2.Status)

	// 3. Stale review submission test
	_, err = s.Subscribe(ctx, "team", "Orchestrator", "Orchestrator", "orch-sess-2", nil, nil, "all")
	require.NoError(t, err)

	t3, err := s.CreateTask(ctx, "team", "Review Task", "body", "medium", "Operator", "worker")
	require.NoError(t, err)
	claimed3, err := s.ClaimTask(ctx, "team", "worker")
	require.NoError(t, err)
	require.Equal(t, t3.ID, claimed3.ID)

	_, err = s.AmendTask(ctx, "team", t3.ID, "Orchestrator", 1, "Review spec change", map[string]interface{}{"body": "revised review body"})
	require.NoError(t, err)

	// Stale review submission with expectedRevision == 1 fails
	_, err = s.SubmitCompletionReviewAtRevision(ctx, "team", t3.ID, "worker", "done", "success", "needs review", nil, &rev1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "has revision 2; reread task detail before submitting review")

	// Matching review submission with expectedRevision == 2 succeeds
	revTask3, err := s.SubmitCompletionReviewAtRevision(ctx, "team", t3.ID, "worker", "done", "success", "needs review", nil, &rev2)
	require.NoError(t, err)
	require.Equal(t, "in_progress", revTask3.Status)
	require.NotNil(t, revTask3.Workflow.CompletionReview)

	released3, err := s.ReleaseCompletionReview(ctx, "team", t3.ID, "Orchestrator", "releasing slot for review")
	require.NoError(t, err)
	require.Equal(t, "review_pending", released3.Status)
}

func TestTask1670_StoreReadOnlyGETDoesNotAcknowledge(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	task, err := s.CreateTask(ctx, "team", "Read Task", "body", "medium", "Operator", "worker")
	require.NoError(t, err)

	_, err = s.AmendTask(ctx, "team", task.ID, "Orchestrator", 1, "update", map[string]interface{}{"body": "new"})
	require.NoError(t, err)

	// Read task multiple times via GetTask
	for i := 0; i < 5; i++ {
		read, err := s.GetTask(ctx, "team", task.ID)
		require.NoError(t, err)
		require.Equal(t, 2, read.Revision)
		// Acknowledged revision must remain 0 (unacknowledged)
		require.Equal(t, 0, read.Workflow.AcknowledgedRevision)
	}
}

func TestTask1670_StoreFrozenWorkingModePreserved(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Create task with initial workflow custom instructions
	task, err := s.CreateTaskWithOpts(ctx, "team", "Custom Task", "initial body", "medium", "Operator", nil, "worker")
	require.NoError(t, err)

	claimed, err := s.ClaimTask(ctx, "team", "worker")
	require.NoError(t, err)
	frozenInstructions := claimed.Workflow.Instructions

	// Amend the task
	amended, err := s.AmendTask(ctx, "team", task.ID, "Orchestrator", 1, "clarify", map[string]interface{}{
		"workflow_instructions": "Additional amended instructions",
	})
	require.NoError(t, err)

	// Original Instructions must remain frozen; EffectiveInstructions contains the amendment
	require.Equal(t, frozenInstructions, amended.Workflow.Instructions)
	require.Contains(t, amended.Workflow.EffectiveInstructions, "Additional amended instructions")
}

func TestTask1670_StoreConcurrentAmendmentAndCompletionRaceFree(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	task, err := s.CreateTask(ctx, "team", "Race Task", "body", "medium", "Operator", "worker")
	require.NoError(t, err)

	claimed, err := s.ClaimTask(ctx, "team", "worker")
	require.NoError(t, err)
	require.Equal(t, task.ID, claimed.ID)

	var wg sync.WaitGroup
	wg.Add(2)

	// Thread 1: attempts to amend to revision 2
	var amendErr error
	go func() {
		defer wg.Done()
		_, amendErr = s.AmendTask(ctx, "team", task.ID, "Orchestrator", 1, "race amendment", map[string]interface{}{
			"body": "concurrent body",
		})
	}()

	// Thread 2: attempts to complete at revision 1
	var compErr error
	go func() {
		defer wg.Done()
		rev1 := 1
		_, compErr = s.CompleteTaskWithArtifactsAtRevision(ctx, "team", task.ID, "worker", nil, "success", nil, &rev1)
	}()

	wg.Wait()

	// Exactly one of the following valid outcomes must hold:
	// 1. Completion won: task is completed at revision 1, amendErr returned "cannot be amended (status: completed)".
	// 2. Amendment won: task is amended to revision 2, compErr returned "has revision 2; reread task detail before completing".
	finalTask, err := s.GetTask(ctx, "team", task.ID)
	require.NoError(t, err)

	if finalTask.Status == "completed" {
		require.NoError(t, compErr)
		require.Error(t, amendErr)
		require.Contains(t, amendErr.Error(), "completed")
	} else {
		require.Equal(t, 2, finalTask.Revision)
		require.NoError(t, amendErr)
		require.Error(t, compErr)
		require.Contains(t, compErr.Error(), "reread task detail before completing")
	}
}
