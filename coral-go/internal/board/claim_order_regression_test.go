package board

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// Similar names, integration prose and newer assignments do not establish
// supersession. Only explicit lifecycle changes retire otherwise-ready work.
func TestClaimOrderIncidentReplay(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	owner := "Frontend Dev"
	older, err := s.CreateTaskWithOpts(ctx, "team", "Old hero fix", "Already integrated; superseded in prose only", "high", "Orchestrator", nil, owner)
	require.NoError(t, err)
	newer, err := s.CreateTaskWithOpts(ctx, "team", "Current focus fix", "", "high", "Orchestrator", nil, owner)
	require.NoError(t, err)
	claimed, err := s.ClaimTask(ctx, "team", owner)
	require.NoError(t, err)
	require.Equal(t, older.ID, claimed.ID)
	_, err = s.ClaimTask(ctx, "team", owner, newer.ID)
	require.ErrorContains(t, err, "complete your current task")
	// The planner must explicitly retire redundant work; prose is not state.
	reason := "Superseded by accepted integration; retained as historical evidence"
	_, err = s.CancelTask(ctx, "team", older.ID, "Orchestrator", &reason)
	require.NoError(t, err)
	_, err = s.ClaimTask(ctx, "team", owner, older.ID)
	require.Error(t, err)
	claimed, err = s.ClaimTask(ctx, "team", owner, newer.ID)
	require.NoError(t, err)
	require.Equal(t, newer.ID, claimed.ID)
}

func TestExplicitClaimPreservesOlderReadyWork(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	owner := "Frontend Dev"
	older, err := s.CreateTaskWithOpts(ctx, "team", "Same title", "", "high", "Orchestrator", nil, owner)
	require.NoError(t, err)
	newer, err := s.CreateTaskWithOpts(ctx, "team", "Same title", "", "high", "Orchestrator", nil, owner)
	require.NoError(t, err)
	claimed, err := s.ClaimTask(ctx, "team", owner, newer.ID)
	require.NoError(t, err)
	require.Equal(t, newer.ID, claimed.ID)
	pending, err := s.GetTask(ctx, "team", older.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", pending.Status)
	_, err = s.CompleteTask(ctx, "team", newer.ID, owner, nil)
	require.NoError(t, err)
	claimed, err = s.ClaimTask(ctx, "team", owner)
	require.NoError(t, err)
	require.Equal(t, older.ID, claimed.ID)
}

func TestRetiredPredecessorCannotClaimAcrossRestartAndConcurrency(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "board.db")
	s, err := NewStore(path)
	require.NoError(t, err)
	defer func() { s.Close() }()
	old, err := s.CreateTask(ctx, "team", "Original", "", "high", "Orchestrator")
	require.NoError(t, err)
	child, err := s.CreateTaskWithOpts(ctx, "team", "Dependent", "", "medium", "Orchestrator", &CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: old.ID}}})
	require.NoError(t, err)
	_, err = s.CancelTask(ctx, "team", old.ID, "Orchestrator", nil)
	require.NoError(t, err)
	replacement, err := s.CreateTaskWithOpts(ctx, "team", "Replacement", "", "high", "Orchestrator", &CreateTaskOpts{Workflow: TaskWorkflow{RetryOf: old.ID}})
	require.NoError(t, err)
	sibling, err := s.CreateTaskWithOpts(ctx, "team", "Alternative retry", "", "high", "Orchestrator", &CreateTaskOpts{Workflow: TaskWorkflow{RetryOf: old.ID}})
	require.NoError(t, err)
	require.NoError(t, s.Close())
	s, err = NewStore(path)
	require.NoError(t, err)
	dependent, err := s.GetTask(ctx, "team", child.ID)
	require.NoError(t, err)
	require.Equal(t, "blocked", dependent.Status)
	require.Len(t, dependent.BlockedBy, 1)
	require.Equal(t, replacement.ID, dependent.BlockedBy[0].TaskID)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := s.ClaimTask(ctx, "team", "worker", old.ID); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		require.Error(t, e)
	}
	next, err := s.ClaimTask(ctx, "team", "worker")
	require.NoError(t, err)
	require.Equal(t, replacement.ID, next.ID)
	// Sibling retries are independent attempts, not implicit supersession.
	other, err := s.ClaimTask(ctx, "team", "other", sibling.ID)
	require.NoError(t, err)
	require.Equal(t, sibling.ID, other.ID)
}
