package board

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCompletedPrerequisitesStillRequireNamedArtifacts(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	qa, err := s.CreateTask(ctx, "team", "QA (#816)", "", "medium", "orch")
	require.NoError(t, err)
	_, err = s.CompleteTaskWithArtifacts(ctx, "team", qa.ID, "qa", nil, "success", []TaskArtifact{{Name: "qa_report", Content: "pass"}})
	require.NoError(t, err)
	// Legacy producers without an output contract remain readable; their
	// immutable artifacts are checked at readiness time.
	diagnosis, err := s.CreateTask(ctx, "team", "Diagnosis (#835)", "", "medium", "orch")
	require.NoError(t, err)
	_, err = s.CompleteTaskWithArtifacts(ctx, "team", diagnosis.ID, "dev", nil, "success", []TaskArtifact{{Name: "diagnostic", Content: "diagnosis"}, {Name: "verification", Content: "evidence"}})
	require.NoError(t, err)
	deps := []TaskDep{{TaskID: qa.ID, RequiredArtifacts: []string{"qa_report"}}, {TaskID: diagnosis.ID, RequiredArtifacts: []string{"candidate", "verification"}}}
	task, err := s.CreateTaskWithOpts(ctx, "team", "Follow-up (#837)", "", "medium", "orch", &CreateTaskOpts{BlockedBy: deps}, "dev")
	require.NoError(t, err)
	require.Equal(t, "blocked", task.Status)
	require.True(t, task.BlockedBy[0].Satisfied)
	d := task.BlockedBy[1]
	require.Equal(t, "completed", d.Status)
	require.Equal(t, "success", d.Outcome)
	require.False(t, d.Satisfied)
	require.Equal(t, []string{"candidate"}, d.MissingArtifacts)
	require.Equal(t, "missing required artifacts: candidate", d.BlockedReason)
	// Recomputing cannot make an unmet artifact condition true.
	require.NoError(t, s.recoverTaskReadiness(ctx))
	claimed, err := s.ClaimTask(ctx, "team", "dev")
	require.NoError(t, err)
	require.Nil(t, claimed)
	notices, err := s.ResolveDownstreamTasks(ctx, "team", diagnosis.ID)
	require.NoError(t, err)
	require.Empty(t, notices)
	blocked, err := s.BlockedTasksForSubscriber(ctx, "team", "dev", 0)
	require.NoError(t, err)
	require.Len(t, blocked, 1)
	require.Contains(t, blocked[0].Reasons[0], "missing required artifacts: candidate")
	other, err := s.BlockedTasksForSubscriber(ctx, "team", "other", 0)
	require.NoError(t, err)
	require.Empty(t, other)
	// An explicit correction of the contract is enough; no standalone retry needed.
	deps[1].RequiredArtifacts = []string{"diagnostic", "verification"}
	_, _, err = s.UpdateTask(ctx, "team", task.ID, TaskUpdate{BlockedBy: &deps}, 32)
	require.NoError(t, err)
	claimed, err = s.ClaimTask(ctx, "team", "dev")
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, task.ID, claimed.ID)
	require.Len(t, claimed.Workflow.Inputs, 2)
}

func TestDependencyStatusExplainsOutcomeAndLegacyCompletion(t *testing.T) {
	for _, tc := range []struct {
		status, outcome, condition string
		ready                      bool
	}{
		{"completed", "", "success", true}, {"completed", "failed", "success", false},
		{"completed", "failed", "failure", true}, {"skipped", "", "success", false},
		{"skipped", "", "termination", true}, {"in_progress", "", "termination", false},
	} {
		t.Run(tc.status+tc.outcome+tc.condition, func(t *testing.T) {
			_, _, reason := dependencyStatus(tc.status, TaskWorkflow{Outcome: tc.outcome}, tc.condition, nil)
			require.Equal(t, tc.ready, reason == "")
		})
	}
}

func TestDependencyContractRejectsUndeclaredArtifact(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	upstream, err := s.CreateTaskWithOpts(ctx, "team", "Producer", "", "medium", "lead", &CreateTaskOpts{Workflow: TaskWorkflow{RequiredOutputs: []string{"diagnostic", "verification"}}})
	require.NoError(t, err)
	_, err = s.CreateTaskWithOpts(ctx, "team", "Consumer", "", "medium", "lead", &CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: upstream.ID, RequiredArtifacts: []string{"candidate", "verification"}}}})
	require.ErrorContains(t, err, `cannot require artifact "candidate"`)
	// A declared output is a valid dependency contract even before completion.
	consumer, err := s.CreateTaskWithOpts(ctx, "team", "Consumer", "", "medium", "lead", &CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: upstream.ID, RequiredArtifacts: []string{"diagnostic", "verification"}}}})
	require.NoError(t, err)
	require.Equal(t, "blocked", consumer.Status)
}
