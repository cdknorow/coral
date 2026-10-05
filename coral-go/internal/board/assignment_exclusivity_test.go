package board

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// An explicit assignee is exclusive for every claim shape a worker can use.
func TestAssignedTaskIsNotClaimableByOtherSubscribers(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	task, err := s.CreateTaskWithOpts(ctx, "team", "Plan the release", "", "critical", "Operator", nil, "Orchestrator")
	require.NoError(t, err)
	require.Equal(t, "pending", task.Status)

	for _, other := range []string{"Backend Dev", "orchestrator", "Orchestrator ", " Orchestrator", "Orchestrator2", "%"} {
		got, err := s.ClaimTask(ctx, "team", other)
		require.NoError(t, err, other)
		require.Nil(t, got, "next-ready claim by %q must not take the assigned task", other)
		_, err = s.ClaimTask(ctx, "team", other, task.ID)
		require.Error(t, err, "explicit claim by %q", other)
	}

	var wg sync.WaitGroup
	won := make(chan string, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			who := fmt.Sprintf("Worker %d", i)
			if got, err := s.ClaimTask(ctx, "team", who, task.ID); err == nil && got != nil {
				won <- who
			}
			if got, err := s.ClaimTask(ctx, "team", who); err == nil && got != nil {
				won <- who
			}
		}(i)
	}
	wg.Wait()
	close(won)
	for who := range won {
		t.Fatalf("%s claimed a task assigned to Orchestrator", who)
	}

	after, err := s.GetTask(ctx, "team", task.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", after.Status)
	require.NotNil(t, after.AssignedTo)
	require.Equal(t, "Orchestrator", *after.AssignedTo)

	claimed, err := s.ClaimTask(ctx, "team", "Orchestrator")
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, task.ID, claimed.ID)
}

// Ownership must survive dependency blocking, unblocking and a draft publish.
func TestAssignmentSurvivesBlockedAndDraftLifecycle(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	dep, err := s.CreateTaskWithOpts(ctx, "team", "Build", "", "high", "Operator", nil, "Builder")
	require.NoError(t, err)
	blocked, err := s.CreateTaskWithOpts(ctx, "team", "Decide", "", "high", "Operator",
		&CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: dep.ID, BoardID: "team"}}, MaxDepth: 32}, "Orchestrator")
	require.NoError(t, err)
	require.Equal(t, "blocked", blocked.Status)
	draft, err := s.CreateTaskWithOpts(ctx, "team", "Draft", "", "high", "Operator", &CreateTaskOpts{Draft: true}, "Orchestrator")
	require.NoError(t, err)
	_, err = s.PublishTask(ctx, "team", draft.ID)
	require.NoError(t, err)

	_, err = s.ClaimTask(ctx, "team", "Builder")
	require.NoError(t, err)
	_, err = s.CompleteTask(ctx, "team", dep.ID, "Builder", nil)
	require.NoError(t, err)

	for _, id := range []int64{blocked.ID, draft.ID} {
		got, err := s.GetTask(ctx, "team", id)
		require.NoError(t, err)
		require.Equal(t, "pending", got.Status)
		require.NotNil(t, got.AssignedTo, "task %d lost its assignee", id)
		require.Equal(t, "Orchestrator", *got.AssignedTo)
		stolen, err := s.ClaimTask(ctx, "team", "Backend Dev", id)
		require.Error(t, err)
		require.Nil(t, stolen)
	}
}

// Completion is not a claim, but it must not let another agent finish an
// assigned task either. A rejected attempt must leave the whole row unchanged.
func TestCompletionOwnership(t *testing.T) {
	ctx := context.Background()
	artifact := []TaskArtifact{{Name: "report", Content: "evidence", Revision: "rev-1"}}

	type snapshot struct {
		status, assignee, completedBy string
		artifacts                     int
		claimedAt                     bool
	}
	take := func(t *testing.T, s *Store, id int64) snapshot {
		got, err := s.GetTask(ctx, "team", id)
		require.NoError(t, err)
		snap := snapshot{status: got.Status, artifacts: len(got.Workflow.Artifacts), claimedAt: got.ClaimedAt != nil}
		if got.AssignedTo != nil {
			snap.assignee = *got.AssignedTo
		}
		if got.CompletedBy != nil {
			snap.completedBy = *got.CompletedBy
		}
		return snap
	}

	for _, state := range []string{"pending", "in_progress"} {
		for _, outcome := range []string{"success", "failed"} {
			t.Run(state+"/"+outcome, func(t *testing.T) {
				s := testStore(t)
				_, err := sub(s, ctx, "team", "Lead", "Orchestrator")
				require.NoError(t, err)
				_, err = s.Subscribe(ctx, "team", "Peeker", "Reviewer", "tmux-peeker", nil, nil, "", true)
				require.NoError(t, err)
				_, err = sub(s, ctx, "team", "Retired", "Orchestrator")
				require.NoError(t, err)
				_, err = s.Unsubscribe(ctx, "team", "Retired")
				require.NoError(t, err)
				_, err = sub(s, ctx, "other", "Elsewhere", "Orchestrator") // orchestrator on another team only
				require.NoError(t, err)
				_, err = sub(s, ctx, "team", "Backend Dev", "Backend Dev")
				require.NoError(t, err)

				// One assignee per task: a worker holds only one in-progress task.
				n := 0
				newTask := func() (int64, string) {
					n++
					owner := fmt.Sprintf("Owner%d", n)
					task, err := s.CreateTaskWithOpts(ctx, "team", "Assigned work", "", "high", "Operator", nil, owner)
					require.NoError(t, err)
					if state == "in_progress" {
						claimed, err := s.ClaimTask(ctx, "team", owner, task.ID)
						require.NoError(t, err)
						require.Equal(t, task.ID, claimed.ID)
					}
					return task.ID, owner
				}

				// Rejected: other worker, unregistered name, inactive and other-team orchestrators.
				for _, who := range []string{"Backend Dev", "Stranger", "lowercase-owner", "Retired", "Elsewhere"} {
					id, owner := newTask()
					if who == "lowercase-owner" {
						who = strings.ToLower(owner)
					}
					before := take(t, s, id)
					msg := "not mine"
					_, err := s.CompleteTaskWithArtifacts(ctx, "team", id, who, &msg, outcome, artifact)
					require.Error(t, err, who)
					require.Contains(t, err.Error(), "assigned to "+owner, who)
					require.Equal(t, before, take(t, s, id), "rejected completion by %q changed the task", who)
					require.Equal(t, owner, before.assignee)
					require.Empty(t, before.completedBy)
					// The rejected attempt did not free or steal ownership.
					_, err = s.ClaimTask(ctx, "team", who, id)
					require.Error(t, err)
					require.Equal(t, before, take(t, s, id))
				}

				// Permitted: assignee, Operator, orchestrator by title, orchestrator by peek.
				for _, who := range []string{"owner", "Operator", "Lead", "Peeker"} {
					id, owner := newTask()
					if who == "owner" {
						who = owner
					}
					msg := "finished"
					done, err := s.CompleteTaskWithArtifacts(ctx, "team", id, who, &msg, outcome, artifact)
					require.NoError(t, err, who)
					require.Equal(t, "completed", done.Status)
					after := take(t, s, id)
					require.Equal(t, who, after.completedBy)
					require.Equal(t, owner, after.assignee, "completion must not rewrite the assignee")
					require.Equal(t, 1, after.artifacts)
				}
			})
		}
	}
}

// Unassigned work stays completable by anyone who may act on the board.
func TestCompletionOfUnassignedTaskIsUnchanged(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	task, err := s.CreateTask(ctx, "team", "Open work", "", "low", "Operator")
	require.NoError(t, err)
	done, err := s.CompleteTask(ctx, "team", task.ID, "Backend Dev", nil)
	require.NoError(t, err)
	require.Equal(t, "completed", done.Status)
}
