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

func TestBoardTaskWorkflowAPI(t *testing.T) {
	server, h := setupBoardTestServer(t)
	registerTaskPlanner(t, h, "pipeline", "lead")
	base := server.URL + "/api/board/pipeline/tasks"
	create := func(payload map[string]any) board.Task {
		payload["created_by"] = "lead"
		r := postJSON(t, base, payload)
		defer r.Body.Close()
		require.Equal(t, http.StatusCreated, r.StatusCode)
		var task board.Task
		require.NoError(t, json.NewDecoder(r.Body).Decode(&task))
		return task
	}
	build := create(map[string]any{"title": "Build", "workflow": map[string]any{"instructions": "Preserve build provenance.", "required_outputs": []string{"build"}}})
	require.Contains(t, build.Workflow.Instructions, board.DefaultTaskWorkflowInstructions)
	require.Contains(t, build.Workflow.Instructions, "Preserve build provenance.")
	testTask := create(map[string]any{"title": "Test", "blocked_by": []map[string]any{{"task_id": build.ID, "condition": "success", "required_artifacts": []string{"build"}}}})
	r := postJSON(t, base+"/claim", map[string]any{"subscriber_id": "tester", "task_id": testTask.ID})
	require.Equal(t, http.StatusBadRequest, r.StatusCode)
	r.Body.Close()
	r = postJSON(t, fmt.Sprintf("%s/%d/complete", base, build.ID), map[string]any{"subscriber_id": "builder"})
	require.Equal(t, http.StatusBadRequest, r.StatusCode)
	r.Body.Close()
	r = postJSON(t, fmt.Sprintf("%s/%d/complete", base, build.ID), map[string]any{"subscriber_id": "builder", "artifacts": []board.TaskArtifact{{Name: "build", Content: "release candidate", Revision: "rev-42"}}})
	require.Equal(t, http.StatusOK, r.StatusCode)
	r.Body.Close()
	require.Eventually(t, func() bool {
		task, err := h.bs.GetTask(context.Background(), "pipeline", testTask.ID)
		return err == nil && task.Status == "pending"
	}, time.Second, time.Millisecond*10)
	r = postJSON(t, base+"/claim", map[string]any{"subscriber_id": "tester", "task_id": testTask.ID})
	require.Equal(t, http.StatusOK, r.StatusCode)
	var claimed board.Task
	require.NoError(t, json.NewDecoder(r.Body).Decode(&claimed))
	r.Body.Close()
	require.Equal(t, "rev-42", claimed.Workflow.Inputs[0].Artifacts[0].Revision)
	r = postJSON(t, fmt.Sprintf("%s/%d/complete", base, testTask.ID), map[string]any{"subscriber_id": "tester", "outcome": "failed", "artifacts": []board.TaskArtifact{{Name: "test_report", Content: "Regression found"}}})
	require.Equal(t, http.StatusOK, r.StatusCode)
	var result board.Task
	require.NoError(t, json.NewDecoder(r.Body).Decode(&result))
	r.Body.Close()
	require.Equal(t, "failed", result.Workflow.Outcome)
	require.Len(t, result.Workflow.Inputs, 1)
}
