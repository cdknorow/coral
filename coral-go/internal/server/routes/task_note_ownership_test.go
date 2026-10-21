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

	"github.com/subgentic/coral/internal/store"
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

// #3498 v3 (Security's MUST): creating a task or a note is scoped the same way. An
// agent creates only on its own list, whatever the path names (a task's notify types
// into the agent it is created for, so the path can't aim it at another session); a
// body session_id naming another session is 403; a terminal session with no token
// (default mode) creates on its own; the Operator creates on the path's agent; anyone
// else is 401.
func TestLiveTaskAndNoteCreateIsScoped(t *testing.T) {
	server, h, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	for _, s := range []struct{ name, sid, typ string }{{"alpha", "a1", "claude"}, {"beta", "b1", "claude"}, {"shell", "t1", "terminal"}} {
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{AgentName: s.name, AgentType: s.typ, WorkingDir: "/tmp", SessionID: s.sid}))
	}
	const operator, nobody = "", "-"
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
	owner := func(id any) (string, string) {
		task, err := h.ts.GetAgentTask(ctx, int64(id.(float64)))
		require.NoError(t, err)
		require.NotNil(t, task)
		sid := ""
		if task.SessionID != nil {
			sid = *task.SessionID
		}
		return task.AgentName, sid
	}

	// beta's agent, through alpha's path, with notify: it lands on beta's own list.
	code, task := call("b1", "POST", "/api/sessions/live/alpha/tasks", map[string]any{"title": "planted", "notify": true})
	require.Equal(t, 200, code, task)
	name, sid := owner(task["id"])
	assert.Equal(t, [2]string{"beta", "b1"}, [2]string{name, sid}, "an agent creates only on its own list")
	alphas, err := h.ts.ListAgentTasks(ctx, "alpha", nil)
	require.NoError(t, err)
	assert.Empty(t, alphas, "nothing planted on alpha")

	code, _ = call("a1", "POST", "/api/sessions/live/alpha/tasks", map[string]any{"title": "x", "session_id": "b1"})
	assert.Equal(t, 403, code, "the body's session_id must be the token's")
	code, _ = call(nobody, "POST", "/api/sessions/live/alpha/tasks", map[string]any{"title": "x", "session_id": "a1"})
	assert.Equal(t, 401, code, "no credential, not a terminal session")

	code, task = call(nobody, "POST", "/api/sessions/live/alpha/tasks", map[string]any{"title": "from a shell", "session_id": "t1"})
	require.Equal(t, 200, code, task)
	name, sid = owner(task["id"])
	assert.Equal(t, [2]string{"shell", "t1"}, [2]string{name, sid}, "a terminal session creates on its own list")

	code, task = call(operator, "POST", "/api/sessions/live/alpha/tasks", map[string]any{"title": "from the dashboard", "session_id": "a1"})
	require.Equal(t, 200, code, task)
	name, sid = owner(task["id"])
	assert.Equal(t, [2]string{"alpha", "a1"}, [2]string{name, sid})

	// Notes, the same rule.
	code, note := call("b1", "POST", "/api/sessions/live/alpha/notes", map[string]any{"content": "planted"})
	require.Equal(t, 200, code, note)
	n, err := h.ts.GetAgentNote(ctx, int64(note["id"].(float64)))
	require.NoError(t, err)
	assert.Equal(t, "beta", n.AgentName)
	code, _ = call("a1", "POST", "/api/sessions/live/alpha/notes", map[string]any{"content": "x", "session_id": "b1"})
	assert.Equal(t, 403, code)
	code, _ = call(nobody, "POST", "/api/sessions/live/alpha/notes", map[string]any{"content": "x"})
	assert.Equal(t, 401, code)
	code, _ = call(operator, "POST", "/api/sessions/live/alpha/notes", map[string]any{"content": "from the dashboard", "session_id": "a1"})
	assert.Equal(t, 200, code)
	alphaNotes, err := h.ts.ListAgentNotes(ctx, "alpha", nil)
	require.NoError(t, err)
	assert.Len(t, alphaNotes, 1, "only the dashboard's note is on alpha")
}
