package board

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListTeamArtifactTasksIsBoundedInSQLAndScoped(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		task, err := s.CreateTask(ctx, "team", fmt.Sprintf("t%d", i), "", "medium", "lead")
		require.NoError(t, err)
		_, err = s.CompleteTaskWithArtifacts(ctx, "team", task.ID, "lead", nil, "success", []TaskArtifact{{Name: "a", Content: "x"}})
		require.NoError(t, err)
	}
	plain, err := s.CreateTask(ctx, "team", "plain", "", "medium", "lead")
	require.NoError(t, err)
	_, err = s.CompleteTaskWithArtifacts(ctx, "team", plain.ID, "lead", nil, "success", nil)
	require.NoError(t, err)
	other, err := s.CreateTask(ctx, "other", "o", "", "medium", "lead")
	require.NoError(t, err)
	_, err = s.CompleteTaskWithArtifacts(ctx, "other", other.ID, "lead", nil, "success", []TaskArtifact{{Name: "o", Content: "x"}})
	require.NoError(t, err)

	tasks, truncated, err := s.ListTeamArtifactTasks(ctx, "team", 2)
	require.NoError(t, err)
	require.True(t, truncated, "older matching tasks exist beyond the scan limit")
	require.Len(t, tasks, 2, "only limit tasks are decoded")
	require.Greater(t, tasks[0].TaskID, tasks[1].TaskID, "newest task first")

	all, truncated, err := s.ListTeamArtifactTasks(ctx, "team", 100)
	require.NoError(t, err)
	require.False(t, truncated)
	require.Len(t, all, 4, "tasks without artifacts and other teams' tasks are excluded")
}
