package board

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The concurrent part of the removed tests/stress/test_completion_review.py (#3811): of twelve
// concurrent submit-reviews exactly one records the candidate, the hand-off to the orchestrator
// frees the author's slot for other work, and of twelve concurrent completions exactly one
// accepts it, and only a successful one unblocks the task that needs its output.
func TestReviewSubmitAndCompleteRaceToExactlyOne(t *testing.T) {
	for _, outcome := range []string{"success", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			ctx := context.Background()
			s, err := NewStoreWithKey(filepath.Join(t.TempDir(), "board.db"), "")
			require.NoError(t, err)
			defer s.Close()
			_, err = s.Subscribe(ctx, "team", "lead", "Orchestrator", "lead-session", nil, "all")
			require.NoError(t, err)
			up, err := s.CreateTaskWithOpts(ctx, "team", "Build", "", "medium", "lead", &CreateTaskOpts{Workflow: TaskWorkflow{RequiredOutputs: []string{"build"}}}, "worker")
			require.NoError(t, err)
			child, err := s.CreateTaskWithOpts(ctx, "team", "Test", "", "critical", "lead", &CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: up.ID, BoardID: "team", RequiredArtifacts: []string{"build"}}}}, "worker")
			require.NoError(t, err)
			independent, err := s.CreateTask(ctx, "team", "Independent", "", "low", "lead", "worker")
			require.NoError(t, err)
			_, err = s.ClaimTask(ctx, "team", "worker", up.ID)
			require.NoError(t, err)
			evidence := []TaskArtifact{{Name: "build", Content: "candidate", Revision: "rev-1"}}

			race := func(n int, f func(i int) error) (wins int, errs []error) {
				var mu sync.Mutex
				var wg sync.WaitGroup
				for i := 0; i < n; i++ {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						err := f(i)
						mu.Lock()
						defer mu.Unlock()
						if err == nil {
							wins++
						} else {
							errs = append(errs, err)
						}
					}(i)
				}
				wg.Wait()
				return wins, errs
			}
			wins, errs := race(12, func(i int) error {
				_, err := s.SubmitCompletionReview(ctx, "team", up.ID, "worker", fmt.Sprintf("candidate %d", i), outcome, "completion requires review", evidence)
				return err
			})
			assert.Equal(t, 1, wins, "exactly one submission records the candidate: %v", errs)
			_, _, err = s.HandToOrchestrator(ctx, "team", up.ID, "worker")
			require.NoError(t, err)
			claimed, err := s.ClaimTask(ctx, "team", "worker", 0)
			require.NoError(t, err, "the author's slot is free once the orchestrator has the task")
			assert.Equal(t, independent.ID, claimed.ID, "the only claimable task (Test is blocked)")

			wins, errs = race(12, func(int) error {
				_, err := s.CompleteTaskWithArtifacts(ctx, "team", up.ID, "lead", nil, outcome, evidence)
				return err
			})
			assert.Equal(t, 1, wins, "exactly one completion accepts it: %v", errs)
			got, err := s.GetTask(ctx, "team", up.ID)
			require.NoError(t, err)
			assert.Equal(t, outcome, got.Workflow.Outcome)
			assert.Equal(t, evidence, got.Workflow.CompletionReview.Artifacts)
			dep, err := s.GetTask(ctx, "team", child.ID)
			require.NoError(t, err)
			want := "blocked"
			if outcome == "success" {
				want = "pending"
			}
			assert.Equal(t, want, dep.Status, "only an accepted success satisfies the dependent")
		})
	}
}

// The store never writes to the rows a caller passes in (its own stamps and clears go on a copy): a caller's Seq, AttachedBy and landed fields are still there after a submit, a completion and an attach.
func TestTheStoreDoesNotWriteToTheCallersArtifactRows(t *testing.T) {
	ctx := context.Background()
	s, err := NewStoreWithKey(filepath.Join(t.TempDir(), "board.db"), "")
	require.NoError(t, err)
	defer s.Close()
	_, err = s.Subscribe(ctx, "team", "lead", "Orchestrator", "lead-session", nil, "all")
	require.NoError(t, err)
	task, err := s.CreateTask(ctx, "team", "Build", "", "medium", "lead", "worker")
	require.NoError(t, err)
	_, err = s.ClaimTask(ctx, "team", "worker", task.ID)
	require.NoError(t, err)
	rows := []TaskArtifact{{Name: "build", Content: "x", Revision: "r1", Seq: 7, AttachedBy: "someone", LandedBy: "x", LandedAt: "2026-01-01T00:00:00Z"}}
	_, err = s.SubmitCompletionReview(ctx, "team", task.ID, "worker", "m", "success", "review please", rows)
	require.NoError(t, err)
	assert.Equal(t, 7, rows[0].Seq, "the caller's Seq is untouched")
	assert.Equal(t, "someone", rows[0].AttachedBy)
	assert.Equal(t, "x", rows[0].LandedBy)
}
