package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/board"
	"github.com/stretchr/testify/require"
)

func TestTaskAssignmentNoticeClaimsNamedTask(t *testing.T) {
	server, h := setupBoardTestServer(t)
	registerTaskPlanner(t, h, "team", "Orchestrator")
	terminal := newMockTerminal()
	terminal.addSession("frontend", "/tmp/frontend")
	h.SetTerminal(terminal)
	_, err := h.bs.Subscribe(context.Background(), "team", "Frontend", "Frontend", "frontend", nil, nil, "all", false)
	require.NoError(t, err)
	older, err := h.bs.CreateTask(context.Background(), "team", "Old work", "", "high", "Orchestrator", "Frontend")
	require.NoError(t, err)
	base := server.URL + "/api/board/team"
	response := postJSON(t, base+"/tasks", map[string]string{"title": "New named work", "priority": "high", "created_by": "Orchestrator", "assigned_to": "Frontend"})
	require.Equal(t, http.StatusCreated, response.StatusCode)
	var newer board.Task
	require.NoError(t, json.NewDecoder(response.Body).Decode(&newer))
	response.Body.Close()
	command := fmt.Sprintf("coral-board task claim %d", newer.ID)
	require.Eventually(t, func() bool {
		for _, msg := range terminal.sentTo("frontend") {
			if strings.Contains(msg, command) {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return boardMessagesContain(t, base, command) }, 2*time.Second, 10*time.Millisecond)
	for _, reassigned := range []bool{false, true} {
		require.Contains(t, h.buildAssignmentNotification(context.Background(), "team", &newer, "Frontend", reassigned), command)
	}
	response = postJSON(t, base+"/tasks/claim", map[string]any{"subscriber_id": "Frontend", "task_id": newer.ID})
	require.Equal(t, http.StatusOK, response.StatusCode)
	var claimed board.Task
	require.NoError(t, json.NewDecoder(response.Body).Decode(&claimed))
	response.Body.Close()
	require.Equal(t, newer.ID, claimed.ID)
	old, err := h.bs.GetTask(context.Background(), "team", older.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", old.Status)
	// Named commands still enforce the single active slot.
	response = postJSON(t, base+"/tasks/claim", map[string]any{"subscriber_id": "Frontend", "task_id": older.ID})
	require.Equal(t, http.StatusConflict, response.StatusCode)
	response.Body.Close()
}
