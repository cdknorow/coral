package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/board"
)

func setupTask1670Server(t *testing.T) (*httptest.Server, *BoardHandler, *board.Store) {
	t.Helper()
	dbPath := t.TempDir() + "/task1670_board.db"
	bs, err := board.NewStore(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })

	handler := NewBoardHandler(bs)

	r := chi.NewRouter()
	r.Post("/api/board/{project}/subscribe", handler.Subscribe)
	r.Post("/api/board/{project}/tasks", handler.CreateTask)
	r.Get("/api/board/{project}/tasks/{taskID}", handler.GetTask)
	r.Patch("/api/board/{project}/tasks/{taskID}", handler.UpdateTask)
	r.Patch("/api/board/{project}/tasks/{taskID}/amend", handler.AmendTask)
	r.Post("/api/board/{project}/tasks/claim", handler.ClaimTask)
	r.Post("/api/board/{project}/tasks/{taskID}/complete", handler.CompleteTaskByID)
	r.Post("/api/board/{project}/tasks/{taskID}/submit-review", handler.SubmitCompletionReview)
	r.Get("/api/board/{project}/messages", handler.ReadMessages)

	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	return server, handler, bs
}

func TestTask1670_HTTPPlannerAuthorization(t *testing.T) {
	server, handler, bs := setupTask1670Server(t)
	ctx := context.Background()

	// Register Orchestrator as planner, Worker as regular subscriber
	registerTaskPlanner(t, handler, "proj", "Orchestrator")
	_, err := bs.Subscribe(ctx, "proj", "Worker", "Worker", "worker-sess", nil, nil, "mentions")
	require.NoError(t, err)

	task, err := bs.CreateTaskWithOpts(ctx, "proj", "Title", "Body", "medium", "Operator", nil, "Worker")
	require.NoError(t, err)

	// 1. Worker tries to amend -> 403 Forbidden
	payloadWorker := map[string]interface{}{
		"subscriber_id": "Worker",
		"base_revision": 1,
		"reason":        "Worker tries to amend",
		"changes":       map[string]interface{}{"body": "Worker body"},
	}
	bodyBytes, _ := json.Marshal(payloadWorker)
	req, _ := http.NewRequest("PATCH", fmt.Sprintf("%s/api/board/proj/tasks/%d/amend", server.URL, task.ID), bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	// 2. Orchestrator tries to amend -> 200 OK
	payloadOrch := map[string]interface{}{
		"subscriber_id": "Orchestrator",
		"base_revision": 1,
		"reason":        "Orchestrator amends",
		"changes":       map[string]interface{}{"body": "Authorized body"},
	}
	bodyBytes, _ = json.Marshal(payloadOrch)
	req, _ = http.NewRequest("PATCH", fmt.Sprintf("%s/api/board/proj/tasks/%d/amend", server.URL, task.ID), bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var amended board.Task
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&amended))
	assert.Equal(t, 2, amended.Revision)
	assert.Equal(t, "Authorized body", *amended.Body)
}

func TestTask1670_HTTPLegacyPatchBodyBypassPrevention(t *testing.T) {
	server, handler, bs := setupTask1670Server(t)
	ctx := context.Background()

	registerTaskPlanner(t, handler, "proj", "Orchestrator")
	task, err := bs.CreateTaskWithOpts(ctx, "proj", "Original Title", "Original Body", "medium", "Operator", nil, "Worker")
	require.NoError(t, err)

	// 1. Attempting to update body via legacy PATCH /tasks/{taskID} must return 409 Conflict
	legacyPatch := map[string]interface{}{
		"subscriber_id": "Orchestrator",
		"body":          "Bypass attempt",
	}
	bodyBytes, _ := json.Marshal(legacyPatch)
	req, _ := http.NewRequest("PATCH", fmt.Sprintf("%s/api/board/proj/tasks/%d", server.URL, task.ID), bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)

	var errResp map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	assert.Contains(t, errResp["error"], "task body changes require the revisioned /amend endpoint")

	// 2. Updating title or priority via legacy PATCH works cleanly without altering revision or body
	titleUpdate := map[string]interface{}{
		"subscriber_id": "Orchestrator",
		"title":         "Updated Title",
	}
	bodyBytes, _ = json.Marshal(titleUpdate)
	req, _ = http.NewRequest("PATCH", fmt.Sprintf("%s/api/board/proj/tasks/%d", server.URL, task.ID), bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	detail, err := bs.GetTask(ctx, "proj", task.ID)
	require.NoError(t, err)
	assert.Equal(t, "Updated Title", detail.Title)
	assert.Equal(t, "Original Body", *detail.Body)
	assert.Equal(t, 1, detail.Revision)
}

func TestTask1670_HTTPCompletionAndReviewStaleRevision409(t *testing.T) {
	server, handler, bs := setupTask1670Server(t)
	ctx := context.Background()

	registerTaskPlanner(t, handler, "proj", "Orchestrator")
	_, err := bs.Subscribe(ctx, "proj", "Worker", "Worker", "worker-sess", nil, nil, "mentions")
	require.NoError(t, err)

	task, err := bs.CreateTaskWithOpts(ctx, "proj", "Task", "Body", "medium", "Operator", nil, "Worker")
	require.NoError(t, err)

	// Claim task
	_, err = bs.ClaimTask(ctx, "proj", "Worker")
	require.NoError(t, err)

	// Amend task to revision 2
	_, err = bs.AmendTask(ctx, "proj", task.ID, "Orchestrator", 1, "update", map[string]interface{}{"body": "New Body"})
	require.NoError(t, err)

	// 1. Completion with stale revision (expected_revision: 1) -> 409 Conflict
	staleComp := map[string]interface{}{
		"subscriber_id":     "Worker",
		"outcome":           "success",
		"expected_revision": 1,
	}
	bodyBytes, _ := json.Marshal(staleComp)
	req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/board/proj/tasks/%d/complete", server.URL, task.ID), bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)

	// 2. Completion with matching revision (expected_revision: 2) -> 200 OK
	matchingComp := map[string]interface{}{
		"subscriber_id":     "Worker",
		"outcome":           "success",
		"expected_revision": 2,
	}
	bodyBytes, _ = json.Marshal(matchingComp)
	req, _ = http.NewRequest("POST", fmt.Sprintf("%s/api/board/proj/tasks/%d/complete", server.URL, task.ID), bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	finalDetail, err := bs.GetTask(ctx, "proj", task.ID)
	require.NoError(t, err)
	assert.Equal(t, "completed", finalDetail.Status)
}

func TestTask1670_HTTPNoticeEmissionOnAmend(t *testing.T) {
	server, handler, bs := setupTask1670Server(t)
	ctx := context.Background()

	registerTaskPlanner(t, handler, "proj", "Orchestrator")
	_, err := bs.Subscribe(ctx, "proj", "Worker", "Worker", "worker-sess", nil, nil, "all")
	require.NoError(t, err)

	task, err := bs.CreateTaskWithOpts(ctx, "proj", "Feature X", "Body", "medium", "Operator", nil, "Worker")
	require.NoError(t, err)

	// Amend via HTTP
	payload := map[string]interface{}{
		"subscriber_id": "Orchestrator",
		"base_revision": 1,
		"reason":        "Update requirements",
		"changes":       map[string]interface{}{"body": "Updated Feature X Body"},
	}
	bodyBytes, _ := json.Marshal(payload)
	req, _ := http.NewRequest("PATCH", fmt.Sprintf("%s/api/board/proj/tasks/%d/amend", server.URL, task.ID), bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Give background notice goroutine a moment to commit
	time.Sleep(50 * time.Millisecond)

	// Check messages on the board
	msgs, err := bs.ReadMessages(ctx, "proj", "Worker", 10)
	require.NoError(t, err)
	require.NotEmpty(t, msgs)

	foundNotice := false
	expectedSubstring := fmt.Sprintf("[Task #%d amended to revision 2] Feature X — @Worker reread task detail before acting", task.ID)
	for _, m := range msgs {
		if m.SubscriberID == "Coral Task Queue" && m.Content == expectedSubstring {
			foundNotice = true
			break
		}
	}
	assert.True(t, foundNotice, "expected queue notice not found in messages: %+v", msgs)
}
