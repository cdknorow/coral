package board

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/ptr"
)

// #5819 (the Inbox's skip of an unchanged open task, #5803): an edit of a task's body, title, priority or assignee records edited_at and so moves updated_at, and touches
// neither the revision (amendments and review rounds own it) nor last_activity_at (the stall timer's).
func TestATaskEditMovesUpdatedAtAndNothingElse(t *testing.T) {
	const old = "2026-01-01T00:00:00Z"
	edits := map[string]TaskUpdate{
		"body":     {Body: ptr.NonEmpty("a new body")},
		"title":    {Title: ptr.NonEmpty("A new title")},
		"priority": {Priority: ptr.NonEmpty("high")},
		"assignee": {AssignedTo: ptr.NonEmpty("Operator")},
	}
	for name, upd := range edits {
		t.Run(name, func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			_, err := s.Subscribe(ctx, "p", "lead", "Orchestrator", "lead-s", nil, "all")
			require.NoError(t, err)
			task, err := s.CreateTask(ctx, "p", "T", "old body", "medium", "lead", "Dev")
			require.NoError(t, err)
			// everything that feeds updated_at is long ago, so the edit is the only thing that can move it
			_, err = s.db.ExecContext(ctx, "UPDATE board_tasks SET created_at=?, claimed_at=NULL, completed_at=NULL, last_activity_at=NULL, edited_at=NULL WHERE id=?", old, task.ID)
			require.NoError(t, err)
			read := func() (updatedAt string, revision int, activity *string) {
				require.NoError(t, s.reader().QueryRowContext(ctx, `SELECT `+updatedAtSQL+`, revision, last_activity_at FROM board_tasks WHERE id=?`, task.ID).Scan(&updatedAt, &revision, &activity))
				return
			}
			u1, r1, a1 := read()
			assert.Equal(t, old, u1)
			_, _, err = s.UpdateTask(ctx, "p", task.ID, upd, 3)
			require.NoError(t, err)
			u2, r2, a2 := read()
			assert.Greater(t, u2, u1, "the edit moves updated_at")
			assert.Equal(t, r1, r2, "and leaves the revision alone")
			assert.Equal(t, a1, a2, "and last_activity_at alone")
			// a refused edit (an empty title) records nothing
			_, err = s.db.ExecContext(ctx, "UPDATE board_tasks SET edited_at=NULL WHERE id=?", task.ID)
			require.NoError(t, err)
			empty := ""
			_, _, err = s.UpdateTask(ctx, "p", task.ID, TaskUpdate{Title: &empty}, 3)
			require.Error(t, err)
			u3, _, _ := read()
			assert.Equal(t, old, u3, "a refused edit does not move updated_at")
		})
	}
}

// An update that changes nothing (no field named) is no edit.
func TestAnUpdateThatNamesNothingIsNoEdit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, err := s.Subscribe(ctx, "p", "lead", "Orchestrator", "lead-s", nil, "all")
	require.NoError(t, err)
	task, err := s.CreateTask(ctx, "p", "T", "b", "medium", "lead", "Dev")
	require.NoError(t, err)
	_, _, err = s.UpdateTask(ctx, "p", task.ID, TaskUpdate{}, 3)
	require.NoError(t, err)
	var edited *string
	require.NoError(t, s.reader().QueryRowContext(ctx, `SELECT edited_at FROM board_tasks WHERE id=?`, task.ID).Scan(&edited))
	assert.Nil(t, edited)
}
