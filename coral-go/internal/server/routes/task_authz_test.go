package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3668 (the task handlers' follow-up to the board authorization audit): cancel, title and
// priority edits, nudges, reminders and snoozes need a planner (the Operator or an active
// registered orchestrator) or the task's assignee; publishing needs a planner. A second worker
// on the board, neither planner nor assignee, is refused on every one.
func TestTaskRoutesNeedAPlannerOrTheAssignee(t *testing.T) {
	server, h := setupBoardTestServer(t)
	ctx := context.Background()
	registerTaskPlanner(t, h, "team", "Orch")
	for _, w := range []string{"Dev1", "Dev2"} {
		_, err := h.bs.Subscribe(ctx, "team", w, "Developer", "", nil, "all")
		require.NoError(t, err)
	}
	base := server.URL + "/api/board/team"
	req := func(method, path string, body any) int {
		t.Helper()
		var rd *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = bytes.NewReader(nil)
		}
		r, _ := http.NewRequest(method, base+path, rd)
		r.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(r)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	newTask := func(draft bool) int64 {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"title": "work", "created_by": "Orch", "assigned_to": "Dev1", "draft": draft})
		resp, err := http.Post(base+"/tasks", "application/json", bytes.NewReader(b))
		require.NoError(t, err)
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		require.Equal(t, http.StatusCreated, resp.StatusCode, "%v", out)
		return int64(out["id"].(float64))
	}
	id := newTask(false)
	p := func(suffix string) string { return fmt.Sprintf("/tasks/%d%s", id, suffix) }

	// Title and priority edits.
	assert.Equal(t, http.StatusForbidden, req("PATCH", p(""), map[string]any{"subscriber_id": "Dev2", "title": "do something else"}))
	assert.Equal(t, http.StatusOK, req("PATCH", p(""), map[string]any{"subscriber_id": "Dev1", "priority": "high"}), "the assignee")
	assert.Equal(t, http.StatusOK, req("PATCH", p(""), map[string]any{"subscriber_id": "Orch", "title": "work, sharpened"}), "a planner")

	// Nudge and reminders (start, stop). Snooze uses the same check (requirePlannerOrAssignee).
	assert.Equal(t, http.StatusForbidden, req("POST", p("/nudge?subscriber_id=Dev2"), nil))
	assert.Equal(t, http.StatusBadRequest, req("POST", p("/nudge"), nil), "no actor")
	assert.NotEqual(t, http.StatusForbidden, req("POST", p("/nudge?subscriber_id=Operator"), nil), "the Operator may nudge (this test assignee has no terminal to type into)")
	assert.Equal(t, http.StatusForbidden, req("POST", p("/reminder"), map[string]any{"interval_seconds": 60, "subscriber_id": "Dev2"}))
	assert.Equal(t, http.StatusOK, req("POST", p("/reminder"), map[string]any{"interval_seconds": 60, "subscriber_id": "Dev1"}))
	assert.Equal(t, http.StatusForbidden, req("DELETE", p("/reminder?subscriber_id=Dev2"), nil))
	assert.Equal(t, http.StatusOK, req("DELETE", p("/reminder?subscriber_id=Orch"), nil))

	// Publish: a planner only (the assignee can't release its own draft).
	draft := newTask(true)
	pd := fmt.Sprintf("/tasks/%d/publish", draft)
	assert.Equal(t, http.StatusForbidden, req("POST", pd+"?subscriber_id=Dev2", nil))
	assert.Equal(t, http.StatusForbidden, req("POST", pd+"?subscriber_id=Dev1", nil))
	assert.Equal(t, http.StatusBadRequest, req("POST", pd, nil), "no actor")
	assert.Equal(t, http.StatusOK, req("POST", pd, map[string]any{"subscriber_id": "Orch"}))

	// Complete: another worker is refused as a permission refusal (403, like cancel), not a malformed request (400, #298).
	assert.Equal(t, http.StatusForbidden, req("POST", p("/complete"), map[string]any{"subscriber_id": "Dev2"}))

	// Cancel: the same rule as complete; it is the last step, so last.
	assert.Equal(t, http.StatusForbidden, req("POST", p("/cancel"), map[string]any{"subscriber_id": "Dev2"}))
	task, err := h.bs.GetTask(ctx, "team", id)
	require.NoError(t, err)
	assert.NotEqual(t, "skipped", task.Status, "not cancelled by the other worker")
	assert.Equal(t, http.StatusOK, req("POST", p("/cancel"), map[string]any{"subscriber_id": "Dev1"}), "the assignee")
}
