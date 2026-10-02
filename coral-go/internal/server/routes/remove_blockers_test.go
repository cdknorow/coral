package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/cdknorow/coral/internal/board"
	"github.com/stretchr/testify/require"
)

func TestPlannerRemovesBlockers(t *testing.T) {
	server, h := setupBoardTestServer(t)
	ctx := context.Background()
	registerTaskPlanner(t, h, "team", "Orchestrator")
	a, err := h.bs.CreateTask(ctx, "team", "A", "", "medium", "Orchestrator")
	require.NoError(t, err)
	b, err := h.bs.CreateTask(ctx, "team", "B", "", "medium", "Orchestrator")
	require.NoError(t, err)
	child, err := h.bs.CreateTaskWithOpts(ctx, "team", "Dependent", "", "medium", "Orchestrator", &board.CreateTaskOpts{BlockedBy: []board.TaskDep{{TaskID: a.ID, BoardID: "team"}, {TaskID: b.ID, BoardID: "team", Condition: "termination", RequiredArtifacts: []string{"report"}}}, MaxDepth: 3})
	require.NoError(t, err)
	path := fmt.Sprintf("%s/api/board/team/tasks/%d", server.URL, child.ID)
	r := patchJSON(t, path, map[string]any{"subscriber_id": "worker", "clear_blockers": true})
	require.Equal(t, http.StatusForbidden, r.StatusCode)
	r.Body.Close()
	r = patchJSON(t, path, map[string]any{"subscriber_id": "Orchestrator", "remove_blocker": a.ID})
	require.Equal(t, http.StatusOK, r.StatusCode)
	var task board.Task
	require.NoError(t, json.NewDecoder(r.Body).Decode(&task))
	r.Body.Close()
	require.Equal(t, "blocked", task.Status)
	require.Len(t, task.BlockedBy, 1)
	require.Equal(t, b.ID, task.BlockedBy[0].TaskID)
	require.Equal(t, "termination", task.BlockedBy[0].Condition)
	require.Equal(t, []string{"report"}, task.BlockedBy[0].RequiredArtifacts)
	r = patchJSON(t, path, map[string]any{"subscriber_id": "Orchestrator", "clear_blockers": true})
	require.Equal(t, http.StatusOK, r.StatusCode)
	task = board.Task{}
	require.NoError(t, json.NewDecoder(r.Body).Decode(&task))
	r.Body.Close()
	require.Equal(t, "pending", task.Status)
	require.Empty(t, task.BlockedBy)
	_, err = h.bs.ClaimTask(ctx, "team", "worker", child.ID)
	require.NoError(t, err)
	r = patchJSON(t, path, map[string]any{"subscriber_id": "Orchestrator", "clear_blockers": true})
	require.Equal(t, http.StatusBadRequest, r.StatusCode)
	r.Body.Close()
}
