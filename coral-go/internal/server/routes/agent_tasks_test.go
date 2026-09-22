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

func TestAgentTasks_ClaimAndComplete(t *testing.T) {
	server, _, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	ss.RegisterLiveSession(ctx, &store.LiveSession{AgentName: "solo-agent", AgentType: "claude", WorkingDir: "/tmp/solo", SessionID: "solo-1"})
	ss.RegisterLiveSession(ctx, &store.LiveSession{AgentName: "other-agent", AgentType: "claude", WorkingDir: "/tmp/other", SessionID: "other-1"})

	post := func(path string, body any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		resp, err := http.Post(server.URL+path, "application/json", bytes.NewReader(b))
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// The operator gives the agent two tasks (the second with details)
	code, first := post("/api/sessions/live/solo-agent/tasks", map[string]any{"title": "Add a battle log", "session_id": "solo-1"})
	require.Equal(t, http.StatusOK, code)
	code, second := post("/api/sessions/live/solo-agent/tasks", map[string]any{"title": "Write engine tests", "body": "Cover ties and forfeits.", "session_id": "solo-1"})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "Cover ties and forfeits.", second["body"])

	// Claiming takes them in order and marks each in progress
	code, claimed := post("/api/agent-tasks/claim", map[string]any{"session_id": "solo-1"})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, first["id"], claimed["id"])
	assert.EqualValues(t, 2, claimed["completed"], "claimed = in progress")
	code, claimed2 := post("/api/agent-tasks/claim", map[string]any{"session_id": "solo-1"})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, second["id"], claimed2["id"])
	assert.Equal(t, "Cover ties and forfeits.", claimed2["body"], "the details come with the claim")
	code, none := post("/api/agent-tasks/claim", map[string]any{"session_id": "solo-1"})
	assert.Equal(t, http.StatusNotFound, code)
	assert.Equal(t, "No pending tasks", none["error"])

	// Completing marks it done; another agent cannot complete it
	code, _ = post(fmt.Sprintf("/api/agent-tasks/%v/complete", first["id"]), map[string]any{"session_id": "other-1"})
	assert.Equal(t, http.StatusNotFound, code, "a task belongs to its own agent")
	code, done := post(fmt.Sprintf("/api/agent-tasks/%v/complete", first["id"]), map[string]any{"session_id": "solo-1"})
	require.Equal(t, http.StatusOK, code)
	assert.EqualValues(t, 1, done["completed"])

	// The agent's list shows the states
	resp, err := http.Get(server.URL + "/api/agent-tasks?session_id=solo-1")
	require.NoError(t, err)
	var list []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&list))
	resp.Body.Close()
	require.Len(t, list, 2)
	assert.EqualValues(t, 1, list[0]["completed"])
	assert.EqualValues(t, 2, list[1]["completed"])

	// Unknown sessions are refused
	code, _ = post("/api/agent-tasks/claim", map[string]any{"session_id": "nope"})
	assert.Equal(t, http.StatusNotFound, code)
}
