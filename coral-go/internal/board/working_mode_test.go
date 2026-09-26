package board

import (
	"context"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
)

func TestWorkingModeClaimSnapshot(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "board.db")
	s, err := NewStore(path)
	require.NoError(t, err)
	defer func() { s.Close() }()
	mode, err := s.GetWorkingMode(ctx, "team")
	require.NoError(t, err)
	require.Equal(t, "none", mode.Mode)
	_, err = s.SetWorkingMode(ctx, "team", WorkingMode{Mode: "worktrees", DependencyGuidance: true, CustomInstructions: "Run make check."})
	require.NoError(t, err)
	task, err := s.CreateTask(ctx, "team", "Build", "", "medium", "lead")
	require.NoError(t, err)
	claimed, err := s.ClaimTask(ctx, "team", "dev", task.ID)
	require.NoError(t, err)
	require.Contains(t, claimed.Workflow.Instructions, "isolated Git worktree")
	require.Contains(t, claimed.Workflow.Instructions, "separate dependent tasks")
	require.Contains(t, claimed.Workflow.Instructions, "Run make check.")
	require.Equal(t, "worktrees", claimed.Workflow.TeamMode.Mode)
	original := claimed.Workflow.Instructions
	_, err = s.SetWorkingMode(ctx, "team", WorkingMode{Mode: "shared_checkout"})
	require.NoError(t, err)
	current, err := s.getTaskByID(ctx, "team", task.ID)
	require.NoError(t, err)
	require.Equal(t, original, current.Workflow.Instructions)
	_, err = s.ReassignTask(ctx, "team", task.ID, "other")
	require.NoError(t, err)
	reclaimed, err := s.ClaimTask(ctx, "team", "other", task.ID)
	require.NoError(t, err)
	require.Equal(t, original, reclaimed.Workflow.Instructions)
	s.Close()
	s, err = NewStore(path)
	require.NoError(t, err)
	mode, err = s.GetWorkingMode(ctx, "team")
	require.NoError(t, err)
	require.Equal(t, "shared_checkout", mode.Mode)
	next, err := s.CreateTask(ctx, "team", "Next", "", "medium", "lead")
	require.NoError(t, err)
	next, err = s.ClaimTask(ctx, "team", "newdev", next.ID)
	require.NoError(t, err)
	require.Contains(t, next.Workflow.Instructions, "shared checkout")
	_, err = s.SetWorkingMode(ctx, "team", WorkingMode{Mode: "invalid"})
	require.Error(t, err)
}

func TestNoneModeAddsNoInstructions(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "board.db"))
	require.NoError(t, err)
	defer s.Close()
	ctx := context.Background()
	task, err := s.CreateTaskWithOpts(ctx, "team", "Default", "", "medium", "lead", &CreateTaskOpts{Workflow: TaskWorkflow{TeamMode: &WorkingMode{Mode: "worktrees"}}})
	require.NoError(t, err)
	task, err = s.ClaimTask(ctx, "team", "dev", task.ID)
	require.NoError(t, err)
	require.Equal(t, DefaultTaskWorkflowInstructions, task.Workflow.Instructions)
	require.Equal(t, "none", task.Workflow.TeamMode.Mode)
}
