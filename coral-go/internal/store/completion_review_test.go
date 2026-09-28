package store

import (
	"context"
	"testing"

	"github.com/cdknorow/coral/internal/board"
	"github.com/stretchr/testify/require"
)

func TestPersonalTaskReviewProjectionAndCapacity(t *testing.T) {
	db := openTestDB(t)
	s := NewTaskStore(db)
	ctx := context.Background()
	sid := "review-session"
	project := AgentTaskProject("worker", &sid)
	_, err := db.TaskEngine.Subscribe(ctx, project, "lead", "Orchestrator", "lead-session", nil, nil, "all")
	require.NoError(t, err)
	task, err := s.CreateAgentTaskWithWorkflow(ctx, "worker", "Build", &sid, nil, "", "medium", nil)
	require.NoError(t, err)
	next, err := s.CreateAgentTaskWithWorkflow(ctx, "worker", "Independent", &sid, nil, "", "medium", nil)
	require.NoError(t, err)
	_, err = s.ClaimNextAgentTask(ctx, "worker", &sid, task.ID)
	require.NoError(t, err)
	_, err = db.TaskEngine.SubmitCompletionReview(ctx, project, task.ID, "worker", "candidate", "success", "pending review", []board.TaskArtifact{{Name: "build", Content: "candidate"}})
	require.NoError(t, err)
	_, err = db.TaskEngine.ReleaseCompletionReview(ctx, project, task.ID, "lead", "free capacity")
	require.NoError(t, err)
	task, err = s.GetAgentTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, AgentTaskReviewPending, task.Completed)
	require.Equal(t, "review_pending", task.Status)
	require.Empty(t, task.Workflow.Outcome)
	active, err := s.CurrentAgentTask(ctx, "worker", &sid)
	require.NoError(t, err)
	require.Nil(t, active)
	claimed, err := s.ClaimNextAgentTask(ctx, "worker", &sid)
	require.NoError(t, err)
	require.Equal(t, next.ID, claimed.ID)
}
