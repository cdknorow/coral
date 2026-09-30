package board

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAmendTaskRevisionAndEffectiveInstructions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	task, err := s.CreateTaskWithOpts(ctx, "team", "Implement feature", "old body", "high", "Operator", nil, "worker")
	require.NoError(t, err)
	updated, err := s.AmendTask(ctx, "team", task.ID, "Orchestrator", 1, "authoritative correction", map[string]interface{}{
		"body": "new body", "workflow_instructions": "Use the corrected fixture.",
	})
	require.NoError(t, err)
	require.Equal(t, 2, updated.Revision)
	require.Equal(t, "new body", *updated.Body)
	require.Len(t, updated.Workflow.Amendments, 1)
	require.Contains(t, updated.Workflow.EffectiveInstructions, "Use the corrected fixture.")

	_, err = s.AmendTask(ctx, "team", task.ID, "Orchestrator", 1, "stale", map[string]interface{}{"body": "stale"})
	require.Error(t, err)
	_, err = s.AmendTask(ctx, "team", task.ID, "Orchestrator", 2, "unsupported", map[string]interface{}{"required_outputs": []interface{}{"x"}})
	require.Error(t, err)
}

func TestAmendTaskRejectsReviewAndTerminal(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, "team", "Done", "body", "medium", "Operator", "worker")
	require.NoError(t, err)
	_, err = s.CompleteTask(ctx, "team", task.ID, "Operator", nil)
	require.NoError(t, err)
	_, err = s.AmendTask(ctx, "team", task.ID, "Orchestrator", 1, "late", map[string]interface{}{"body": "changed"})
	require.Error(t, err)
}
