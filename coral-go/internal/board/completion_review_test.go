package board

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompletionReviewRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "board.db")
	s, err := NewStore(path)
	require.NoError(t, err)
	defer func() { s.Close() }()
	_, err = s.Subscribe(ctx, "team", "reviewer", "Orchestrator", "reviewer-session", nil, nil, "all")
	require.NoError(t, err)
	up, err := s.CreateTaskWithOpts(ctx, "team", "Build", "", "medium", "lead", &CreateTaskOpts{Workflow: TaskWorkflow{RequiredOutputs: []string{"build"}}}, "worker")
	require.NoError(t, err)
	child, err := s.CreateTaskWithOpts(ctx, "team", "Test", "", "critical", "lead", &CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: up.ID, BoardID: "team", RequiredArtifacts: []string{"build"}}}}, "worker")
	require.NoError(t, err)
	independent, err := s.CreateTask(ctx, "team", "Independent", "", "low", "lead", "worker")
	require.NoError(t, err)
	_, err = s.ClaimTask(ctx, "team", "worker", up.ID)
	require.NoError(t, err)
	// A rejected mutation leaves the active row and final evidence unchanged.
	_, err = s.CompleteTaskWithArtifacts(ctx, "team", up.ID, "worker", nil, "success", nil)
	require.ErrorContains(t, err, "required output")
	task, err := s.GetTask(ctx, "team", up.ID)
	require.NoError(t, err)
	require.Equal(t, "in_progress", task.Status)
	require.Empty(t, task.Workflow.Artifacts)
	artifacts := []TaskArtifact{{Name: "build", Content: "verified candidate", Revision: "revision-A"}}
	_, err = s.SubmitCompletionReview(ctx, "team", up.ID, "stranger", "", "success", "external rejection reported", artifacts)
	require.Error(t, err)
	task, err = s.SubmitCompletionReview(ctx, "team", up.ID, "worker", "candidate built", "success", "external rejection reported", artifacts)
	require.NoError(t, err)
	require.Empty(t, task.Workflow.Outcome)
	require.Empty(t, task.Workflow.Artifacts)
	require.Equal(t, artifacts, task.Workflow.CompletionReview.Artifacts)
	_, err = s.ClaimTask(ctx, "team", "worker", independent.ID)
	require.Error(t, err) // Submission alone cannot free capacity.
	_, err = s.ReleaseCompletionReview(ctx, "team", up.ID, "fake-orchestrator", "release")
	require.Error(t, err)
	_, err = s.ReleaseCompletionReview(ctx, "team", up.ID, "worker", "release")
	require.Error(t, err)
	task, err = s.ReleaseCompletionReview(ctx, "team", up.ID, "reviewer", "independent work can continue")
	require.NoError(t, err)
	require.Equal(t, "review_pending", task.Status)
	require.Nil(t, task.CompletedAt)
	require.Nil(t, task.CompletedBy)
	require.Nil(t, task.CompletionMessage)
	require.Equal(t, "worker", *task.AssignedTo)
	require.NotNil(t, task.ClaimedAt)
	require.Equal(t, "reviewer", task.Workflow.CompletionReview.ReleasedBy)
	_, err = s.ReassignTask(ctx, "team", up.ID, "other")
	require.Error(t, err)
	_, _, err = s.UpdateTask(ctx, "team", up.ID, TaskUpdate{AssignedTo: func() *string { v := "other"; return &v }()}, 32)
	require.Error(t, err)

	_, err = s.CompleteTaskWithArtifacts(ctx, "team", up.ID, "worker", nil, "success", artifacts)
	require.Error(t, err)
	_, err = s.SubmitCompletionReview(ctx, "team", up.ID, "reviewer", "overwrite", "failed", "new reason", nil)
	require.Error(t, err)
	require.NoError(t, s.Close())
	s, err = NewStore(path)
	require.NoError(t, err)
	task, err = s.GetTask(ctx, "team", up.ID)
	require.NoError(t, err)
	require.Equal(t, "review_pending", task.Status)
	require.Equal(t, artifacts, task.Workflow.CompletionReview.Artifacts)
	require.Empty(t, task.Workflow.Outcome)
	dependent, err := s.GetTask(ctx, "team", child.ID)
	require.NoError(t, err)
	require.Equal(t, "blocked", dependent.Status)
	require.Contains(t, dependent.BlockedBy[0].BlockedReason, "review_pending")
	require.False(t, dependent.BlockedBy[0].Satisfied)
	_, err = s.ClaimTask(ctx, "team", "worker", child.ID)
	require.Error(t, err)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task, err := s.ClaimTask(ctx, "team", "worker")
			if err == nil && task != nil {
				if task.ID != independent.ID {
					t.Errorf("claimed dependent/candidate: %d", task.ID)
				}
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, wins.Load())
	_, err = s.CompleteTaskWithArtifacts(ctx, "team", up.ID, "reviewer", nil, "success", nil)
	require.ErrorContains(t, err, "required output")
	_, err = s.CompleteTaskWithArtifacts(ctx, "team", up.ID, "reviewer", nil, "success", artifacts)
	require.NoError(t, err)
	dependent, err = s.GetTask(ctx, "team", child.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", dependent.Status)
	task, err = s.GetTask(ctx, "team", up.ID)
	require.NoError(t, err)
	require.Equal(t, artifacts, task.Workflow.CompletionReview.Artifacts)
	require.Equal(t, artifacts, task.Workflow.Artifacts)
	require.Equal(t, "success", task.Workflow.Outcome)
}

func TestCompletionReviewFailedOrCancelledDoesNotSatisfySuccess(t *testing.T) {
	for _, outcome := range []string{"failed", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			_, err := s.Subscribe(ctx, "team", "lead", "Orchestrator", "session", nil, nil, "all")
			require.NoError(t, err)
			up, err := s.CreateTask(ctx, "team", "Build", "", "medium", "lead", "worker")
			require.NoError(t, err)
			child, err := s.CreateTaskWithOpts(ctx, "team", "Test", "", "medium", "lead", &CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: up.ID, BoardID: "team"}}})
			require.NoError(t, err)
			_, err = s.ClaimTask(ctx, "team", "worker", up.ID)
			require.NoError(t, err)
			_, err = s.SubmitCompletionReview(ctx, "team", up.ID, "worker", "candidate", "success", "pending review", nil)
			require.NoError(t, err)
			_, err = s.ReleaseCompletionReview(ctx, "team", up.ID, "lead", "free capacity")
			require.NoError(t, err)
			if outcome == "failed" {
				_, err = s.CompleteTaskWithArtifacts(ctx, "team", up.ID, "lead", nil, "failed", nil)
			} else {
				_, err = s.CancelTask(ctx, "team", up.ID, "lead", nil)
			}
			require.NoError(t, err)
			task, err := s.GetTask(ctx, "team", child.ID)
			require.NoError(t, err)
			require.Equal(t, "blocked", task.Status)
			task, err = s.GetTask(ctx, "team", up.ID)
			require.NoError(t, err)
			require.Equal(t, "candidate", task.Workflow.CompletionReview.Message)
		})
	}
}

func TestReviewStatusMigrationPreservesSchemaAndHistory(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	var schema string
	require.NoError(t, s.db.GetContext(ctx, &schema, "SELECT sql FROM sqlite_master WHERE name='board_tasks'"))
	require.NoError(t, func() error { _, err := s.db.ExecContext(ctx, "DROP TABLE board_tasks"); return err }())
	schema = strings.ReplaceAll(schema, ", 'review_pending'", "")
	_, err := s.db.ExecContext(ctx, schema)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `ALTER TABLE board_tasks ADD COLUMN future_column TEXT;
 CREATE TABLE migration_audit(task_id INTEGER);
 CREATE TRIGGER review_migration_audit AFTER UPDATE ON board_tasks BEGIN INSERT INTO migration_audit VALUES(NEW.id); END;
 CREATE INDEX review_future_index ON board_tasks(future_column);`)
	require.NoError(t, err)
	task, err := s.CreateTask(ctx, "team", "History", "", "medium", "lead", "worker")
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, "UPDATE board_tasks SET future_column='preserved',last_activity_at='activity',idle_snoozed_until='snooze',cost_usd=12 WHERE id=?", task.ID)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, "UPDATE sqlite_sequence SET seq=9999 WHERE name='board_tasks'")
	require.NoError(t, err)
	var auditBefore int
	require.NoError(t, s.db.GetContext(ctx, &auditBefore, "SELECT COUNT(*) FROM migration_audit"))
	_, err = s.db.ExecContext(ctx, "PRAGMA foreign_keys=ON")
	require.NoError(t, err)
	require.NoError(t, s.migrateTasksCheckConstraint(ctx))
	var fk int
	require.NoError(t, s.db.GetContext(ctx, &fk, "PRAGMA foreign_keys"))
	require.Equal(t, 1, fk)
	var value string
	require.NoError(t, s.db.GetContext(ctx, &value, "SELECT future_column FROM board_tasks WHERE id=?", task.ID))
	require.Equal(t, "preserved", value)
	require.NoError(t, s.db.GetContext(ctx, &value, "SELECT idle_snoozed_until FROM board_tasks WHERE id=?", task.ID))
	require.Equal(t, "snooze", value)
	var cost float64
	require.NoError(t, s.db.GetContext(ctx, &cost, "SELECT cost_usd FROM board_tasks WHERE id=?", task.ID))
	require.Equal(t, 12.0, cost)
	var sequence int64
	require.NoError(t, s.db.GetContext(ctx, &sequence, "SELECT seq FROM sqlite_sequence WHERE name='board_tasks'"))
	require.EqualValues(t, 9999, sequence)
	_, err = s.db.ExecContext(ctx, "UPDATE board_tasks SET status='review_pending' WHERE id=?", task.ID)
	require.NoError(t, err)
	var count int
	require.NoError(t, s.db.GetContext(ctx, &count, "SELECT COUNT(*) FROM migration_audit"))
	require.Equal(t, auditBefore+1, count)
	require.NoError(t, s.db.GetContext(ctx, &count, "SELECT COUNT(*) FROM sqlite_master WHERE name='review_future_index'"))
	require.Equal(t, 1, count)
	require.NoError(t, s.migrateTasksCheckConstraint(ctx)) // idempotent
}
