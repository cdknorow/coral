package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/board"
)

// reviewTeam is a board with an orchestrator ("lead"), an author ("worker") and two reviewers.
func reviewTeam(t *testing.T, project string, withOrchestrator bool) (base func(int64, string) string, h *BoardHandler, task *board.Task) {
	t.Helper()
	server, h := setupBoardTestServer(t)
	ctx := context.Background()
	if withOrchestrator {
		_, err := h.bs.Subscribe(ctx, project, "lead", "Orchestrator", "lead-session", nil, "all")
		require.NoError(t, err)
	}
	for _, s := range []struct{ id, title string }{{"worker", "Backend Dev"}, {"Security Reviewer", "Security Reviewer"}, {"QA Engineer", "QA Engineer"}} {
		_, err := h.bs.Subscribe(ctx, project, s.id, s.title, s.id+"-session", nil, "all")
		require.NoError(t, err)
	}
	creator := "lead"
	if !withOrchestrator {
		creator = "worker"
	}
	task, err := h.bs.CreateTask(ctx, project, "Build the thing", "", "high", creator, "worker")
	require.NoError(t, err)
	_, err = h.bs.ClaimTask(ctx, project, "worker", task.ID)
	require.NoError(t, err)
	base = func(id int64, action string) string {
		return fmt.Sprintf("%s/api/board/%s/tasks/%d/%s", server.URL, project, id, action)
	}
	return base, h, task
}

func postCode(t *testing.T, url string, body map[string]any) (int, map[string]any) {
	t.Helper()
	r := postJSON(t, url, body)
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return r.StatusCode, out
}

func submit(t *testing.T, base func(int64, string) string, id int64) {
	t.Helper()
	code, out := postCode(t, base(id, "submit-review"), map[string]any{"subscriber_id": "worker", "reason": "ready", "message": "built it", "artifacts": []board.TaskArtifact{{Name: "report", Content: "done"}}})
	require.Equal(t, http.StatusOK, code, "%v", out)
}

// (1) With no orchestrator on the team, submit-review completes the task.
func TestReviewFlowWithoutAnOrchestratorCompletes(t *testing.T) {
	base, h, task := reviewTeam(t, "solo", false)
	submit(t, base, task.ID)
	got, err := h.bs.GetTask(context.Background(), "solo", task.ID)
	require.NoError(t, err)
	assert.Equal(t, "completed", got.Status)
	assert.Equal(t, "worker", *got.CompletedBy)
	board := strings.TrimSuffix(base(0, "x"), "/tasks/0/x")
	require.Eventually(t, func() bool {
		return boardMessagesContain(t, board, fmt.Sprintf("[Task #%d completed by worker (completed without review: no orchestrator on the team)]", task.ID))
	}, 2*time.Second, 50*time.Millisecond, "the notice says it was not reviewed")
}

// (1)-(4) submit-review gives the task to the orchestrator and frees the author; only the
// orchestrator delegates; each reviewer claims their review task (it takes their slot) and
// approves; the last approval completes the task with the candidate's outcome.
func TestReviewFlowApproval(t *testing.T) {
	base, h, task := reviewTeam(t, "team", true)
	ctx := context.Background()
	submit(t, base, task.ID)
	got, _ := h.bs.GetTask(ctx, "team", task.ID)
	require.Equal(t, "review_pending", got.Status)
	require.Equal(t, "lead", *got.AssignedTo)
	require.Equal(t, "worker", got.Workflow.OriginalAuthor)
	next, err := h.bs.CreateTask(ctx, "team", "Next thing", "", "medium", "lead", "worker")
	require.NoError(t, err)
	_, err = h.bs.ClaimTask(ctx, "team", "worker", next.ID)
	require.NoError(t, err, "the author's slot is free at once")

	reviewers := []string{"Security Reviewer", "QA Engineer"}
	code, _ := postCode(t, base(task.ID, "review"), map[string]any{"subscriber_id": "worker", "reviewers": reviewers})
	assert.Equal(t, http.StatusForbidden, code, "only the orchestrator delegates")
	code, out := postCode(t, base(task.ID, "review"), map[string]any{"subscriber_id": "lead", "reviewers": []string{"worker"}})
	assert.Equal(t, http.StatusBadRequest, code, "the author can't review their own work: %v", out)
	code, _ = postCode(t, base(task.ID, "review"), map[string]any{"subscriber_id": "lead", "reviewers": []string{"Nobody"}})
	assert.Equal(t, http.StatusBadRequest, code, "only the board's subscribers")
	code, out = postCode(t, base(task.ID, "review"), map[string]any{"subscriber_id": "lead", "reviewers": reviewers})
	require.Equal(t, http.StatusOK, code, "%v", out)
	code, _ = postCode(t, base(task.ID, "review"), map[string]any{"subscriber_id": "lead", "reviewers": reviewers})
	assert.Equal(t, http.StatusOK, code, "naming the same reviewers again changes nothing (#3787)")

	got, _ = h.bs.GetTask(ctx, "team", task.ID)
	round := got.Workflow.ReviewRounds[0]
	require.Len(t, round.Reviewers, 2)
	secReview, qaReview := round.Reviewers[0].ReviewTaskID, round.Reviewers[1].ReviewTaskID
	rt, _ := h.bs.GetTask(ctx, "team", secReview)
	assert.Equal(t, "pending", rt.Status)
	assert.Equal(t, "Security Reviewer", *rt.AssignedTo)
	assert.Equal(t, task.ID, rt.Workflow.ReviewOf)

	// The review task is an ordinary task: claiming it takes the reviewer's one slot (a verdict
	// doesn't need the claim since #3787).
	other, err := h.bs.CreateTask(ctx, "team", "Other work", "", "medium", "lead", "QA Engineer")
	require.NoError(t, err)
	_, err = h.bs.ClaimTask(ctx, "team", "QA Engineer", other.ID)
	require.NoError(t, err)
	_, err = h.bs.ClaimTask(ctx, "team", "QA Engineer", qaReview)
	assert.ErrorContains(t, err, "complete your current task", "one task or review at a time")
	_, err = h.bs.CompleteTask(ctx, "team", other.ID, "QA Engineer", nil)
	require.NoError(t, err)

	_, err = h.bs.ClaimTask(ctx, "team", "Security Reviewer", secReview)
	require.NoError(t, err)
	code, _ = postCode(t, base(task.ID, "approve"), map[string]any{"subscriber_id": "lead"})
	assert.Equal(t, http.StatusForbidden, code, "only the delegated reviewers")
	code, out = postCode(t, base(secReview, "approve"), map[string]any{"subscriber_id": "Security Reviewer", "note": "fine"})
	require.Equal(t, http.StatusOK, code, "%v", out)
	assert.Equal(t, false, out["completed"])
	rt, _ = h.bs.GetTask(ctx, "team", secReview)
	assert.Equal(t, "completed", rt.Status, "the reviewer's slot is free")
	code, _ = postCode(t, base(task.ID, "approve"), map[string]any{"subscriber_id": "Security Reviewer"})
	assert.Equal(t, http.StatusBadRequest, code, "once per round")

	_, err = h.bs.ClaimTask(ctx, "team", "QA Engineer", qaReview)
	require.NoError(t, err)
	code, out = postCode(t, base(task.ID, "approve"), map[string]any{"subscriber_id": "QA Engineer"})
	require.Equal(t, http.StatusOK, code, "%v", out)
	assert.Equal(t, true, out["completed"])
	got, _ = h.bs.GetTask(ctx, "team", task.ID)
	assert.Equal(t, "completed", got.Status, "every reviewer approved: it completed itself")
	assert.Equal(t, "lead", *got.CompletedBy)
	assert.Equal(t, "success", got.Workflow.Outcome)
	assert.Equal(t, "approved", got.Workflow.ReviewRounds[0].Result)
	board := strings.TrimSuffix(base(0, "x"), "/tasks/0/x")
	require.Eventually(t, func() bool {
		return boardMessagesContain(t, board, fmt.Sprintf("[Task #%d completed by lead (approved by Security Reviewer, QA Engineer)]", task.ID))
	}, 2*time.Second, 50*time.Millisecond, "the completion notice names the approvers")
}

// (3) #3787 (operator): a rejection sends the task back to its author and the reviewers stay on
// it: the other reviewer's review task stays open, the author re-claims and resubmits, both review
// tasks reopen for round 2 with no orchestrator step, a verdict on round 1 is refused, and both
// approvals (claimed or not) complete the task.
func TestReviewFlowRejection(t *testing.T) {
	base, h, task := reviewTeam(t, "rej", true)
	ctx := context.Background()
	submit(t, base, task.ID)
	code, out := postCode(t, base(task.ID, "review"), map[string]any{"subscriber_id": "lead", "reviewers": []string{"Security Reviewer", "QA Engineer"}})
	require.Equal(t, http.StatusOK, code, "%v", out)
	got, _ := h.bs.GetTask(ctx, "rej", task.ID)
	secReview, qaReview := got.Workflow.ReviewRounds[0].Reviewers[0].ReviewTaskID, got.Workflow.ReviewRounds[0].Reviewers[1].ReviewTaskID
	_, err := h.bs.ClaimTask(ctx, "rej", "Security Reviewer", secReview)
	require.NoError(t, err)
	code, _ = postCode(t, base(task.ID, "reject"), map[string]any{"subscriber_id": "Security Reviewer"})
	assert.Equal(t, http.StatusBadRequest, code, "a rejection needs a note")
	code, out = postCode(t, base(task.ID, "reject"), map[string]any{"subscriber_id": "Security Reviewer", "note": "unsafe default", "round": 1})
	require.Equal(t, http.StatusOK, code, "%v", out)
	assert.Equal(t, true, out["rejected"])

	got, _ = h.bs.GetTask(ctx, "rej", task.ID)
	assert.Equal(t, "pending", got.Status)
	assert.Equal(t, "worker", *got.AssignedTo, "back to its author")
	assert.Equal(t, "rejected", got.Workflow.ReviewRounds[0].Result)
	assert.Equal(t, "unsafe default", got.Workflow.ReviewRounds[0].Reviewers[0].Note)
	qa, _ := h.bs.GetTask(ctx, "rej", qaReview)
	assert.Equal(t, "pending", qa.Status, "the other reviewer's review task stays open")
	assert.Equal(t, 1, qa.Workflow.ReviewRound, "a review task records its round (#3805)")
	code, out = postCode(t, base(task.ID, "approve"), map[string]any{"subscriber_id": "QA Engineer"})
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, out["error"], "back with worker", "no verdict while the author has it")

	// The author re-claims it, fixes it and submits again: round 2 opens by itself, for both.
	_, err = h.bs.ClaimTask(ctx, "rej", "worker", task.ID)
	require.NoError(t, err)
	submit(t, base, task.ID)
	got, _ = h.bs.GetTask(ctx, "rej", task.ID)
	require.Equal(t, "review_pending", got.Status)
	require.Len(t, got.Workflow.ReviewRounds, 2)
	r2 := got.Workflow.ReviewRounds[1]
	assert.Equal(t, 2, r2.Round)
	assert.Equal(t, "resubmitted", r2.DelegatedBy, "no orchestrator step")
	require.Len(t, r2.Reviewers, 2)
	assert.Equal(t, secReview, r2.Reviewers[0].ReviewTaskID, "the same review tasks")
	assert.Equal(t, qaReview, r2.Reviewers[1].ReviewTaskID)
	for _, rv := range r2.Reviewers {
		assert.Equal(t, "pending", rv.State)
		rt, _ := h.bs.GetTask(ctx, "rej", rv.ReviewTaskID)
		assert.Equal(t, "pending", rt.Status, "%s's review task reopens", rv.Reviewer)
		assert.Contains(t, *rt.Body, "--round 2", "it names the new round")
		assert.Equal(t, 2, rt.Workflow.ReviewRound, "and records it (#3805)")
	}

	// A verdict on round 1 is refused; any time it is in review, each reviewer's verdict counts.
	code, out = postCode(t, base(task.ID, "approve"), map[string]any{"subscriber_id": "QA Engineer", "round": 1})
	assert.Equal(t, http.StatusConflict, code)
	assert.Contains(t, out["error"], "round 2")
	code, out = postCode(t, base(secReview, "approve"), map[string]any{"subscriber_id": "Security Reviewer", "round": 2})
	require.Equal(t, http.StatusOK, code, "%v", out)
	assert.Equal(t, false, out["completed"])
	code, out = postCode(t, base(task.ID, "approve"), map[string]any{"subscriber_id": "QA Engineer", "round": 2})
	require.Equal(t, http.StatusOK, code, "%v", out)
	assert.Equal(t, true, out["completed"])
	got, _ = h.bs.GetTask(ctx, "rej", task.ID)
	assert.Equal(t, "completed", got.Status)
	assert.Equal(t, "approved", got.Workflow.ReviewRounds[1].Result)
}

// #3787: the orchestrator changes the reviewers of an open round: a dropped one's review task is
// skipped, a new one gets a review task, a kept one keeps theirs and their verdict.
func TestReviewFlowOrchestratorChangesReviewers(t *testing.T) {
	base, h, task := reviewTeam(t, "chg", true)
	ctx := context.Background()
	submit(t, base, task.ID)
	code, out := postCode(t, base(task.ID, "review"), map[string]any{"subscriber_id": "lead", "reviewers": []string{"Security Reviewer"}})
	require.Equal(t, http.StatusOK, code, "%v", out)
	got, _ := h.bs.GetTask(ctx, "chg", task.ID)
	sec := got.Workflow.ReviewRounds[0].Reviewers[0].ReviewTaskID
	code, out = postCode(t, base(task.ID, "approve"), map[string]any{"subscriber_id": "Security Reviewer"})
	require.Equal(t, http.StatusOK, code, "%v", out)
	assert.Equal(t, true, out["completed"], "the only reviewer approved")

	// A second task: change the open round's reviewers.
	next, err := h.bs.CreateTask(ctx, "chg", "Second", "", "high", "lead", "worker")
	require.NoError(t, err)
	_, err = h.bs.ClaimTask(ctx, "chg", "worker", next.ID)
	require.NoError(t, err)
	submit(t, base, next.ID)
	code, out = postCode(t, base(next.ID, "review"), map[string]any{"subscriber_id": "lead", "reviewers": []string{"Security Reviewer", "QA Engineer"}})
	require.Equal(t, http.StatusOK, code, "%v", out)
	code, out = postCode(t, base(next.ID, "approve"), map[string]any{"subscriber_id": "Security Reviewer"})
	require.Equal(t, http.StatusOK, code, "%v", out)
	got, _ = h.bs.GetTask(ctx, "chg", next.ID)
	qa := got.Workflow.ReviewRounds[0].Reviewers[1].ReviewTaskID
	code, out = postCode(t, base(next.ID, "review"), map[string]any{"subscriber_id": "lead", "reviewers": []string{"Security Reviewer"}})
	require.Equal(t, http.StatusOK, code, "%v", out)
	got, _ = h.bs.GetTask(ctx, "chg", next.ID)
	require.Len(t, got.Workflow.ReviewRounds, 1, "the same round")
	require.Len(t, got.Workflow.ReviewRounds[0].Reviewers, 1)
	assert.Equal(t, "approved", got.Workflow.ReviewRounds[0].Reviewers[0].State, "a kept reviewer keeps their verdict")
	qt, _ := h.bs.GetTask(ctx, "chg", qa)
	assert.Equal(t, "skipped", qt.Status, "a dropped reviewer's review task ends")
	_ = sec
}

// (5) The health check shows open reviews and their age.
func TestReviewFlowOpenReviewsInTheHealthCheck(t *testing.T) {
	base, h, task := reviewTeam(t, "health", true)
	ctx := context.Background()
	submit(t, base, task.ID)
	tasks, err := h.bs.ListTasks(ctx, "health")
	require.NoError(t, err)
	lines := strings.Join(openReviewLines(tasks, time.Now().Add(90*time.Minute)), "\n")
	assert.Contains(t, lines, fmt.Sprintf("#%d is in review with lead, not yet completed or delegated", task.ID))
	code, _ := postCode(t, base(task.ID, "review"), map[string]any{"subscriber_id": "lead", "reviewers": []string{"QA Engineer"}})
	require.Equal(t, http.StatusOK, code)
	tasks, err = h.bs.ListTasks(ctx, "health")
	require.NoError(t, err)
	lines = strings.Join(openReviewLines(tasks, time.Now().Add(90*time.Minute)), "\n")
	assert.Contains(t, lines, "QA Engineer has review #")
	assert.Contains(t, lines, "1h30m0s")
	assert.NotContains(t, lines, "not yet completed or delegated", "delegated now")
}

// #3889: a rejected task is back with its author, pending. An author already busy with another
// task (the claim is refused: one task at a time) resubmits the fix without claiming it, and its
// reviewers' round reopens as after a re-claim. Only the author may, and only after a rejection.
func TestReviewFlowResubmitWithoutClaiming(t *testing.T) {
	base, h, task := reviewTeam(t, "busy", true)
	ctx := context.Background()
	submit(t, base, task.ID)
	code, out := postCode(t, base(task.ID, "review"), map[string]any{"subscriber_id": "lead", "reviewers": []string{"Security Reviewer", "QA Engineer"}})
	require.Equal(t, http.StatusOK, code, "%v", out)
	got, _ := h.bs.GetTask(ctx, "busy", task.ID)
	secReview := got.Workflow.ReviewRounds[0].Reviewers[0].ReviewTaskID
	_, err := h.bs.ClaimTask(ctx, "busy", "Security Reviewer", secReview)
	require.NoError(t, err)
	code, out = postCode(t, base(task.ID, "reject"), map[string]any{"subscriber_id": "Security Reviewer", "note": "unsafe default", "round": 1})
	require.Equal(t, http.StatusOK, code, "%v", out)

	// Meanwhile the author took task B, so claiming the rejected task back is refused.
	other, err := h.bs.CreateTask(ctx, "busy", "Task B", "", "medium", "lead", "worker")
	require.NoError(t, err)
	_, err = h.bs.ClaimTask(ctx, "busy", "worker", other.ID)
	require.NoError(t, err)
	_, err = h.bs.ClaimTask(ctx, "busy", "worker", task.ID)
	require.Error(t, err, "one task in progress at a time")

	// Someone else can't resubmit the author's task.
	code, _ = postCode(t, base(task.ID, "submit-review"), map[string]any{"subscriber_id": "QA Engineer", "reason": "ready"})
	assert.Equal(t, http.StatusBadRequest, code)

	// The author resubmits it unclaimed: in review again, round 2 for the same reviewers.
	submit(t, base, task.ID)
	got, _ = h.bs.GetTask(ctx, "busy", task.ID)
	require.Equal(t, "review_pending", got.Status)
	require.Len(t, got.Workflow.ReviewRounds, 2)
	assert.Equal(t, "resubmitted", got.Workflow.ReviewRounds[1].DelegatedBy)
	for _, rv := range got.Workflow.ReviewRounds[1].Reviewers {
		rt, _ := h.bs.GetTask(ctx, "busy", rv.ReviewTaskID)
		assert.Equal(t, "pending", rt.Status, "%s's review task reopens", rv.Reviewer)
	}
	b, _ := h.bs.GetTask(ctx, "busy", other.ID)
	assert.Equal(t, "in_progress", b.Status, "task B is untouched")

	// A pending task that was never reviewed still needs a claim first.
	fresh, err := h.bs.CreateTask(ctx, "busy", "Task C", "", "medium", "lead", "worker")
	require.NoError(t, err)
	code, out = postCode(t, base(fresh.ID, "submit-review"), map[string]any{"subscriber_id": "worker", "reason": "ready"})
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, out["error"], "in-progress")
}

// #5159: the one-task limit is for claiming. A reviewer who is busy with another task still gives their verdict (by the task under review
// or by their review task) and completes a plain task assigned to them (a "review only" request that is not a review task) without claiming it.
func TestReviewVerdictAndPlainTaskNeedNoClaimWhileBusy(t *testing.T) {
	base, h, task := reviewTeam(t, "busy", true)
	ctx := context.Background()
	submit(t, base, task.ID)
	reviewers := []string{"Security Reviewer", "QA Engineer"}
	code, out := postCode(t, base(task.ID, "review"), map[string]any{"subscriber_id": "lead", "reviewers": reviewers})
	require.Equal(t, http.StatusOK, code, "%v", out)
	got, err := h.bs.GetTask(ctx, "busy", task.ID)
	require.NoError(t, err)
	secReview, qaReview := got.Workflow.ReviewRounds[0].Reviewers[0].ReviewTaskID, got.Workflow.ReviewRounds[0].Reviewers[1].ReviewTaskID

	for _, who := range reviewers {
		other, err := h.bs.CreateTask(ctx, "busy", "Build for "+who, "", "medium", "lead", who)
		require.NoError(t, err)
		_, err = h.bs.ClaimTask(ctx, "busy", who, other.ID)
		require.NoError(t, err)
	}
	// Claiming is refused while busy, as before.
	_, err = h.bs.ClaimTask(ctx, "busy", "Security Reviewer", secReview)
	assert.ErrorContains(t, err, "complete your current task")

	// A plain task assigned to a busy agent completes without being claimed.
	plain, err := h.bs.CreateTask(ctx, "busy", "Review only: a patch", "", "high", "lead", "Security Reviewer")
	require.NoError(t, err)
	code, out = postCode(t, base(plain.ID, "complete"), map[string]any{"subscriber_id": "Security Reviewer", "message": "approved, no concerns"})
	require.Equal(t, http.StatusOK, code, "%v", out)
	done, err := h.bs.GetTask(ctx, "busy", plain.ID)
	require.NoError(t, err)
	assert.Equal(t, "completed", done.Status)

	// Verdicts need no claim: one by the task under review, one by the reviewer's own review task.
	code, out = postCode(t, base(task.ID, "approve"), map[string]any{"subscriber_id": "Security Reviewer"})
	require.Equal(t, http.StatusOK, code, "%v", out)
	assert.Equal(t, false, out["completed"])
	code, out = postCode(t, base(qaReview, "approve"), map[string]any{"subscriber_id": "QA Engineer"})
	require.Equal(t, http.StatusOK, code, "%v", out)
	assert.Equal(t, true, out["completed"])
	final, err := h.bs.GetTask(ctx, "busy", task.ID)
	require.NoError(t, err)
	assert.Equal(t, "completed", final.Status)
}
