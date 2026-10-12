package board

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// blockingRunner is a registered-check runner that holds every run until
// released, like a slow go test.
type blockingRunner struct {
	started chan struct{}
	release chan struct{}
}

func newBlockingRunner() *blockingRunner {
	return &blockingRunner{started: make(chan struct{}, 4), release: make(chan struct{})}
}

func (b *blockingRunner) Run(ctx context.Context, checkID, candidateRevision string, parameters map[string]string) RegisteredCheckEvidence {
	b.started <- struct{}{}
	<-b.release
	return RegisteredCheckEvidence{Passed: true, Details: "ok", Command: "go test ./..."}
}

func checkedTask(t *testing.T, s *Store) *Task {
	t.Helper()
	task, err := s.CreateTaskWithOpts(context.Background(), "team", "Checked work", "", "medium", "lead",
		&CreateTaskOpts{Workflow: TaskWorkflow{CompletionGates: []CompletionGate{{Type: "registered_check", Name: "tests", CheckID: "go_test"}}}}, "worker")
	require.NoError(t, err)
	_, err = s.ClaimTask(context.Background(), "team", "worker", task.ID)
	require.NoError(t, err)
	return task
}

// While a slow completion check runs, the board database stays usable.
func boardStaysUsable(t *testing.T, s *Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := s.PostMessage(ctx, "team", "lead", "status update while a check runs", nil)
	require.NoError(t, err, "posting must not wait for the check")
	_, err = s.ReadMessages(ctx, "team", "lead", 10)
	require.NoError(t, err, "reading must not wait for the check")
	_, err = s.ListTasks(ctx, "team")
	require.NoError(t, err, "listing tasks must not wait for the check")
}

func TestCompleteTask_ChecksRunOutsideTransaction(t *testing.T) {
	s := testStore(t)
	s.SetCompletionChecksEnabled(true)
	runner := newBlockingRunner()
	s.SetCompletionCheckRunner(runner)
	task := checkedTask(t, s)

	done := make(chan error, 1)
	go func() {
		_, err := s.CompleteTaskWithArtifactsAtRevisionAndCandidate(context.Background(), "team", task.ID, "worker", nil, "success", nil, nil, "rev1")
		done <- err
	}()
	<-runner.started
	boardStaysUsable(t, s)
	close(runner.release)
	require.NoError(t, <-done)

	got, err := s.GetTask(context.Background(), "team", task.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", got.Status)
	require.Len(t, got.Workflow.GateResults, 1)
	require.True(t, got.Workflow.GateResults[0].Passed)
}

// A task changed while its check ran is not completed with stale results.
func TestCompleteTask_TaskChangedDuringCheckIsRejected(t *testing.T) {
	s := testStore(t)
	s.SetCompletionChecksEnabled(true)
	runner := newBlockingRunner()
	s.SetCompletionCheckRunner(runner)
	task := checkedTask(t, s)

	done := make(chan error, 1)
	go func() {
		_, err := s.CompleteTaskWithArtifactsAtRevisionAndCandidate(context.Background(), "team", task.ID, "worker", nil, "success", nil, nil, "rev1")
		done <- err
	}()
	<-runner.started
	// An amendment bumps the revision, so the revision check refuses it.
	_, err := s.AmendTask(context.Background(), "team", task.ID, "lead", 1, "tighten", map[string]interface{}{"workflow_instructions": "also run lint"})
	require.NoError(t, err)
	close(runner.release)
	require.ErrorContains(t, <-done, "reread task detail")

	got, err := s.GetTask(context.Background(), "team", task.ID)
	require.NoError(t, err)
	require.Equal(t, "in_progress", got.Status)
	require.Empty(t, got.Workflow.GateResults)
}

func TestSubmitCompletionReview_ChecksRunOutsideTransaction(t *testing.T) {
	s := testStore(t)
	s.SetCompletionChecksEnabled(true)
	runner := newBlockingRunner()
	s.SetCompletionCheckRunner(runner)
	task := checkedTask(t, s)

	done := make(chan error, 1)
	go func() {
		_, err := s.SubmitCompletionReviewAtRevisionAndCandidate(context.Background(), "team", task.ID, "worker", "candidate", "success", "needs a reviewer", nil, nil, "rev1")
		done <- err
	}()
	<-runner.started
	boardStaysUsable(t, s)
	close(runner.release)
	require.NoError(t, <-done)
	got, err := s.GetTask(context.Background(), "team", task.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Workflow.CompletionReview)
	require.Len(t, got.Workflow.CompletionReview.GateResults, 1)
}

// A change that leaves the revision alone (here the stored workflow itself) is
// caught by the snapshot comparison.
func TestCompleteTask_WorkflowChangedDuringCheckIsRejected(t *testing.T) {
	s := testStore(t)
	s.SetCompletionChecksEnabled(true)
	runner := newBlockingRunner()
	s.SetCompletionCheckRunner(runner)
	task := checkedTask(t, s)

	done := make(chan error, 1)
	go func() {
		_, err := s.CompleteTaskWithArtifactsAtRevisionAndCandidate(context.Background(), "team", task.ID, "worker", nil, "success", nil, nil, "rev1")
		done <- err
	}()
	<-runner.started
	_, err := s.db.Exec(`UPDATE task_workflows SET data = json_set(data, '$.instructions', 'changed by the orchestrator') WHERE task_id = ?`, task.ID)
	require.NoError(t, err)
	close(runner.release)
	err = <-done
	require.Error(t, err)
	require.Contains(t, err.Error(), "changed while its completion checks were running")
	got, err := s.GetTask(context.Background(), "team", task.ID)
	require.NoError(t, err)
	require.Equal(t, "in_progress", got.Status)
}
