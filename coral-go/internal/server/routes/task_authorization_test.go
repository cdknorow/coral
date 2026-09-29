package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBoardTaskPlanningRequiresRegisteredPrivilege(t *testing.T) {
	server, h := setupBoardTestServer(t)
	ctx := context.Background()
	registerTaskPlanner(t, h, "team", "coordinator")
	registerTaskPlanner(t, h, "other", "foreign")
	for _, name := range []string{"worker", "Fake Orchestrator"} {
		_, err := h.bs.Subscribe(ctx, "team", name, "Developer", "", nil, nil, "all")
		require.NoError(t, err)
	}
	registerTaskPlanner(t, h, "team", "inactive")
	_, err := h.bs.Unsubscribe(ctx, "team", "inactive")
	require.NoError(t, err)
	base := server.URL + "/api/board/team/tasks"
	for _, actor := range []string{"worker", "Fake Orchestrator", "foreign", "inactive", "unregistered"} {
		t.Run("deny-"+actor, func(t *testing.T) {
			r := postJSON(t, base, map[string]any{"title": "Unauthorized", "subscriber_id": actor, "created_by": actor, "job_title": "Orchestrator", "can_peek": 1, "draft": true, "workflow": map[string]any{"retry_of": 1}})
			r.Body.Close()
			require.Equal(t, http.StatusForbidden, r.StatusCode)
		})
	}
	spoof := postJSON(t, base, map[string]string{"title": "Spoof", "subscriber_id": "worker", "created_by": "coordinator"})
	spoof.Body.Close()
	require.Equal(t, http.StatusForbidden, spoof.StatusCode)
	_, err = h.bs.Subscribe(ctx, "team", "privileged", "Coordinator", "", nil, nil, "all", true)
	require.NoError(t, err)
	privileged := postJSON(t, base, map[string]any{"title": "Draft", "subscriber_id": "privileged", "draft": true})
	privileged.Body.Close()
	require.Equal(t, http.StatusCreated, privileged.StatusCode)
	r := postJSON(t, base, map[string]any{"title": "Work", "subscriber_id": "coordinator", "assigned_to": "worker"})
	require.Equal(t, http.StatusCreated, r.StatusCode)
	var task struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.NewDecoder(r.Body).Decode(&task))
	r.Body.Close()
	for _, actor := range []string{"worker", "foreign", "inactive", ""} {
		r = postJSON(t, fmt.Sprintf("%s/%d/reassign", base, task.ID), map[string]string{"subscriber_id": actor, "assignee": "other"})
		r.Body.Close()
		require.Equal(t, http.StatusForbidden, r.StatusCode)
		b, _ := json.Marshal(map[string]string{"subscriber_id": actor, "assigned_to": "other", "title": "mutated"})
		req, err := http.NewRequest(http.MethodPatch, fmt.Sprintf("%s/%d", base, task.ID), bytes.NewReader(b))
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusForbidden, resp.StatusCode)
	}
	saved, err := h.bs.GetTask(ctx, "team", task.ID)
	require.NoError(t, err)
	require.Equal(t, "Work", saved.Title)
	require.Equal(t, "worker", *saved.AssignedTo)
	r = postJSON(t, base+"/claim", map[string]any{"subscriber_id": "worker", "task_id": task.ID})
	r.Body.Close()
	require.Equal(t, http.StatusOK, r.StatusCode)
	r = postJSON(t, fmt.Sprintf("%s/%d/complete", base, task.ID), map[string]string{"subscriber_id": "worker", "message": "done"})
	r.Body.Close()
	require.Equal(t, http.StatusOK, r.StatusCode)
}

func TestBoardTaskPlanningOperatorAndOrchestrator(t *testing.T) {
	for _, actor := range []string{"Operator", "coordinator"} {
		t.Run(actor, func(t *testing.T) {
			server, h := setupBoardTestServer(t)
			registerTaskPlanner(t, h, "team", "coordinator")
			base := server.URL + "/api/board/team/tasks"
			r := postJSON(t, base, map[string]string{"created_by": actor, "title": "Allowed"})
			require.Equal(t, http.StatusCreated, r.StatusCode)
			var task struct {
				ID int64 `json:"id"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&task))
			r.Body.Close()
			r = postJSON(t, fmt.Sprintf("%s/%d/reassign", base, task.ID), map[string]string{"subscriber_id": actor, "assignee": "worker"})
			r.Body.Close()
			require.Equal(t, http.StatusOK, r.StatusCode)
			b, _ := json.Marshal(map[string]string{"subscriber_id": actor, "assigned_to": "next"})
			req, err := http.NewRequest(http.MethodPatch, fmt.Sprintf("%s/%d", base, task.ID), bytes.NewReader(b))
			require.NoError(t, err)
			r, err = http.DefaultClient.Do(req)
			require.NoError(t, err)
			r.Body.Close()
			require.Equal(t, http.StatusOK, r.StatusCode)
			saved, err := h.bs.GetTask(context.Background(), "team", task.ID)
			require.NoError(t, err)
			require.Equal(t, "next", *saved.AssignedTo)
			spoof := postJSON(t, base, map[string]string{"subscriber_id": "worker", "created_by": actor, "title": "Spoof"})
			spoof.Body.Close()
			require.Equal(t, http.StatusForbidden, spoof.StatusCode)
		})
	}
}
