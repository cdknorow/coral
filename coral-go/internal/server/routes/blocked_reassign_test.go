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

func TestBlockedTaskReassignPreservesDependencies(t *testing.T) {
	server, h := setupBoardTestServer(t)
	ctx := context.Background()
	registerTaskPlanner(t, h, "team", "Orchestrator")
	terminal := newMockTerminal()
	terminal.addSession("new-owner", "/tmp/fixture")
	h.SetTerminal(terminal)
	_, err := h.bs.Subscribe(ctx, "team", "new-owner", "Developer", "new-owner", nil, nil, "all", false)
	require.NoError(t, err)
	up, err := h.bs.CreateTask(ctx, "team", "Upstream", "", "medium", "Orchestrator", "builder")
	require.NoError(t, err)
	child, err := h.bs.CreateTaskWithOpts(ctx, "team", "Blocked", "", "medium", "Orchestrator", &board.CreateTaskOpts{BlockedBy: []board.TaskDep{{TaskID: up.ID, BoardID: "team", Condition: "success"}}, MaxDepth: 3})
	require.NoError(t, err)
	require.Equal(t, "blocked", child.Status)
	base := server.URL + "/api/board/team"
	path := fmt.Sprintf("%s/tasks/%d/reassign", base, child.ID)
	// Both reassignment and returning to the unassigned pool preserve the block.
	for _, owner := range []string{"old-owner", "", "new-owner"} {
		resp := postJSON(t, path, map[string]string{"subscriber_id": "Orchestrator", "assignee": owner})
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var result board.Task
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
		resp.Body.Close()
		require.Equal(t, "blocked", result.Status)
		if owner == "" {
			require.Nil(t, result.AssignedTo)
		} else {
			require.Equal(t, owner, *result.AssignedTo)
		}
		require.Nil(t, result.ClaimedAt)
		require.Nil(t, result.SessionID)
		require.NotContains(t, h.buildAssignmentNotification(ctx, "team", &result, owner, true), "coral-board task claim")
	}
	require.Eventually(t, func() bool { return boardMessagesContain(t, base, "assigned to @new-owner; still blocked") }, time.Second, 10*time.Millisecond)
	require.Never(t, func() bool { return len(terminal.sentTo("new-owner")) > 0 }, 100*time.Millisecond, 10*time.Millisecond)
	_, err = h.bs.ClaimTask(ctx, "team", "new-owner", child.ID)
	require.Error(t, err)
	_, err = h.bs.ClaimTask(ctx, "team", "builder", up.ID)
	require.NoError(t, err)
	_, err = h.bs.CompleteTask(ctx, "team", up.ID, "builder", nil)
	require.NoError(t, err)
	_, err = h.bs.ResolveDownstreamTasks(ctx, "team", up.ID)
	require.NoError(t, err)
	claimed, err := h.bs.ClaimTask(ctx, "team", "new-owner", child.ID)
	require.NoError(t, err)
	require.Equal(t, child.ID, claimed.ID)
	_, err = h.bs.CompleteTask(ctx, "team", child.ID, "new-owner", nil)
	require.NoError(t, err)
	_, err = h.bs.ReassignTask(ctx, "team", child.ID, "other")
	require.Error(t, err, "completed tasks must remain immutable")
}
