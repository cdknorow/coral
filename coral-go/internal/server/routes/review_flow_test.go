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
	assert.Equal(t, http.StatusBadRequest, code, "a round is already open")

	got, _ = h.bs.GetTask(ctx, "team", task.ID)
	round := got.Workflow.ReviewRounds[0]
	require.Len(t, round.Reviewers, 2)
	secReview, qaReview := round.Reviewers[0].ReviewTaskID, round.Reviewers[1].ReviewTaskID
	rt, _ := h.bs.GetTask(ctx, "team", secReview)
	assert.Equal(t, "pending", rt.Status)
	assert.Equal(t, "Security Reviewer", *rt.AssignedTo)
	assert.Equal(t, task.ID, rt.Workflow.ReviewOf)

	code, out = postCode(t, base(task.ID, "approve"), map[string]any{"subscriber_id": "Security Reviewer"})
	assert.Equal(t, http.StatusBadRequest, code, "claim the review task first: %v", out)
	// The review task is an ordinary task: it takes the reviewer's one slot.
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

// (3) A rejection sends the task back to its author (pending, with the note), ends the round's
// other review, and the author can submit again for a new round.
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
	code, out = postCode(t, base(task.ID, "reject"), map[string]any{"subscriber_id": "Security Reviewer", "note": "unsafe default"})
	require.Equal(t, http.StatusOK, code, "%v", out)
	assert.Equal(t, true, out["rejected"])

	got, _ = h.bs.GetTask(ctx, "rej", task.ID)
	assert.Equal(t, "pending", got.Status)
	assert.Equal(t, "worker", *got.AssignedTo, "back to its author")
	assert.Equal(t, "rejected", got.Workflow.ReviewRounds[0].Result)
	assert.Equal(t, "unsafe default", got.Workflow.ReviewRounds[0].Reviewers[0].Note)
	qa, _ := h.bs.GetTask(ctx, "rej", qaReview)
	assert.Equal(t, "skipped", qa.Status, "the round's other review ends")

	// The author fixes it and submits again: round 2.
	_, err = h.bs.ClaimTask(ctx, "rej", "worker", task.ID)
	require.NoError(t, err)
	submit(t, base, task.ID)
	code, out = postCode(t, base(task.ID, "review"), map[string]any{"subscriber_id": "lead", "reviewers": []string{"QA Engineer"}})
	require.Equal(t, http.StatusOK, code, "%v", out)
	got, _ = h.bs.GetTask(ctx, "rej", task.ID)
	require.Len(t, got.Workflow.ReviewRounds, 2)
	assert.Equal(t, 2, got.Workflow.ReviewRounds[1].Round)
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
