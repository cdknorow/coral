package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/cdknorow/coral/internal/store"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
)

func TestPersonalWorkflowAPI(t *testing.T) {
	server, _, _, ss := setupSessionsTestServer(t)
	for _, sid := range []string{"one", "two"} {
		require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{AgentName: "same-name", AgentType: "claude", WorkingDir: "/tmp", SessionID: sid}))
	}
	call := func(method, path string, body any, status int) map[string]any {
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, server.URL+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var got map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		require.Equal(t, status, resp.StatusCode, got)
		return got
	}
	base := "/api/agent/tasks"
	build := call("POST", base, map[string]any{"session_id": "one", "title": "Build", "workflow": map[string]any{"required_outputs": []string{"build"}, "instructions": "Pin revision."}}, 201)
	require.Contains(t, build["workflow"].(map[string]any)["instructions"], "Pin revision.")
	bid := int64(build["id"].(float64))
	dep := []map[string]any{{"task_id": bid, "required_artifacts": []string{"build"}}}
	child := call("POST", base, map[string]any{"session_id": "one", "title": "Test", "blocked_by": dep}, 201)
	cid := int64(child["id"].(float64))
	require.Equal(t, "blocked", child["status"])
	call("GET", fmt.Sprintf("%s/%d?session_id=two", base, bid), nil, 404)
	call("POST", base, map[string]any{"session_id": "two", "title": "Steal", "blocked_by": dep}, 400)
	call("PATCH", fmt.Sprintf("%s/%d", base, bid), map[string]any{"session_id": "two", "title": "Overwrite"}, 404)
	call("POST", base+"/claim", map[string]any{"session_id": "one", "task_id": cid}, 400)
	call("POST", base+"/claim", map[string]any{"session_id": "one", "task_id": bid}, 200)
	call("POST", base+"/claim", map[string]any{"session_id": "one"}, 409)
	call("POST", fmt.Sprintf("%s/%d/complete", base, bid), map[string]any{"session_id": "one"}, 400)
	// The older dashboard route cannot bypass the required artifact either.
	call("PATCH", fmt.Sprintf("/api/sessions/live/same-name/tasks/%d", bid), map[string]any{"completed": 1}, 400)
	call("POST", fmt.Sprintf("%s/%d/complete", base, bid), map[string]any{"session_id": "one", "artifacts": []map[string]string{{"name": "build", "content": "candidate", "revision": "rev-7"}}}, 200)
	ready := call("GET", fmt.Sprintf("%s/%d?session_id=one", base, cid), nil, 200)
	require.Equal(t, "pending", ready["status"])
	claimed := call("POST", base+"/claim", map[string]any{"session_id": "one", "task_id": cid}, 200)
	inputs := claimed["workflow"].(map[string]any)["inputs"].([]any)
	require.Len(t, inputs, 1)
	call("POST", fmt.Sprintf("%s/%d/complete", base, bid), map[string]any{"session_id": "one"}, 400)
	call("POST", fmt.Sprintf("%s/%d/complete", base, cid), map[string]any{"session_id": "one", "outcome": "failed"}, 200)
	failRule := []map[string]any{{"task_id": cid, "condition": "failure"}}
	recovery := call("POST", base, map[string]any{"session_id": "one", "title": "Recover", "blocked_by": failRule}, 201)
	require.Equal(t, "pending", recovery["status"])
	retry := call("POST", base, map[string]any{"session_id": "one", "title": "Retry", "workflow": map[string]any{"retry_of": cid}}, 201)
	require.Equal(t, float64(cid), retry["workflow"].(map[string]any)["retry_of"])
	draft := call("POST", base, map[string]any{"session_id": "one", "title": "Draft", "draft": true}, 201)
	did := int64(draft["id"].(float64))
	require.Equal(t, "draft", draft["status"])
	call("POST", fmt.Sprintf("%s/%d/publish", base, did), map[string]any{"session_id": "one"}, 200)
	names := make([]string, 33)
	for i := range names {
		names[i] = fmt.Sprint(i)
	}
	call("POST", base, map[string]any{"session_id": "one", "title": "Too many", "workflow": map[string]any{"required_outputs": names}}, 400)
}
