package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/internal/board"
)

func TestTaskAssignmentNoticeClaimsNamedTask(t *testing.T) {
	server, h := setupBoardTestServer(t)
	registerTaskPlanner(t, h, "team", "Orchestrator")
	terminal := newMockTerminal()
	terminal.addSession("claude-0000000b-3864-4864-8864-00000000000b", "/tmp/frontend")
	h.SetTerminal(terminal)
	_, err := h.bs.Subscribe(context.Background(), "team", "Frontend", "Frontend", "claude-0000000b-3864-4864-8864-00000000000b", nil, "all", false)
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
		for _, msg := range terminal.sentTo("claude-0000000b-3864-4864-8864-00000000000b") {
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

// #5183: a parked task is not offered. Reassigning it to nobody says it is unassigned and parked, without the "run claim to pick it up" invitation to everyone, and reassigning it to an
// agent says it is theirs but parked, with no claim nudge typed into their terminal.
func TestParkedTaskReassignOffersNothing(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	server, h := setupBoardTestServer(t)
	registerTaskPlanner(t, h, "team", "Orchestrator")
	terminal := newMockTerminal()
	terminal.addSession("claude-0000000c-3864-4864-8864-00000000000c", "/tmp/frontend")
	h.SetTerminal(terminal)
	_, err := h.bs.Subscribe(context.Background(), "team", "Frontend", "Frontend", "claude-0000000c-3864-4864-8864-00000000000c", nil, "all", false)
	require.NoError(t, err)
	task, err := h.bs.CreateTask(context.Background(), "team", "Phase 2 comments", "", "medium", "Orchestrator", "")
	require.NoError(t, err)
	_, err = h.bs.ParkTask(context.Background(), "team", task.ID, "starts after phase 1 ships", time.Time{})
	require.NoError(t, err)
	base := server.URL + "/api/board/team"
	for _, to := range []string{"", "Frontend"} {
		response := postJSON(t, fmt.Sprintf("%s/tasks/%d/reassign", base, task.ID), map[string]string{"subscriber_id": "Orchestrator", "assignee": to})
		require.Equal(t, http.StatusOK, response.StatusCode)
		response.Body.Close()
	}
	require.Eventually(t, func() bool {
		return boardMessagesContain(t, base, "parked, not for claiming yet: starts after phase 1 ships")
	}, 2*time.Second, 10*time.Millisecond, "unassigned: said, without an invitation")
	require.Eventually(t, func() bool {
		return boardMessagesContain(t, base, "assigned to Frontend; parked, not to be started yet")
	}, 2*time.Second, 10*time.Millisecond, "assigned: said, without a claim line")
	time.Sleep(300 * time.Millisecond) // sleep-ok: the claim nudge, if it were sent, goes out in the same background step as the notice
	require.False(t, boardMessagesContain(t, base, "run 'coral-board task claim"), "nobody is invited to claim a parked task")
	require.False(t, boardMessagesContain(t, base, "@notify-all"), "no all-hands notice")
	for _, msg := range terminal.sentTo("claude-0000000c-3864-4864-8864-00000000000c") {
		require.NotContains(t, msg, "task claim", "no claim nudge for a parked task")
	}
	// unparked, the same reassign offers it as before
	_, err = h.bs.UnparkTask(context.Background(), "team", task.ID)
	require.NoError(t, err)
	got, err := h.bs.GetTask(context.Background(), "team", task.ID)
	require.NoError(t, err)
	require.Contains(t, h.buildAssignmentNotification(context.Background(), "team", got, "Frontend", true), "run 'coral-board task claim")
}
