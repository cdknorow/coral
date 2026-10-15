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

	"github.com/cdknorow/coral/internal/store"
)

// #3498 (security sweep item 9): a live session's task and note routes act only on
// their own items. The Operator (the dashboard) acts on the path's agent, and on its
// ?session_id= when given; an agent, by its token, acts only on its own items,
// whatever the path says; anyone else is refused. A refused change changes nothing.
func TestLiveTaskAndNoteRoutesCheckOwnership(t *testing.T) {
	server, h, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	for _, s := range []struct{ name, sid string }{{"alpha", "a1"}, {"beta", "b1"}} {
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{AgentName: s.name, AgentType: "claude", WorkingDir: "/tmp", SessionID: s.sid}))
	}
	const operator, nobody = "", "-"
	// as is the caller: operator (the dashboard), nobody (no credential), or a session
	// id whose agent the request acts as (its token).
	call := func(as, method, path string, body any) (int, map[string]any) {
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, server.URL+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		switch as {
		case operator:
			req.Header.Set("Origin", server.URL)
			req.Header.Set("Sec-Fetch-Site", "same-origin")
		case nobody:
		default:
			req.Header.Set(testAgentHeader, as)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var got map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&got)
		return resp.StatusCode, got
	}

	code, task := call(operator, "POST", "/api/sessions/live/alpha/tasks", map[string]any{"title": "alpha's task", "session_id": "a1"})
	require.Equal(t, 200, code, task)
	tid := int64(task["id"].(float64))
	taskPath := func(name, q string) string { return fmt.Sprintf("/api/sessions/live/%s/tasks/%d%s", name, tid, q) }
	unchanged := func(why string) {
		got, err := h.ts.GetAgentTask(ctx, tid)
		require.NoError(t, err)
		require.NotNil(t, got, why)
		assert.Equal(t, "alpha's task", got.Title, why)
	}

	for _, c := range []struct {
		as, method, path string
		want             int
	}{
		{operator, "PATCH", taskPath("beta", ""), 404},
		{operator, "DELETE", taskPath("beta", ""), 404},
		{operator, "PATCH", taskPath("alpha", "?session_id=b1"), 404},
		{operator, "PATCH", fmt.Sprintf("/api/sessions/live/alpha/tasks/%d", tid+999), 404},
		{"b1", "PATCH", taskPath("alpha", ""), 404},               // another agent, through the owner's path
		{"b1", "DELETE", taskPath("alpha", ""), 404},              // the path doesn't decide for an agent
		{"a1", "PATCH", taskPath("alpha", "?session_id=b1"), 403}, // claiming another session
		{nobody, "PATCH", taskPath("alpha", ""), 401},
		{nobody, "DELETE", taskPath("alpha", ""), 401},
	} {
		code, _ := call(c.as, c.method, c.path, map[string]any{"title": "overwritten"})
		assert.Equal(t, c.want, code, "%q %s %s", c.as, c.method, c.path)
	}
	code, _ = call(operator, "POST", "/api/sessions/live/beta/tasks/reorder", map[string]any{"task_ids": []int64{tid}})
	assert.Equal(t, 404, code, "reorder through another agent's path")
	code, _ = call("b1", "POST", "/api/sessions/live/alpha/tasks/reorder", map[string]any{"task_ids": []int64{tid}})
	assert.Equal(t, 404, code, "another agent's reorder")
	unchanged("after the refused calls")

	// The owner: the dashboard by path (with and without the session id), and the
	// agent by its token even when the path's name drifted (the hook's working folder).
	code, _ = call(operator, "PATCH", taskPath("alpha", ""), map[string]any{"sort_order": 2})
	assert.Equal(t, 200, code)
	code, _ = call(operator, "PATCH", taskPath("alpha", "?session_id=a1"), map[string]any{"sort_order": 3})
	assert.Equal(t, 200, code)
	code, _ = call("a1", "PATCH", taskPath("alpha-subfolder", ""), map[string]any{"title": "renamed by its agent"})
	assert.Equal(t, 200, code)
	code, _ = call("a1", "POST", "/api/sessions/live/alpha/tasks/reorder", map[string]any{"task_ids": []int64{tid}})
	assert.Equal(t, 200, code)

	code, note := call(operator, "POST", "/api/sessions/live/alpha/notes", map[string]any{"content": "alpha's note", "session_id": "a1"})
	require.Equal(t, 200, code, note)
	nid := int64(note["id"].(float64))
	notePath := func(name, q string) string { return fmt.Sprintf("/api/sessions/live/%s/notes/%d%s", name, nid, q) }
	for _, c := range []struct {
		as, method, path string
		want             int
	}{
		{operator, "PATCH", notePath("beta", ""), 404},
		{operator, "DELETE", notePath("alpha", "?session_id=b1"), 404},
		{"b1", "DELETE", notePath("alpha", ""), 404},
		{nobody, "DELETE", notePath("alpha", ""), 401},
		{operator, "DELETE", fmt.Sprintf("/api/sessions/live/alpha/notes/%d", nid+999), 404},
	} {
		code, _ := call(c.as, c.method, c.path, map[string]any{"content": "overwritten"})
		assert.Equal(t, c.want, code, "%q %s %s", c.as, c.method, c.path)
	}
	n, err := h.ts.GetAgentNote(ctx, nid)
	require.NoError(t, err)
	require.NotNil(t, n)
	assert.Equal(t, "alpha's note", n.Content)
	code, _ = call("a1", "PATCH", notePath("alpha", ""), map[string]any{"content": "edited by its agent"})
	assert.Equal(t, 200, code)
	code, _ = call(operator, "DELETE", notePath("alpha", "?session_id=a1"), nil)
	assert.Equal(t, 200, code)
}
