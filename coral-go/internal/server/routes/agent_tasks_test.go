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

// The agent task API mirrors the board task API: same verbs, status codes and
// task JSON shape (status/priority/assigned_to/completion_message).
func TestAgentTasks_MirrorBoardTaskAPI(t *testing.T) {
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
	list := func(sid string) []map[string]any {
		resp, err := http.Get(server.URL + "/api/agent/tasks?session_id=" + sid)
		require.NoError(t, err)
		defer resp.Body.Close()
		var out struct {
			Tasks []map[string]any `json:"tasks"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		return out.Tasks
	}

	// The operator gives the agent a task from the dashboard (with priority and details)
	code, first := post("/api/sessions/live/solo-agent/tasks", map[string]any{"title": "Add a battle log", "body": "Log each round.", "priority": "high", "session_id": "solo-1"})
	require.Equal(t, http.StatusOK, code)
	// The agent adds one itself (task add), like on a board: 201 + board-shaped task
	code, added := post("/api/agent/tasks", map[string]any{"session_id": "solo-1", "title": "Write engine tests", "body": "Cover ties.", "priority": "low"})
	require.Equal(t, http.StatusCreated, code)
	assert.Equal(t, "pending", added["status"])
	assert.Equal(t, "low", added["priority"])
	assert.Equal(t, "solo-agent", added["assigned_to"])
	code, third := post("/api/agent/tasks", map[string]any{"session_id": "solo-1", "title": "Tidy up"})
	require.Equal(t, http.StatusCreated, code)
	assert.Equal(t, "medium", third["priority"], "priority defaults to medium, as on a board")

	// current: nothing in progress yet
	code, cur := post("/api/agent/tasks/current", map[string]any{"session_id": "solo-1"})
	assert.Equal(t, http.StatusNotFound, code)
	assert.Equal(t, "No active task", cur["error"])

	// claim takes them in order; the claimed task is in_progress with its details
	code, claimed := post("/api/agent/tasks/claim", map[string]any{"session_id": "solo-1"})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, first["id"], claimed["id"])
	assert.Equal(t, "in_progress", claimed["status"])
	assert.Equal(t, "high", claimed["priority"])
	assert.Equal(t, "Log each round.", claimed["body"])
	assert.NotEmpty(t, claimed["claimed_at"])
	code, cur = post("/api/agent/tasks/current", map[string]any{"session_id": "solo-1"})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, first["id"], cur["id"])

	// complete with a message; another agent cannot touch it
	code, _ = post(fmt.Sprintf("/api/agent/tasks/%v/complete", first["id"]), map[string]any{"session_id": "other-1"})
	assert.Equal(t, http.StatusNotFound, code, "a task belongs to its own agent")
	code, done := post(fmt.Sprintf("/api/agent/tasks/%v/complete", first["id"]), map[string]any{"session_id": "solo-1", "message": "Logged in battle_log.go"})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "completed", done["status"])
	assert.Equal(t, "Logged in battle_log.go", done["completion_message"])

	// cancel -> skipped (board vocabulary); a cancelled task is never claimed
	code, cancelled := post(fmt.Sprintf("/api/agent/tasks/%v/cancel", added["id"]), map[string]any{"session_id": "solo-1", "message": "Not needed"})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "skipped", cancelled["status"])
	code, claimed = post("/api/agent/tasks/claim", map[string]any{"session_id": "solo-1"})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, third["id"], claimed["id"], "cancelled tasks are skipped by claim")
	code, none := post("/api/agent/tasks/claim", map[string]any{"session_id": "solo-1"})
	assert.Equal(t, http.StatusNotFound, code)
	assert.Equal(t, "No available tasks", none["error"])

	// list: {"tasks": [...]} with board statuses
	statuses := map[any]string{}
	for _, t := range list("solo-1") {
		statuses[t["id"]] = t["status"].(string)
	}
	assert.Equal(t, map[any]string{first["id"]: "completed", added["id"]: "skipped", third["id"]: "in_progress"}, statuses)
	assert.Empty(t, list("other-1"), "the other agent sees none of them")

	// Claim order follows the board: highest priority first, then oldest
	for _, tc := range []struct{ title, priority string }{{"Low one", "low"}, {"Critical one", "critical"}, {"Medium one", "medium"}, {"High one", "high"}} {
		code, _ := post("/api/agent/tasks", map[string]any{"session_id": "other-1", "title": tc.title, "priority": tc.priority})
		require.Equal(t, http.StatusCreated, code)
	}
	var order []string
	for i := 0; i < 4; i++ {
		_, c := post("/api/agent/tasks/claim", map[string]any{"session_id": "other-1"})
		order = append(order, c["title"].(string))
	}
	assert.Equal(t, []string{"Critical one", "High one", "Medium one", "Low one"}, order)

	// Unknown sessions are refused
	code, _ = post("/api/agent/tasks/claim", map[string]any{"session_id": "nope"})
	assert.Equal(t, http.StatusNotFound, code)
}
