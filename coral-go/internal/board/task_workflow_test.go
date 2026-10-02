package board

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

type countingCompletionRunner struct{ calls int }

func (r *countingCompletionRunner) Run(context.Context, string, string, map[string]string) RegisteredCheckEvidence {
	r.calls++
	return RegisteredCheckEvidence{Passed: true, Details: "unexpected runner call"}
}

func TestWorkflowReadinessSurvivesRestart(t *testing.T) {
	for _, outcome := range []string{"success", "failed", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "board.db")
			s, err := NewStore(path)
			require.NoError(t, err)
			defer func() { s.Close() }()
			up, err := s.CreateTask(ctx, "upstream", "Build", "", "medium", "lead")
			require.NoError(t, err)
			children := map[string]int64{}
			for _, condition := range []string{"success", "failure", "termination"} {
				child, err := s.CreateTaskWithOpts(ctx, "downstream", condition, "", "medium", "lead", &CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: up.ID, BoardID: "upstream", Condition: condition}}})
				require.NoError(t, err)
				children[condition] = child.ID
			}
			if outcome == "cancelled" {
				_, err = s.CancelTask(ctx, "upstream", up.ID, "builder", nil)
			} else {
				_, err = s.CompleteTaskWithArtifacts(ctx, "upstream", up.ID, "builder", nil, outcome, nil)
			}
			require.NoError(t, err)
			check := func() {
				for condition, id := range children {
					child, err := s.GetTask(ctx, "downstream", id)
					require.NoError(t, err)
					want := "blocked"
					if condition == "termination" || condition == "success" && outcome == "success" || condition == "failure" && outcome == "failed" {
						want = "pending"
					}
					require.Equal(t, want, child.Status)
				}
			}
			check() // No notifier was invoked: readiness is already committed.
			require.NoError(t, s.Close())
			s, err = NewStore(path)
			require.NoError(t, err)
			check()
			notices, err := s.ResolveDownstreamTasks(ctx, "", 0)
			require.NoError(t, err)
			want := 2
			if outcome == "cancelled" {
				want = 1
			}
			require.Len(t, notices, want)
			notices, err = s.ResolveDownstreamTasks(ctx, "", 0)
			require.NoError(t, err)
			require.Empty(t, notices)
			_, err = s.ClaimTask(ctx, "downstream", "worker", children["termination"])
			require.NoError(t, err)
		})
	}
}

func TestCancelStallsPendingDescendantsAndTheyCannotBeClaimed(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	upstream, err := s.CreateTask(ctx, "team", "Build", "", "medium", "lead")
	require.NoError(t, err)
	child, err := s.CreateTaskWithOpts(ctx, "team", "Test", "", "medium", "qa", &CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: upstream.ID}}})
	require.NoError(t, err)
	require.Equal(t, "blocked", child.Status)
	// Simulate stale metadata left by an older queue implementation.
	_, err = s.db.ExecContext(ctx, "UPDATE board_tasks SET status='pending' WHERE id=?", child.ID)
	require.NoError(t, err)
	_, err = s.CancelTask(ctx, "team", upstream.ID, "lead", nil)
	require.NoError(t, err)
	stalled, err := s.StallDownstreamTasks(ctx, "team", upstream.ID)
	require.NoError(t, err)
	// The cancellation path is idempotent; if the row was already reconciled,
	// it remains blocked and is still excluded from claims.
	if len(stalled) == 0 {
		got, getErr := s.GetTask(ctx, "team", child.ID)
		require.NoError(t, getErr)
		require.Equal(t, "blocked", got.Status)
	}
	claimed, err := s.ClaimTask(ctx, "team", "qa")
	require.NoError(t, err)
	require.Nil(t, claimed)
}

func TestWorkflowStartupRepairsLegacyBlockedTasks(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "board.db")
	s, err := NewStore(path)
	require.NoError(t, err)
	defer func() { s.Close() }()
	up, err := s.CreateTask(ctx, "team", "Build", "", "medium", "lead")
	require.NoError(t, err)
	child, err := s.CreateTaskWithOpts(ctx, "team", "Test", "", "medium", "lead", &CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: up.ID}}})
	require.NoError(t, err)
	// Seed precisely the state left by old versions when the callback was lost.
	_, err = s.db.ExecContext(ctx, "UPDATE board_tasks SET status = 'completed' WHERE id = ?", up.ID)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	s, err = NewStore(path)
	require.NoError(t, err)
	got, err := s.GetTask(ctx, "team", child.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", got.Status)
}

func TestWorkflowCompletionRollsBackIfReadinessFails(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	up, err := s.CreateTask(ctx, "team", "Build", "", "medium", "lead")
	require.NoError(t, err)
	_, err = s.CreateTaskWithOpts(ctx, "team", "Test", "", "medium", "lead", &CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: up.ID}}})
	require.NoError(t, err)
	_, err = s.db.Exec(`CREATE TRIGGER fail_ready BEFORE INSERT ON task_ready_notifications BEGIN SELECT RAISE(ABORT, 'injected readiness failure'); END`)
	require.NoError(t, err)
	_, err = s.CompleteTaskWithArtifacts(ctx, "team", up.ID, "builder", nil, "success", []TaskArtifact{{Name: "build", Content: "candidate"}})
	require.ErrorContains(t, err, "injected readiness failure")
	got, err := s.GetTask(ctx, "team", up.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", got.Status)
	require.Empty(t, got.Workflow.Artifacts)
}

func TestWorkflowRequiredArtifactLimits(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	names := make([]string, 33)
	for i := range names {
		names[i] = fmt.Sprintf("output-%d", i)
	}
	_, err := s.CreateTaskWithOpts(ctx, "team", "Invalid", "", "medium", "lead", &CreateTaskOpts{Workflow: TaskWorkflow{RequiredOutputs: names}})
	require.ErrorContains(t, err, "at most 32")
	tasks, err := s.ListTasks(ctx, "team")
	require.NoError(t, err)
	require.Empty(t, tasks)
	up, err := s.CreateTaskWithOpts(ctx, "team", "Build", "", "medium", "lead", &CreateTaskOpts{Workflow: TaskWorkflow{RequiredOutputs: names[:32]}})
	require.NoError(t, err)
	child, err := s.CreateTaskWithOpts(ctx, "team", "Test", "", "medium", "lead", &CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: up.ID, RequiredArtifacts: names[:32]}}})
	require.NoError(t, err)
	deps := []TaskDep{{TaskID: up.ID, RequiredArtifacts: names}}
	_, _, err = s.UpdateTask(ctx, "team", child.ID, TaskUpdate{BlockedBy: &deps}, 32)
	require.ErrorContains(t, err, "at most 32")
	got, err := s.GetTask(ctx, "team", child.ID)
	require.NoError(t, err)
	require.Len(t, got.BlockedBy[0].RequiredArtifacts, 32)
	var artifacts []TaskArtifact
	for _, name := range names[:32] {
		artifacts = append(artifacts, TaskArtifact{Name: name, Content: "evidence"})
	}
	_, err = s.CompleteTaskWithArtifacts(ctx, "team", up.ID, "builder", nil, "success", artifacts)
	require.NoError(t, err)
	got, err = s.GetTask(ctx, "team", child.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", got.Status)
}

func TestArtifactWorkflowBuildTestRelease(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	build, err := s.CreateTaskWithOpts(ctx, "team", "Build", "Implement", "medium", "lead", &CreateTaskOpts{Workflow: TaskWorkflow{Name: "Ship", Stage: "Build", Instructions: "Use the project test suite.", RequiredOutputs: []string{"build"}}})
	require.NoError(t, err)
	require.Contains(t, build.Workflow.Instructions, DefaultTaskWorkflowInstructions)
	require.Contains(t, build.Workflow.Instructions, "Use the project test suite.")
	test, err := s.CreateTaskWithOpts(ctx, "team", "Test", "Verify", "medium", "lead", &CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: build.ID, RequiredArtifacts: []string{"build"}}}, Workflow: TaskWorkflow{Stage: "Test", RequiredOutputs: []string{"test_report"}}})
	require.NoError(t, err)
	release, err := s.CreateTaskWithOpts(ctx, "team", "Release", "Publish", "medium", "lead", &CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: build.ID, RequiredArtifacts: []string{"build"}}, {TaskID: test.ID, RequiredArtifacts: []string{"test_report"}}}})
	require.NoError(t, err) // diamond DAG: Release depends on Build directly and through Test
	require.Equal(t, "blocked", release.Status)
	_, err = s.ClaimTask(ctx, "team", "tester", test.ID)
	require.Error(t, err)
	_, err = s.CompleteTask(ctx, "team", build.ID, "builder", nil)
	require.ErrorContains(t, err, "required output")
	unchanged, _ := s.GetTask(ctx, "team", build.ID)
	require.Equal(t, "pending", unchanged.Status)
	artifact := TaskArtifact{Name: "build", URI: "https://example.test/builds/42", Revision: "tree-42", Digest: "sha256:42"}
	_, err = s.CompleteTaskWithArtifacts(ctx, "team", build.ID, "builder", nil, "success", []TaskArtifact{artifact})
	require.NoError(t, err)
	unblocked, err := s.ResolveDownstreamTasks(ctx, "team", build.ID)
	require.NoError(t, err)
	require.Len(t, unblocked, 1)
	require.Equal(t, test.ID, unblocked[0].ID)
	claimed, err := s.ClaimTask(ctx, "team", "tester", test.ID)
	require.NoError(t, err)
	require.Equal(t, artifact, claimed.Workflow.Inputs[0].Artifacts[0])
	current := s.ActiveTaskForSubscriber(ctx, "team", "tester")
	require.Equal(t, claimed.Workflow, current.Workflow)
	_, err = s.CompleteTaskWithArtifacts(ctx, "team", test.ID, "tester", nil, "success", []TaskArtifact{{Name: "test_report", Content: "Passed", Revision: "tree-42"}})
	require.NoError(t, err)
	unblocked, err = s.ResolveDownstreamTasks(ctx, "team", test.ID)
	require.NoError(t, err)
	require.Len(t, unblocked, 1)
	require.Equal(t, release.ID, unblocked[0].ID)
	claimed, err = s.ClaimTask(ctx, "team", "releaser", release.ID)
	require.NoError(t, err)
	require.Len(t, claimed.Workflow.Inputs, 2)
	_, err = s.CompleteTaskWithArtifacts(ctx, "team", build.ID, "builder", nil, "success", []TaskArtifact{{Name: "build", Content: "different revision"}})
	require.Error(t, err) // evidence is immutable
	deps := []TaskDep{{TaskID: build.ID}}
	_, _, err = s.UpdateTask(ctx, "team", release.ID, TaskUpdate{BlockedBy: &deps}, 32)
	require.Error(t, err) // an executing release cannot switch its inputs
}

func TestTaskArtifactsRejectLocalPaths(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, "team", "Publish", "", "medium", "lead")
	require.NoError(t, err)
	for _, uri := range []string{"/private/tmp/report.md", "~/report.md", "./report.md", "../report.md", "file:///tmp/report.md"} {
		_, err = s.CompleteTaskWithArtifacts(ctx, "team", task.ID, "lead", nil, "success", []TaskArtifact{{Name: "report", URI: uri}})
		require.ErrorContains(t, err, "local filesystem path")
	}
	_, err = s.CompleteTaskWithArtifacts(ctx, "team", task.ID, "lead", nil, "success", []TaskArtifact{{Name: "report", Content: "report contents"}})
	require.NoError(t, err)
}

func TestCompletionGatesRequireEvidenceAndCandidateRevision(t *testing.T) {
	s := testStore(t)
	s.SetCompletionChecksEnabled(true)
	ctx := context.Background()
	task, err := s.CreateTaskWithOpts(ctx, "team", "Verify", "", "medium", "lead", &CreateTaskOpts{Workflow: TaskWorkflow{CompletionGates: []CompletionGate{{Type: "test_evidence", Name: "tests", Artifact: "tests"}}}})
	require.NoError(t, err)
	_, err = s.CompleteTaskWithArtifactsAtRevisionAndCandidate(ctx, "team", task.ID, "lead", nil, "success", []TaskArtifact{{Name: "tests", Content: "exit 0"}}, nil, "rev-1")
	require.ErrorContains(t, err, "completion gate")
	unchanged, _ := s.GetTask(ctx, "team", task.ID)
	require.Equal(t, "pending", unchanged.Status)
	exitCode := 0
	evidence := TaskArtifact{Name: "tests", Kind: "test", Revision: "rev-1", Command: "go test ./...", Runner: "ci", ExitCode: &exitCode, OutputDigest: "sha256:tests", StartedAt: "2026-10-02T18:00:00Z", FinishedAt: "2026-10-02T18:01:00Z", Content: "PASS"}
	completed, err := s.CompleteTaskWithArtifactsAtRevisionAndCandidate(ctx, "team", task.ID, "lead", nil, "success", []TaskArtifact{evidence}, nil, "rev-1")
	require.NoError(t, err)
	require.Equal(t, "completed", completed.Status)
	require.Equal(t, "rev-1", completed.Workflow.CandidateRevision)
	require.Len(t, completed.Workflow.GateResults, 1)
	require.True(t, completed.Workflow.GateResults[0].Passed)
}

func TestCompletionGatesRejectUnsupportedChecksAndLandingMismatch(t *testing.T) {
	s := testStore(t)
	s.SetCompletionChecksEnabled(true)
	ctx := context.Background()
	_, err := s.CreateTaskWithOpts(ctx, "team", "Unsafe", "", "medium", "lead", &CreateTaskOpts{Workflow: TaskWorkflow{CompletionGates: []CompletionGate{{Type: "check", Artifact: "result"}}}})
	require.ErrorContains(t, err, "registered check scripts are not configured")
	task, err := s.CreateTaskWithOpts(ctx, "team", "Land", "", "medium", "lead", &CreateTaskOpts{Workflow: TaskWorkflow{CompletionGates: []CompletionGate{{Type: "landed_revision", Artifact: "landing", Remote: "origin", Branch: "release"}}}})
	require.NoError(t, err)
	_, err = s.CompleteTaskWithArtifactsAtRevisionAndCandidate(ctx, "team", task.ID, "lead", nil, "success", []TaskArtifact{{Name: "landing", Revision: "rev-2", Remote: "origin", Branch: "main", Landed: true, Content: "attestation"}}, nil, "rev-2")
	require.ErrorContains(t, err, "branch")
	got, _ := s.GetTask(ctx, "team", task.ID)
	require.Equal(t, "pending", got.Status)
}

func TestCompletionGateAmendmentClearsPriorResultsAndRequiresNewRevision(t *testing.T) {
	s := testStore(t)
	s.SetCompletionChecksEnabled(true)
	ctx := context.Background()
	task, err := s.CreateTaskWithOpts(ctx, "team", "Amend gates", "", "medium", "lead", &CreateTaskOpts{Workflow: TaskWorkflow{CompletionGates: []CompletionGate{{Type: "report", Artifact: "report"}}}})
	require.NoError(t, err)
	_, err = s.AmendTask(ctx, "team", task.ID, "Orchestrator", 1, "tighten evidence", map[string]interface{}{"completion_gates": []interface{}{map[string]interface{}{"type": "report", "artifact": "final"}}})
	require.NoError(t, err)
	got, _ := s.GetTask(ctx, "team", task.ID)
	require.Equal(t, 2, got.Revision)
	require.Len(t, got.Workflow.CompletionGates, 1)
	require.Equal(t, "final", got.Workflow.CompletionGates[0].Artifact)
	require.Empty(t, got.Workflow.GateResults)
	_, err = s.CompleteTaskWithArtifactsAtRevisionAndCandidate(ctx, "team", task.ID, "lead", nil, "success", []TaskArtifact{{Name: "report", Content: "old"}}, &got.Revision, "")
	require.ErrorContains(t, err, "artifact")
}

func TestRegisteredCompletionCheckOwnsResultAndRejectsForgedCompletion(t *testing.T) {
	s := testStore(t)
	s.SetCompletionChecksEnabled(true)
	ctx := context.Background()
	workdir, revision := gitFixture(t)
	s.SetCompletionCheckRunner(NewLocalRegisteredCheckRunner(workdir))
	task, err := s.CreateTaskWithOpts(ctx, "team", "Run trusted tests", "", "medium", "lead", &CreateTaskOpts{Workflow: TaskWorkflow{CompletionGates: []CompletionGate{{Type: "registered_check", Name: "trusted tests", CheckID: "go_test", Parameters: map[string]string{"packages": "./..."}}}}})
	require.NoError(t, err)
	// Completion has no check-result input to forge; Coral executes the
	// registered command and stores the server-owned result.
	completed, err := s.CompleteTaskWithArtifactsAtRevisionAndCandidate(ctx, "team", task.ID, "lead", nil, "success", nil, nil, revision)
	require.NoError(t, err)
	require.Equal(t, "completed", completed.Status)
	require.Len(t, completed.Workflow.GateResults, 1)
	require.True(t, completed.Workflow.GateResults[0].Passed)
	require.Equal(t, "go test ./...", completed.Workflow.GateResults[0].Command)

	missingRunner := testStore(t)
	missingRunner.SetCompletionChecksEnabled(true)
	deferred, err := missingRunner.CreateTaskWithOpts(ctx, "team", "Unavailable check", "", "medium", "lead", &CreateTaskOpts{Workflow: TaskWorkflow{CompletionGates: []CompletionGate{{Type: "registered_check", CheckID: "go_test"}}}})
	require.NoError(t, err)
	_, err = missingRunner.CompleteTaskWithArtifactsAtRevisionAndCandidate(ctx, "team", deferred.ID, "lead", nil, "success", nil, nil, revision)
	require.ErrorContains(t, err, "runner unavailable")
}

func TestRegisteredCompletionChecksStayDormantWhenDisabled(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, gate := range []CompletionGate{
		{Type: "report", Artifact: "report"},
		{Type: "test_evidence", Artifact: "tests"},
		{Type: "landed_revision", Artifact: "landing", Remote: "origin", Branch: "release"},
		{Type: "registered_check", CheckID: "go_test"},
	} {
		_, err := s.CreateTaskWithOpts(ctx, "team", "Disabled check", "", "medium", "lead", &CreateTaskOpts{Workflow: TaskWorkflow{CompletionGates: []CompletionGate{gate}}})
		require.ErrorContains(t, err, "completion_gates are an experimental feature disabled")
	}

	// A declaration created during an explicit rollout remains readable and is
	// allowed to complete after the rollout is disabled, but it is recorded as
	// unavailable and never invokes the runner.
	s.SetCompletionChecksEnabled(true)
	task, err := s.CreateTaskWithOpts(ctx, "team", "Persisted check", "", "medium", "lead", &CreateTaskOpts{Workflow: TaskWorkflow{CompletionGates: []CompletionGate{
		{Type: "report", Artifact: "report"},
		{Type: "test_evidence", Artifact: "tests"},
		{Type: "landed_revision", Artifact: "landing", Remote: "origin", Branch: "release"},
		{Type: "registered_check", CheckID: "go_test"},
	}}})
	require.NoError(t, err)
	s.SetCompletionChecksEnabled(false)
	runner := &countingCompletionRunner{}
	s.SetCompletionCheckRunner(runner)
	completed, err := s.CompleteTaskWithArtifactsAtRevisionAndCandidate(ctx, "team", task.ID, "lead", nil, "success", nil, nil, "candidate")
	require.NoError(t, err)
	require.Equal(t, "completed", completed.Status)
	require.Len(t, completed.Workflow.GateResults, 4)
	require.Zero(t, runner.calls)
	for _, result := range completed.Workflow.GateResults {
		require.False(t, result.Passed)
		require.Contains(t, result.Details, "disabled by server policy")
	}
	required, err := s.CreateTaskWithOpts(ctx, "team", "Required output remains enforced", "", "medium", "lead", &CreateTaskOpts{Workflow: TaskWorkflow{RequiredOutputs: []string{"build"}}})
	require.NoError(t, err)
	_, err = s.CompleteTaskWithArtifacts(ctx, "team", required.ID, "lead", nil, "success", nil)
	require.ErrorContains(t, err, `required output "build" is missing`)

	amendable, err := s.CreateTask(ctx, "team", "Amend gate", "", "medium", "lead")
	require.NoError(t, err)
	_, err = s.AmendTask(ctx, "team", amendable.ID, "Orchestrator", 1, "defer trusted check", map[string]interface{}{
		"completion_gates": []interface{}{map[string]interface{}{"type": "registered_check", "check_id": "go_test"}},
	})
	require.ErrorContains(t, err, "completion_gates are an experimental feature disabled")
}

func TestWorkflowFailureCancellationAndRetry(t *testing.T) {
	for _, outcome := range []string{"failed", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			build, err := s.CreateTask(ctx, "team", "Build", "", "medium", "lead")
			require.NoError(t, err)
			children := map[string]*Task{}
			for _, condition := range []string{"success", "failure", "termination"} {
				child, err := s.CreateTaskWithOpts(ctx, "team", condition, "", "medium", "lead", &CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: build.ID, Condition: condition}}})
				require.NoError(t, err)
				children[condition] = child
			}
			if outcome == "cancelled" {
				_, err = s.CancelTask(ctx, "team", build.ID, "builder", nil)
			} else {
				_, err = s.CompleteTaskWithArtifacts(ctx, "team", build.ID, "builder", nil, "failed", nil)
			}
			require.NoError(t, err)
			_, err = s.ResolveDownstreamTasks(ctx, "team", build.ID)
			require.NoError(t, err)
			for condition, child := range children {
				got, err := s.GetTask(ctx, "team", child.ID)
				require.NoError(t, err)
				want := "blocked"
				if condition == "termination" || condition == "failure" && outcome == "failed" {
					want = "pending"
				}
				require.Equal(t, want, got.Status, condition)
			}
			retry, err := s.CreateTaskWithOpts(ctx, "team", "Build retry", "", "medium", "lead", &CreateTaskOpts{Workflow: TaskWorkflow{RetryOf: build.ID}})
			require.NoError(t, err)
			// Creating a retry automatically reconnects the unstarted success
			// dependent, so it no longer remains pinned to the failed attempt.
			rewired, _ := s.GetTask(ctx, "team", children["success"].ID)
			require.Equal(t, "blocked", rewired.Status)
			require.Len(t, rewired.BlockedBy, 1)
			require.Equal(t, retry.ID, rewired.BlockedBy[0].TaskID)
			_, err = s.CompleteTask(ctx, "team", retry.ID, "builder", nil)
			require.NoError(t, err)
			_, err = s.ResolveDownstreamTasks(ctx, "team", retry.ID)
			require.NoError(t, err)
			old, _ := s.GetTask(ctx, "team", children["success"].ID)
			require.Equal(t, "pending", old.Status)
		})
	}
}

func TestRecoverRewiresDependentsPinnedToSupersededAttempt(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	qa, err := s.CreateTask(ctx, "team", "QA attempt", "", "medium", "qa")
	require.NoError(t, err)
	_, err = s.CompleteTaskWithArtifacts(ctx, "team", qa.ID, "qa", nil, "failed", []TaskArtifact{{Name: "candidate", Content: "8fa27d3"}})
	require.NoError(t, err)
	retry, err := s.CreateTaskWithOpts(ctx, "team", "QA retry", "", "medium", "qa", &CreateTaskOpts{Workflow: TaskWorkflow{RetryOf: qa.ID}})
	require.NoError(t, err)
	// Simulate a dependent created by an older server while the retry already existed.
	ml, err := s.CreateTaskWithOpts(ctx, "team", "ML", "", "medium", "ml", &CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: qa.ID}}})
	require.NoError(t, err)
	require.Equal(t, qa.ID, ml.BlockedBy[0].TaskID)
	require.NoError(t, s.RecoverTaskReadiness(ctx))
	rewired, err := s.GetTask(ctx, "team", ml.ID)
	require.NoError(t, err)
	require.Equal(t, retry.ID, rewired.BlockedBy[0].TaskID)
	require.Equal(t, "blocked", rewired.Status)
	_, err = s.CompleteTask(ctx, "team", retry.ID, "qa", nil)
	require.NoError(t, err)
	_, err = s.ResolveDownstreamTasks(ctx, "team", retry.ID)
	require.NoError(t, err)
	ready, err := s.GetTask(ctx, "team", ml.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", ready.Status)
}

func TestWorkflowValidationAndPersistence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "board.db")
	s, err := NewStore(path)
	require.NoError(t, err)
	build, err := s.CreateTask(ctx, "team", "Build", "", "medium", "lead")
	require.NoError(t, err)
	for _, condition := range []string{"typo", "success"} {
		dep := TaskDep{TaskID: build.ID, Condition: condition}
		if condition == "success" {
			dep.TaskID = 9999
		}
		_, err = s.CreateTaskWithOpts(ctx, "team", "Invalid", "", "medium", "lead", &CreateTaskOpts{Draft: true, BlockedBy: []TaskDep{dep}})
		require.Error(t, err)
	}
	tasks, err := s.ListTasks(ctx, "team")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	_, err = s.CompleteTaskWithArtifacts(ctx, "team", build.ID, "lead", nil, "success", []TaskArtifact{{Name: "report", Content: "immutable result"}})
	require.NoError(t, err)
	child, err := s.CreateTaskWithOpts(ctx, "team", "Test", "", "medium", "lead", &CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: build.ID, RequiredArtifacts: []string{"missing"}}}})
	require.NoError(t, err)
	require.Equal(t, "blocked", child.Status)
	require.NoError(t, s.Close())
	s, err = NewStore(path)
	require.NoError(t, err)
	defer s.Close()
	got, err := s.GetTask(ctx, "team", build.ID)
	require.NoError(t, err)
	require.Equal(t, "immutable result", got.Workflow.Artifacts[0].Content)
	require.NotEmpty(t, got.Workflow.Instructions)
}
