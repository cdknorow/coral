package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/store"
	"github.com/stretchr/testify/require"
)

// spyCompletionRunner fails the test's zero-call assertion if any registered
// check is ever executed while the experimental feature is disabled.
type spyCompletionRunner struct{ calls atomic.Int32 }

func (r *spyCompletionRunner) Run(context.Context, string, string, map[string]string) board.RegisteredCheckEvidence {
	r.calls.Add(1)
	return board.RegisteredCheckEvidence{Passed: true, Details: "unexpected runner call"}
}

var allDisabledGateTypes = []board.CompletionGate{
	{Type: "report", Artifact: "report"},
	{Type: "test_evidence", Artifact: "tests"},
	{Type: "landed_revision", Artifact: "landing", Remote: "origin", Branch: "release"},
	{Type: "registered_check", CheckID: "go_test"},
}

func TestCompletionReviewDisabledGatesNeverInvokeRunner(t *testing.T) {
	server, h := setupBoardTestServer(t)
	ctx := context.Background()
	_, err := h.bs.Subscribe(ctx, "spy-review", "lead", "Orchestrator", "lead-session", nil, nil, "all")
	require.NoError(t, err)
	h.bs.SetCompletionChecksEnabled(true)
	task, err := h.bs.CreateTaskWithOpts(ctx, "spy-review", "Gated", "", "medium", "lead", &board.CreateTaskOpts{Workflow: board.TaskWorkflow{CompletionGates: allDisabledGateTypes}}, "worker")
	require.NoError(t, err)
	_, err = h.bs.ClaimTask(ctx, "spy-review", "worker", task.ID)
	require.NoError(t, err)
	h.bs.SetCompletionChecksEnabled(false)
	spy := &spyCompletionRunner{}
	h.bs.SetCompletionCheckRunner(spy)

	r := postJSON(t, fmt.Sprintf("%s/api/board/spy-review/tasks/%d/submit-review", server.URL, task.ID), map[string]any{"subscriber_id": "worker", "reason": "candidate", "candidate_revision": "rev-1"})
	defer r.Body.Close()
	require.Equal(t, http.StatusOK, r.StatusCode)
	require.Zero(t, spy.calls.Load(), "disabled review submission must never run a registered check")
	got, err := h.bs.GetTask(ctx, "spy-review", task.ID)
	require.NoError(t, err)
	require.Len(t, got.Workflow.CompletionReview.GateResults, 4)
	for _, result := range got.Workflow.CompletionReview.GateResults {
		require.False(t, result.Passed)
		require.Contains(t, result.Details, "disabled by server policy")
	}
}

func TestCompletionGatesAuthorizedClearingWhileDisabled(t *testing.T) {
	server, handler, bs := setupTask1670Server(t)
	ctx := context.Background()
	registerTaskPlanner(t, handler, "proj", "Orchestrator")
	_, err := bs.Subscribe(ctx, "proj", "Worker", "Worker", "worker-sess", nil, nil, "mentions")
	require.NoError(t, err)
	bs.SetCompletionChecksEnabled(true)
	task, err := bs.CreateTaskWithOpts(ctx, "proj", "Gated", "", "medium", "Operator", &board.CreateTaskOpts{Workflow: board.TaskWorkflow{CompletionGates: allDisabledGateTypes}}, "Worker")
	require.NoError(t, err)
	bs.SetCompletionChecksEnabled(false)

	amend := func(subscriber string, revision int, gates any) int {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"subscriber_id": subscriber, "base_revision": revision, "reason": "retire experimental gates", "changes": map[string]any{"completion_gates": gates}})
		req, _ := http.NewRequest(http.MethodPatch, fmt.Sprintf("%s/api/board/proj/tasks/%d/amend", server.URL, task.ID), bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		return resp.StatusCode
	}
	// An unauthorized caller cannot clear, and a new non-empty declaration is
	// rejected while the feature is disabled; stored declarations are untouched.
	require.Equal(t, http.StatusForbidden, amend("Worker", 1, []board.CompletionGate{}))
	require.Equal(t, http.StatusBadRequest, amend("Orchestrator", 1, []board.CompletionGate{{Type: "report", Artifact: "report"}}))
	got, err := bs.GetTask(ctx, "proj", task.ID)
	require.NoError(t, err)
	require.Equal(t, 1, got.Revision)
	require.Len(t, got.Workflow.CompletionGates, 4)

	// The planner can clear them.
	require.Equal(t, http.StatusOK, amend("Orchestrator", 1, []board.CompletionGate{}))
	got, err = bs.GetTask(ctx, "proj", task.ID)
	require.NoError(t, err)
	require.Equal(t, 2, got.Revision)
	require.Empty(t, got.Workflow.CompletionGates)
}

func TestPersonalTaskCompletionGatesDisabledByDefault(t *testing.T) {
	server, h, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	ss.RegisterLiveSession(ctx, &store.LiveSession{AgentName: "solo-agent", AgentType: "claude", WorkingDir: "/tmp/solo", SessionID: "solo-1"})
	post := func(path string, body any) (int, map[string]any) {
		t.Helper()
		b, _ := json.Marshal(body)
		resp, err := http.Post(server.URL+path, "application/json", bytes.NewReader(b))
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	require.False(t, h.db.TaskEngine.CompletionChecksEnabled(), "personal-task engine must default to disabled")

	// Every gate type is rejected on create while disabled.
	for _, gate := range allDisabledGateTypes {
		code, out := post("/api/agent/tasks", map[string]any{"session_id": "solo-1", "title": "Gated", "workflow": map[string]any{"completion_gates": []board.CompletionGate{gate}}})
		require.Equal(t, http.StatusBadRequest, code, gate.Type)
		require.Contains(t, fmt.Sprint(out["error"]), "disabled", gate.Type)
	}

	// A declaration stored during an explicit rollout completes normally once
	// disabled, with inactive results and zero runner calls.
	h.db.TaskEngine.SetCompletionChecksEnabled(true)
	code, created := post("/api/agent/tasks", map[string]any{"session_id": "solo-1", "title": "Stored gates", "workflow": map[string]any{"completion_gates": allDisabledGateTypes}})
	require.Equal(t, http.StatusCreated, code)
	h.db.TaskEngine.SetCompletionChecksEnabled(false)
	spy := &spyCompletionRunner{}
	h.db.TaskEngine.SetCompletionCheckRunner(spy)
	code, _ = post("/api/agent/tasks/claim", map[string]any{"session_id": "solo-1"})
	require.Equal(t, http.StatusOK, code)
	code, done := post(fmt.Sprintf("/api/agent/tasks/%v/complete", created["id"]), map[string]any{"session_id": "solo-1", "message": "done", "candidate_revision": "rev-1"})
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "completed", done["status"])
	require.Zero(t, spy.calls.Load(), "disabled personal completion must never run a registered check")
	workflow, _ := done["workflow"].(map[string]any)
	results, _ := workflow["gate_results"].([]any)
	require.Len(t, results, 4)
	for _, r := range results {
		m := r.(map[string]any)
		require.Equal(t, false, m["passed"])
		require.Contains(t, fmt.Sprint(m["details"]), "disabled by server policy")
	}
}
