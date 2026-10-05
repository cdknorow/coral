package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/board"
	"github.com/stretchr/testify/require"
)

// An explicit planner assignment must never be offered to the idle worker
// pool, and neither automatic nor named claims may take another agent's task.
func TestOrchestratorTaskAssignmentIsolation(t *testing.T) {
	server, h := setupBoardTestServer(t)
	registerTaskPlanner(t, h, "team", "Orchestrator")
	terminal := newMockTerminal()
	terminal.addSession("orchestrator-session", "/tmp")
	terminal.addSession("worker-session", "/tmp")
	h.SetTerminal(terminal)
	ctx := context.Background()
	_, err := h.bs.Subscribe(ctx, "team", "Orchestrator", "Orchestrator", "orchestrator-session", nil, nil, "all", true)
	require.NoError(t, err)
	_, err = h.bs.Subscribe(ctx, "team", "Worker", "Worker", "worker-session", nil, nil, "all", false)
	require.NoError(t, err)
	base := server.URL + "/api/board/team"
	resp := postJSON(t, base+"/tasks", map[string]string{"title": "Orchestrator only", "created_by": "Orchestrator", "assigned_to": "Orchestrator"})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var task board.Task
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&task))
	resp.Body.Close()
	require.Eventually(t, func() bool { return len(terminal.sentTo("orchestrator-session")) > 0 }, 2*time.Second, 10*time.Millisecond)
	require.Empty(t, terminal.sentTo("worker-session"))
	for _, id := range []int64{0, task.ID} {
		resp = postJSON(t, base+"/tasks/claim", map[string]any{"subscriber_id": "Worker", "task_id": id})
		require.NotEqual(t, http.StatusOK, resp.StatusCode)
		resp.Body.Close()
		saved, err := h.bs.GetTask(ctx, "team", task.ID)
		require.NoError(t, err)
		require.Equal(t, "pending", saved.Status)
		require.Equal(t, "Orchestrator", *saved.AssignedTo)
	}
	resp = postJSON(t, base+"/tasks/claim", map[string]any{"subscriber_id": "Orchestrator", "task_id": task.ID})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
	require.Contains(t, terminal.sentTo("orchestrator-session")[0], fmt.Sprintf("claim %d", task.ID))
}
