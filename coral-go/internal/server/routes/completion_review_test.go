package routes

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/cdknorow/coral/internal/board"
	"github.com/stretchr/testify/require"
)

func TestCompletionReviewAPI(t *testing.T) {
	server, h := setupBoardTestServer(t)
	ctx := context.Background()
	_, err := h.bs.Subscribe(ctx, "review", "lead", "Orchestrator", "lead-session", nil, nil, "all")
	require.NoError(t, err)
	up, err := h.bs.CreateTask(ctx, "review", "Build", "", "medium", "lead", "worker")
	require.NoError(t, err)
	_, err = h.bs.ClaimTask(ctx, "review", "worker", up.ID)
	require.NoError(t, err)
	base := fmt.Sprintf("%s/api/board/review/tasks/%d/", server.URL, up.ID)
	call := func(action string, body map[string]any, want int) {
		t.Helper()
		r := postJSON(t, base+action, body)
		defer r.Body.Close()
		require.Equal(t, want, r.StatusCode)
	}
	call("release-review", map[string]any{"subscriber_id": "lead", "reason": "free capacity"}, 400)
	call("submit-review", map[string]any{"subscriber_id": "worker", "reason": "external review pending", "artifacts": []board.TaskArtifact{{Name: "build", Content: "candidate"}}}, 200)
	call("release-review", map[string]any{"subscriber_id": "pretend-orchestrator", "reason": "free capacity"}, http.StatusForbidden)
	call("release-review", map[string]any{"subscriber_id": "worker", "reason": "free capacity"}, http.StatusForbidden)
	call("release-review", map[string]any{"subscriber_id": "lead", "reason": "independent work"}, 200)
	call("release-review", map[string]any{"subscriber_id": "lead", "reason": "duplicate"}, 400)
	call("complete", map[string]any{"subscriber_id": "worker"}, 400)
	task, err := h.bs.GetTask(ctx, "review", up.ID)
	require.NoError(t, err)
	require.Equal(t, "review_pending", task.Status)
	require.Empty(t, task.Workflow.Outcome)
	require.Equal(t, "candidate", task.Workflow.CompletionReview.Artifacts[0].Content)
	call("complete", map[string]any{"subscriber_id": "lead", "outcome": "failed", "message": "review declined"}, 200)
}

func TestCompletionReviewCannotBypassGates(t *testing.T) {
	server, h := setupBoardTestServer(t)
	h.bs.SetCompletionChecksEnabled(true)
	ctx := context.Background()
	_, err := h.bs.Subscribe(ctx, "gated-review", "lead", "Orchestrator", "lead-session", nil, nil, "all")
	require.NoError(t, err)
	task, err := h.bs.CreateTaskWithOpts(ctx, "gated-review", "Gated", "", "medium", "lead", &board.CreateTaskOpts{Workflow: board.TaskWorkflow{CompletionGates: []board.CompletionGate{{Type: "test_evidence", Artifact: "tests"}}}}, "worker")
	require.NoError(t, err)
	_, err = h.bs.ClaimTask(ctx, "gated-review", "worker", task.ID)
	require.NoError(t, err)
	base := fmt.Sprintf("%s/api/board/gated-review/tasks/%d/", server.URL, task.ID)
	r := postJSON(t, base+"submit-review", map[string]any{"subscriber_id": "worker", "reason": "candidate", "candidate_revision": "rev-1", "artifacts": []board.TaskArtifact{{Name: "tests", Content: "exit 0"}}})
	defer r.Body.Close()
	require.Equal(t, http.StatusBadRequest, r.StatusCode)
	got, err := h.bs.GetTask(ctx, "gated-review", task.ID)
	require.NoError(t, err)
	require.Equal(t, "in_progress", got.Status)
}

func TestCompletionReviewAllowsStoredGatesWhenFeatureDisabled(t *testing.T) {
	server, h := setupBoardTestServer(t)
	ctx := context.Background()
	_, err := h.bs.Subscribe(ctx, "disabled-review", "lead", "Orchestrator", "lead-session", nil, nil, "all")
	require.NoError(t, err)
	h.bs.SetCompletionChecksEnabled(true)
	task, err := h.bs.CreateTaskWithOpts(ctx, "disabled-review", "Gated", "", "medium", "lead", &board.CreateTaskOpts{Workflow: board.TaskWorkflow{CompletionGates: []board.CompletionGate{
		{Type: "report", Artifact: "report"},
		{Type: "test_evidence", Artifact: "tests"},
		{Type: "landed_revision", Artifact: "landing", Remote: "origin", Branch: "release"},
		{Type: "registered_check", CheckID: "go_test"},
	}}}, "worker")
	require.NoError(t, err)
	_, err = h.bs.ClaimTask(ctx, "disabled-review", "worker", task.ID)
	require.NoError(t, err)
	h.bs.SetCompletionChecksEnabled(false)
	base := fmt.Sprintf("%s/api/board/disabled-review/tasks/%d/", server.URL, task.ID)
	r := postJSON(t, base+"submit-review", map[string]any{"subscriber_id": "worker", "reason": "candidate", "candidate_revision": "rev-1"})
	defer r.Body.Close()
	require.Equal(t, http.StatusOK, r.StatusCode)
	got, err := h.bs.GetTask(ctx, "disabled-review", task.ID)
	require.NoError(t, err)
	require.Equal(t, "in_progress", got.Status)
	require.Len(t, got.Workflow.CompletionReview.GateResults, 4)
	for _, result := range got.Workflow.CompletionReview.GateResults {
		require.False(t, result.Passed)
		require.Contains(t, result.Details, "disabled by server policy")
	}
}
