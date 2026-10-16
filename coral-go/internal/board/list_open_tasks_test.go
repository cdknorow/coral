package board

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ListOpenTasks must return exactly the open subset of ListTasks, in the same
// order and with the same BlockedBy, but without hydrating workflows.
func TestListOpenTasks_MatchesOpenSubsetOfListTasks(t *testing.T) {
	ctx := context.Background()
	s, err := NewStoreWithKey(filepath.Join(t.TempDir(), "board.db"), "")
	require.NoError(t, err)
	defer s.Close()

	done, err := s.CreateTask(ctx, "team", "Done", "", "medium", "lead", "worker")
	require.NoError(t, err)
	_, err = s.ClaimTask(ctx, "team", "worker", done.ID)
	require.NoError(t, err)
	_, err = s.CompleteTask(ctx, "team", done.ID, "worker", nil)
	require.NoError(t, err)

	active, err := s.CreateTask(ctx, "team", "Active", "", "low", "lead", "worker")
	require.NoError(t, err)
	_, err = s.ClaimTask(ctx, "team", "worker", active.ID)
	require.NoError(t, err)
	_, err = s.CreateTaskWithOpts(ctx, "team", "Blocked", "", "critical", "lead",
		&CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: active.ID, BoardID: "team"}}})
	require.NoError(t, err)
	_, err = s.CreateTask(ctx, "team", "Pending", "", "high", "lead")
	require.NoError(t, err)
	_, err = s.CreateTask(ctx, "other", "Other board", "", "high", "lead")
	require.NoError(t, err)

	all, err := s.ListTasks(ctx, "team")
	require.NoError(t, err)
	var want []Task
	for _, task := range all {
		if task.Status != "completed" {
			want = append(want, task)
		}
	}
	require.Len(t, want, 3)

	got, err := s.ListOpenTasks(ctx, "team")
	require.NoError(t, err)
	require.Len(t, got, len(want))
	for i := range want {
		assert.Equal(t, want[i].ID, got[i].ID)
		assert.Equal(t, want[i].Status, got[i].Status)
		assert.Equal(t, want[i].AssignedTo, got[i].AssignedTo)
		assert.Equal(t, want[i].BlockedBy, got[i].BlockedBy)
		assert.Zero(t, got[i].Workflow, "ListOpenTasks must not hydrate workflows")
	}
	blocked := got[0]
	assert.Equal(t, "Blocked", blocked.Title)
	require.Len(t, blocked.BlockedBy, 1)
	assert.Equal(t, active.ID, blocked.BlockedBy[0].TaskID)
	assert.Equal(t, "in_progress", blocked.BlockedBy[0].Status)
}
