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

// An update that changes nothing (no field named) is no edit: edited_at is what it was.
func TestAnUpdateThatNamesNothingIsNoEdit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, err := s.Subscribe(ctx, "p", "lead", "Orchestrator", "lead-s", nil, "all")
	require.NoError(t, err)
	task, err := s.CreateTask(ctx, "p", "T", "b", "medium", "lead", "Dev")
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, "UPDATE board_tasks SET edited_at='2026-01-01T00:00:00Z' WHERE id=?", task.ID)
	require.NoError(t, err)
	_, _, err = s.UpdateTask(ctx, "p", task.ID, TaskUpdate{}, 3)
	require.NoError(t, err)
	var edited *string
	require.NoError(t, s.reader().QueryRowContext(ctx, `SELECT edited_at FROM board_tasks WHERE id=?`, task.ID).Scan(&edited))
	require.NotNil(t, edited)
	assert.Equal(t, "2026-01-01T00:00:00Z", *edited)
}

// A status change of the review flow shows in the task's text panel ("From X, status"), so a reader that keeps what it drew while updated_at is unchanged must see it move:
// the hand to the orchestrator (the status becomes review_pending), a rejection and a send-back each record edited_at (#5819, the Orchestrator's ruling), and leave the revision and last_activity_at alone.
func TestAReviewFlowStatusChangeMovesUpdatedAt(t *testing.T) {
	const old = "2026-01-01T00:00:00Z"
	e := newVerEnv(t)
	task, err := e.s.CreateTask(e.ctx, "team", "flow", "", "medium", "lead", "worker")
	require.NoError(t, err)
	id := task.ID
	_, err = e.s.ClaimTask(e.ctx, "team", "worker", id)
	require.NoError(t, err)
	state := func() (updatedAt string, revision int, activity *string, status string) {
		require.NoError(t, e.s.reader().QueryRowContext(e.ctx, `SELECT `+updatedAtSQL+`, revision, last_activity_at, status FROM board_tasks WHERE id=?`, id).Scan(&updatedAt, &revision, &activity, &status))
		return
	}
	backdate := func() {
		_, err := e.s.db.ExecContext(e.ctx, "UPDATE board_tasks SET created_at=?, claimed_at=?, completed_at=NULL, last_activity_at=NULL, edited_at=NULL WHERE id=?", old, old, id)
		require.NoError(t, err)
	}
	step := func(name, wantStatus string, do func()) {
		t.Helper()
		backdate()
		_, r1, a1, _ := state()
		do()
		u2, r2, a2, st := state()
		assert.Greater(t, u2, old, name+": updated_at moved")
		assert.Equal(t, wantStatus, st, name)
		assert.Equal(t, r1, r2, name+": the revision is left alone")
		assert.Equal(t, a1, a2, name+": and last_activity_at")
	}
	_, err = e.s.SubmitCompletionReview(e.ctx, "team", id, "worker", "built", "success", "needs review", []TaskArtifact{patchRow("fix", "d1")}) // the status stays in_progress until the hand
	require.NoError(t, err)
	step("hand to the orchestrator", "review_pending", func() {
		_, _, err := e.s.HandToOrchestrator(e.ctx, "team", id, "worker")
		require.NoError(t, err)
	})
	_, _, err = e.s.DelegateReview(e.ctx, "team", id, "lead", []string{"rev1"})
	require.NoError(t, err)
	step("rejection", "pending", func() {
		_, err := e.s.RejectReview(e.ctx, "team", id, "rev1", "not yet", 0)
		require.NoError(t, err)
	})
}

func TestASendBackMovesUpdatedAt(t *testing.T) {
	const old = "2026-01-01T00:00:00Z"
	e := newVerEnv(t)
	id := e.inReview("p", []TaskArtifact{patchRow("fix", "d1")}, "rev1")
	e.approve(id, "rev1")
	_, err := e.s.db.ExecContext(e.ctx, "UPDATE board_tasks SET created_at=?, claimed_at=?, completed_at=?, last_activity_at=NULL, edited_at=NULL WHERE id=?", old, old, old, id)
	require.NoError(t, err)
	_, err = e.s.SendBack(e.ctx, "team", id, "lead", "conflicts in a.go")
	require.NoError(t, err)
	var u string
	require.NoError(t, e.s.reader().QueryRowContext(e.ctx, `SELECT `+updatedAtSQL+` FROM board_tasks WHERE id=?`, id).Scan(&u))
	assert.Greater(t, u, old, "the send-back moved updated_at")
}

// Blocking and unblocking, and publishing a draft, change the status the panel shows.
func TestABlockedOrPublishedStatusChangeMovesUpdatedAt(t *testing.T) {
	const old = "2026-01-01T00:00:00Z"
	s := testStore(t)
	ctx := context.Background()
	_, err := s.Subscribe(ctx, "p", "lead", "Orchestrator", "lead-s", nil, "all")
	require.NoError(t, err)
	a, err := s.CreateTask(ctx, "p", "blocker", "", "medium", "lead", "Dev")
	require.NoError(t, err)
	b, err := s.CreateTask(ctx, "p", "dependent", "", "medium", "lead", "Dev")
	require.NoError(t, err)
	updatedAt := func(id int64) string {
		var u string
		require.NoError(t, s.reader().QueryRowContext(ctx, `SELECT `+updatedAtSQL+` FROM board_tasks WHERE id=?`, id).Scan(&u))
		return u
	}
	_, err = s.db.ExecContext(ctx, "UPDATE board_tasks SET created_at=?, claimed_at=NULL, completed_at=NULL, last_activity_at=NULL, edited_at=NULL", old)
	require.NoError(t, err)
	require.NoError(t, s.AddTaskDependencies(ctx, "p", b.ID, []TaskDep{{BoardID: "p", TaskID: a.ID}}, 3))
	got, err := s.GetTask(ctx, "p", b.ID)
	require.NoError(t, err)
	require.Equal(t, "blocked", got.Status)
	assert.Greater(t, updatedAt(b.ID), old, "blocked: updated_at moved")
}
