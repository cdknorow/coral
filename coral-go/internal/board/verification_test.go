package board

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type verEnv struct {
	t   *testing.T
	s   *Store
	ctx context.Context
	ids map[string]int64
}

func newVerEnv(t *testing.T) *verEnv {
	t.Helper()
	t.Setenv(completionChecksFeatureEnv, "true") // gates are an opt-in feature
	s := testStore(t)
	ctx := context.Background()
	for name, role := range map[string]string{"lead": "Orchestrator", "worker": "Developer", "rev1": "Security", "rev2": "QA"} {
		_, err := s.Subscribe(ctx, "team", name, role, name+"-session", nil, "all")
		require.NoError(t, err)
	}
	return &verEnv{t: t, s: s, ctx: ctx, ids: map[string]int64{}}
}

// done makes a task, has the worker claim it and complete it directly (the orchestrator-style completion, no review).
func (e *verEnv) done(name, outcome string, artifacts []TaskArtifact, opts *CreateTaskOpts) int64 {
	e.t.Helper()
	task, err := e.s.CreateTaskWithOpts(e.ctx, "team", name, "", "medium", "lead", opts, "worker")
	require.NoError(e.t, err)
	_, err = e.s.ClaimTask(e.ctx, "team", "worker", task.ID)
	require.NoError(e.t, err)
	_, err = e.s.CompleteTaskWithArtifactsAtRevisionAndCandidate(e.ctx, "team", task.ID, "lead", nil, outcome, artifacts, nil, "rev-1")
	require.NoError(e.t, err)
	e.ids[name] = task.ID
	return task.ID
}

// reviewed takes a task through the review flow: submit, delegate, then each reviewer approves.
func (e *verEnv) reviewed(name string, artifacts []TaskArtifact, reviewers ...string) int64 {
	e.t.Helper()
	task, err := e.s.CreateTask(e.ctx, "team", name, "", "medium", "lead", "worker")
	require.NoError(e.t, err)
	_, err = e.s.ClaimTask(e.ctx, "team", "worker", task.ID)
	require.NoError(e.t, err)
	_, err = e.s.SubmitCompletionReview(e.ctx, "team", task.ID, "worker", "built", "success", "needs review", artifacts)
	require.NoError(e.t, err)
	_, err = e.s.ReleaseCompletionReview(e.ctx, "team", task.ID, "lead", "review")
	require.NoError(e.t, err)
	_, _, err = e.s.DelegateReview(e.ctx, "team", task.ID, "lead", reviewers)
	require.NoError(e.t, err)
	for _, r := range reviewers {
		v, err := e.s.ApproveReview(e.ctx, "team", task.ID, r, "ok", 0)
		require.NoError(e.t, err)
		if v.Complete {
			e.finish(v)
		}
	}
	e.ids[name] = task.ID
	return task.ID
}

// finish completes an approved task the way the approve handler does: as the orchestrator, with the submitted candidate.
func (e *verEnv) finish(v *ReviewVerdict) {
	e.t.Helper()
	cr := v.Task.Workflow.CompletionReview
	require.NotNil(e.t, cr)
	msg := cr.Message
	_, err := e.s.CompleteTaskWithArtifactsAtRevisionAndCandidate(e.ctx, "team", v.Task.ID, "lead", &msg, cr.ProposedOutcome, cr.Artifacts, &v.Task.Revision, cr.CandidateRevision)
	require.NoError(e.t, err)
}

func (e *verEnv) task(id int64) *Task {
	e.t.Helper()
	task, err := e.s.GetTask(e.ctx, "team", id)
	require.NoError(e.t, err)
	return task
}

func (e *verEnv) editWorkflow(id int64, f func(w *TaskWorkflow)) {
	e.t.Helper()
	w, err := loadWorkflow(e.ctx, e.s.db, id)
	require.NoError(e.t, err)
	f(&w)
	require.NoError(e.t, saveWorkflow(e.ctx, e.s.db, id, w))
}

var reportGate = []CompletionGate{{Type: "report", Artifact: "report"}}

func TestVerificationStates(t *testing.T) {
	e := newVerEnv(t)
	art := []TaskArtifact{{Name: "report", Content: "what was done"}}

	plain := e.done("plain", "success", nil, nil)
	evidence := e.done("evidence", "success", art, nil)
	failed := e.done("failed", "failed", art, nil)
	reviewed := e.reviewed("reviewed", art, "rev1")
	two := e.reviewed("two", nil, "rev1", "rev2")
	gated := e.done("gated", "success", art, &CreateTaskOpts{Workflow: TaskWorkflow{CompletionGates: reportGate}})
	pending, err := e.s.CreateTask(e.ctx, "team", "pending", "", "medium", "lead", "worker")
	require.NoError(t, err)

	v := e.task(plain).Verification
	require.NotNil(t, v)
	assert.Equal(t, VerificationUnverified, v.State)
	assert.Equal(t, []string{}, v.By)
	assert.Equal(t, *e.task(plain).CompletedAt, v.At)

	v = e.task(evidence).Verification
	require.NotNil(t, v)
	assert.Equal(t, VerificationEvidence, v.State)
	assert.Empty(t, v.By)

	assert.Nil(t, e.task(failed).Verification, "a failed task has no state")
	assert.Nil(t, e.task(pending.ID).Verification, "an unfinished task has no state")

	v = e.task(reviewed).Verification
	require.NotNil(t, v)
	assert.Equal(t, VerificationVerified, v.State)
	assert.Equal(t, []string{"rev1"}, v.By)
	assert.NotEmpty(t, v.At)

	v = e.task(two).Verification
	require.NotNil(t, v)
	assert.Equal(t, VerificationVerified, v.State, "an approved review with no artifacts still verifies")
	assert.Equal(t, []string{"rev1", "rev2"}, v.By)

	v = e.task(gated).Verification
	require.NotNil(t, v)
	assert.Equal(t, VerificationVerified, v.State, "a passed completion gate counts")
	assert.Equal(t, []string{"gate:report"}, v.By)

	// A review task is the verdict record of someone else's work, not a work product: no state.
	rounds := e.task(reviewed).Workflow.ReviewRounds
	require.Len(t, rounds, 1)
	rt := e.task(rounds[0].Reviewers[0].ReviewTaskID)
	assert.Equal(t, "completed", rt.Status)
	assert.Nil(t, rt.Verification)

	// A skipped (cancelled) task has none either.
	skipped, err := e.s.CreateTask(e.ctx, "team", "skipped", "", "medium", "lead", "worker")
	require.NoError(t, err)
	_, err = e.s.db.Exec("UPDATE board_tasks SET status = 'skipped' WHERE id = ?", skipped.ID)
	require.NoError(t, err)
	assert.Nil(t, e.task(skipped.ID).Verification)
}

// The author approving their own work never counts, however the record got there.
func TestVerificationSelfApprovalDoesNotCount(t *testing.T) {
	e := newVerEnv(t)
	art := []TaskArtifact{{Name: "report", Content: "x"}}
	id := e.reviewed("own", art, "rev1")
	require.Equal(t, VerificationVerified, e.task(id).Verification.State)

	// Only the author approved the approved round (a hand-written record; the flow refuses to delegate to the author).
	e.editWorkflow(id, func(w *TaskWorkflow) {
		w.ReviewRounds[0].Reviewers = []ReviewerState{{Reviewer: w.OriginalAuthor, State: "approved", At: "2026-10-18T00:00:00Z"}}
	})
	v := e.task(id).Verification
	assert.Equal(t, VerificationEvidence, v.State, "the author's own approval is not a check (it still has artifacts)")
	assert.Empty(t, v.By)
	// the same in a different case, and with the reviewer list holding a real reviewer who has not approved
	e.editWorkflow(id, func(w *TaskWorkflow) {
		w.ReviewRounds[0].Reviewers = []ReviewerState{{Reviewer: " " + upperFirst(w.OriginalAuthor) + " ", State: "approved"}, {Reviewer: "rev1", State: "pending"}}
	})
	assert.Equal(t, VerificationEvidence, e.task(id).Verification.State)
	// a round that was not approved does not count, whoever approved in it
	e.editWorkflow(id, func(w *TaskWorkflow) {
		w.ReviewRounds[0].Result = "rejected"
		w.ReviewRounds[0].Reviewers = []ReviewerState{{Reviewer: "rev1", State: "approved"}}
	})
	assert.Equal(t, VerificationEvidence, e.task(id).Verification.State)
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return string(s[0]-32) + s[1:]
}

// A rejected round followed by an approved one is verified by the approving round's reviewers only.
func TestVerificationRejectedThenApproved(t *testing.T) {
	e := newVerEnv(t)
	art := []TaskArtifact{{Name: "report", Content: "x"}}
	task, err := e.s.CreateTask(e.ctx, "team", "redo", "", "medium", "lead", "worker")
	require.NoError(t, err)
	_, err = e.s.ClaimTask(e.ctx, "team", "worker", task.ID)
	require.NoError(t, err)
	_, err = e.s.SubmitCompletionReview(e.ctx, "team", task.ID, "worker", "v1", "success", "r", art)
	require.NoError(t, err)
	_, err = e.s.ReleaseCompletionReview(e.ctx, "team", task.ID, "lead", "review")
	require.NoError(t, err)
	_, _, err = e.s.DelegateReview(e.ctx, "team", task.ID, "lead", []string{"rev1"})
	require.NoError(t, err)
	_, err = e.s.RejectReview(e.ctx, "team", task.ID, "rev1", "not yet", 0)
	require.NoError(t, err)
	assert.Nil(t, e.task(task.ID).Verification, "back with its author: not completed")
	_, err = e.s.ClaimTask(e.ctx, "team", "worker", task.ID)
	require.NoError(t, err)
	_, err = e.s.SubmitCompletionReview(e.ctx, "team", task.ID, "worker", "v2", "success", "r", art)
	require.NoError(t, err)
	_, err = e.s.ReleaseCompletionReview(e.ctx, "team", task.ID, "lead", "review")
	require.NoError(t, err)
	_, _, err = e.s.DelegateReview(e.ctx, "team", task.ID, "lead", []string{"rev2"})
	require.NoError(t, err)
	verdict, err := e.s.ApproveReview(e.ctx, "team", task.ID, "rev2", "ok", 0)
	require.NoError(t, err)
	require.True(t, verdict.Complete)
	e.finish(verdict)
	v := e.task(task.ID).Verification
	require.NotNil(t, v)
	assert.Equal(t, VerificationVerified, v.State)
	assert.Equal(t, []string{"rev2"}, v.By)
}

func TestVerificationGateRules(t *testing.T) {
	e := newVerEnv(t)
	art := []TaskArtifact{{Name: "report", Content: "x"}}
	id := e.done("g", "success", art, &CreateTaskOpts{Workflow: TaskWorkflow{CompletionGates: reportGate}})
	require.Equal(t, VerificationVerified, e.task(id).Verification.State)

	cases := map[string]func(w *TaskWorkflow){
		"a result that failed":          func(w *TaskWorkflow) { w.GateResults[0].Passed = false },
		"a result for another revision": func(w *TaskWorkflow) { w.GateResults[0].CandidateRevision = "rev-other" },
		"a gate with no result":         func(w *TaskWorkflow) { w.GateResults = nil },
		"fewer results than registered gates": func(w *TaskWorkflow) {
			w.CompletionGates = append(w.CompletionGates, CompletionGate{Type: "report", Artifact: "r2"})
		},
		"no registered gates (stray results)": func(w *TaskWorkflow) { w.CompletionGates = nil },
		"one of two results failed": func(w *TaskWorkflow) {
			w.CompletionGates = append(w.CompletionGates, w.CompletionGates[0])
			w.GateResults = append(w.GateResults, w.GateResults[0])
			w.GateResults[1].Passed = false
		},
	}
	for name, mutate := range cases {
		e.editWorkflow(id, func(w *TaskWorkflow) { // start from the passing record each time
			*w = TaskWorkflow{Instructions: w.Instructions, Outcome: "success", Artifacts: art, CandidateRevision: "rev-1",
				CompletionGates: reportGate, GateResults: []CompletionGateResult{{Type: "report", Passed: true, CandidateRevision: "rev-1", CheckedAt: "2026-10-18T00:00:00Z"}}}
			mutate(w)
		})
		v := e.task(id).Verification
		assert.Equal(t, VerificationEvidence, v.State, name)
	}
}

// The filter (SQL over the stored workflow) and the computed state (Go) agree for every kind of task, through the task query.
func TestVerificationFilterAgreesWithTheComputedState(t *testing.T) {
	e := newVerEnv(t)
	art := []TaskArtifact{{Name: "report", Content: "x"}}
	e.done("plain", "success", nil, nil)
	e.done("evidence", "success", art, nil)
	e.done("failed", "failed", art, nil)
	e.reviewed("reviewed", art, "rev1")
	e.reviewed("two", nil, "rev1", "rev2")
	e.done("gated", "success", art, &CreateTaskOpts{Workflow: TaskWorkflow{CompletionGates: reportGate}})
	own := e.reviewed("own", art, "rev1")
	e.editWorkflow(own, func(w *TaskWorkflow) {
		w.ReviewRounds[0].Reviewers = []ReviewerState{{Reviewer: w.OriginalAuthor, State: "approved"}}
	})
	pendRev := e.reviewed("pendrev", art, "rev1")
	e.editWorkflow(pendRev, func(w *TaskWorkflow) { // an approved round whose only reviewer has not approved (a hand-written record)
		w.ReviewRounds[0].Reviewers = []ReviewerState{{Reviewer: "rev1", State: "pending"}}
	})
	badGate := e.done("badgate", "success", art, &CreateTaskOpts{Workflow: TaskWorkflow{CompletionGates: reportGate}})
	e.editWorkflow(badGate, func(w *TaskWorkflow) { w.GateResults[0].CandidateRevision = "rev-other" })
	_, err := e.s.CreateTask(e.ctx, "team", "pending", "", "medium", "lead", "worker")
	require.NoError(t, err)

	all, err := e.s.ListAllTasks(e.ctx, 100)
	require.NoError(t, err)
	want := map[string][]int64{}
	for _, task := range all {
		if v := task.Verification; v != nil {
			want[v.State] = append(want[v.State], task.ID)
		}
	}
	for _, ids := range want {
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	}
	require.NotEmpty(t, want[VerificationVerified])
	require.NotEmpty(t, want[VerificationEvidence])
	require.NotEmpty(t, want[VerificationUnverified])

	idsOf := func(q TaskQueryParams) []int64 {
		parsed, err := ParseTaskQuery(q)
		require.NoError(t, err)
		parsed.Fields = []string{"id"}
		res, err := e.s.QueryTasks(e.ctx, parsed)
		require.NoError(t, err)
		var ids []int64
		for _, m := range res.Tasks {
			ids = append(ids, m["id"].(int64))
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		return ids
	}
	for _, state := range []string{VerificationVerified, VerificationEvidence, VerificationUnverified} {
		assert.Equal(t, want[state], idsOf(TaskQueryParams{Verification: []string{state}}), state)
	}
	both := append(append([]int64{}, want[VerificationVerified]...), want[VerificationEvidence]...)
	sort.Slice(both, func(i, j int) bool { return both[i] < both[j] })
	assert.Equal(t, both, idsOf(TaskQueryParams{Verification: []string{"verified,evidence"}}))
	// combined with another filter
	assert.Empty(t, idsOf(TaskQueryParams{Verification: []string{"verified"}, Status: []string{"pending"}}))
	assert.Equal(t, want[VerificationVerified], idsOf(TaskQueryParams{Verification: []string{"verified"}, Status: []string{"completed"}}))
	assert.Equal(t, []int64{e.ids["reviewed"]}, idsOf(TaskQueryParams{Verification: []string{"verified"}, AssignedTo: []string{"worker"}, CreatedBy: []string{"lead"}, Sort: []string{"created_at"}, Limit: []string{"100"}})[:1:1])

	// the projection carries the same state and who backs it
	parsed, err := ParseTaskQuery(TaskQueryParams{Fields: []string{"id,verification"}, Verification: []string{"verified"}})
	require.NoError(t, err)
	res, err := e.s.QueryTasks(e.ctx, parsed)
	require.NoError(t, err)
	byID := map[int64]*Verification{}
	for _, task := range all {
		byID[task.ID] = task.Verification
	}
	require.NotEmpty(t, res.Tasks)
	for _, m := range res.Tasks {
		assert.Equal(t, byID[m["id"].(int64)], m["verification"])
	}
	// a task with no state is null in the projection
	parsed, err = ParseTaskQuery(TaskQueryParams{Fields: []string{"id,verification"}, Status: []string{"pending"}})
	require.NoError(t, err)
	res, err = e.s.QueryTasks(e.ctx, parsed)
	require.NoError(t, err)
	require.Len(t, res.Tasks, 1)
	assert.Nil(t, res.Tasks[0]["verification"])
	_, hasKey := res.Tasks[0]["verification"]
	assert.True(t, hasKey)
}
