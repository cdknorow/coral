package routes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/board"
)

func setupBoardTestServer(t *testing.T) (*httptest.Server, *BoardHandler) {
	t.Helper()

	dbPath := t.TempDir() + "/board_test.db"
	bs, err := board.NewStore(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })

	handler := NewBoardHandler(bs)

	r := chi.NewRouter()
	r.Get("/api/board/projects", handler.ListProjects)
	r.Post("/api/board/{project}/subscribe", handler.Subscribe)
	r.Delete("/api/board/{project}/subscribe", handler.Unsubscribe)
	r.Post("/api/board/{project}/messages", handler.PostMessage)
	r.Get("/api/board/{project}/messages", handler.ReadMessages)
	r.Get("/api/board/{project}/messages/check", handler.CheckUnread)
	r.Get("/api/board/{project}/messages/all", handler.ListAllMessages)
	r.Delete("/api/board/{project}/messages/{messageID}", handler.DeleteMessage)
	r.Get("/api/board/{project}/subscribers", handler.ListSubscribers)
	r.Post("/api/board/{project}/tasks", handler.CreateTask)
	r.Get("/api/board/{project}/tasks", handler.ListTasks)
	r.Post("/api/board/{project}/tasks/claim", handler.ClaimTask)
	r.Patch("/api/board/{project}/tasks/{taskID}", handler.UpdateTask)
	r.Post("/api/board/{project}/tasks/{taskID}/complete", handler.CompleteTaskByID)
	r.Post("/api/board/{project}/tasks/{taskID}/cancel", handler.CancelTaskByID)
	r.Post("/api/board/{project}/tasks/{taskID}/publish", handler.PublishTask)
	r.Post("/api/board/{project}/tasks/{taskID}/reassign", handler.ReassignTask)
	r.Post("/api/board/{project}/pause", handler.PauseBoard)
	r.Post("/api/board/{project}/resume", handler.ResumeBoard)
	r.Get("/api/board/{project}/paused", handler.GetPaused)
	r.Delete("/api/board/{project}", handler.DeleteBoard)

	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	return server, handler
}

func postJSON(t *testing.T, url string, payload any) *http.Response {
	t.Helper()
	body, _ := json.Marshal(payload)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	return resp
}

func TestBoardPauseResume(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/testproject"

	// Initially not paused
	resp, err := http.Get(base + "/paused")
	require.NoError(t, err)
	defer resp.Body.Close()
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	assert.Equal(t, false, body["paused"])

	// Pause the board
	resp2 := postJSON(t, base+"/pause", nil)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusOK, resp2.StatusCode)
	var pauseBody map[string]any
	json.NewDecoder(resp2.Body).Decode(&pauseBody)
	assert.Equal(t, true, pauseBody["paused"])

	// Verify paused
	resp3, err := http.Get(base + "/paused")
	require.NoError(t, err)
	defer resp3.Body.Close()
	var body2 map[string]any
	json.NewDecoder(resp3.Body).Decode(&body2)
	assert.Equal(t, true, body2["paused"])

	// Resume
	resp4 := postJSON(t, base+"/resume", nil)
	defer resp4.Body.Close()
	assert.Equal(t, http.StatusOK, resp4.StatusCode)
	var resumeBody map[string]any
	json.NewDecoder(resp4.Body).Decode(&resumeBody)
	assert.Equal(t, false, resumeBody["paused"])

	// Verify not paused
	resp5, err := http.Get(base + "/paused")
	require.NoError(t, err)
	defer resp5.Body.Close()
	var body3 map[string]any
	json.NewDecoder(resp5.Body).Decode(&body3)
	assert.Equal(t, false, body3["paused"])
}

func TestBoardPause_ReadMessagesReturnsEmpty(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/testproject"

	// Subscribe a session
	resp := postJSON(t, base+"/subscribe", map[string]string{
		"session_id": "sess-1",
		"job_title":  "Dev",
	})
	resp.Body.Close()

	// Post a message
	resp2 := postJSON(t, base+"/messages", map[string]string{
		"session_id": "sess-1",
		"content":    "Hello world",
	})
	resp2.Body.Close()

	// Pause the board
	resp3 := postJSON(t, base+"/pause", nil)
	resp3.Body.Close()

	// ReadMessages should return empty array when paused
	resp4, err := http.Get(base + "/messages?session_id=sess-1")
	require.NoError(t, err)
	defer resp4.Body.Close()

	var messages []any
	json.NewDecoder(resp4.Body).Decode(&messages)
	assert.Empty(t, messages)
}

func TestBoardPause_CheckUnreadReturnsZero(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/testproject"

	// Subscribe two sessions
	resp := postJSON(t, base+"/subscribe", map[string]string{
		"session_id": "sess-1",
		"job_title":  "Dev",
	})
	resp.Body.Close()
	resp2 := postJSON(t, base+"/subscribe", map[string]string{
		"session_id": "sess-2",
		"job_title":  "QA",
	})
	resp2.Body.Close()

	// Post from sess-1
	resp3 := postJSON(t, base+"/messages", map[string]string{
		"session_id": "sess-1",
		"content":    "Hello",
	})
	resp3.Body.Close()

	// Pause
	resp4 := postJSON(t, base+"/pause", nil)
	resp4.Body.Close()

	// CheckUnread for sess-2 should return 0 when paused
	resp5, err := http.Get(base + "/messages/check?session_id=sess-2")
	require.NoError(t, err)
	defer resp5.Body.Close()

	var body map[string]any
	json.NewDecoder(resp5.Body).Decode(&body)
	assert.Equal(t, float64(0), body["unread"])
}

func TestBoardDelete_ClearsPauseState(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/testproject"

	// Pause
	resp := postJSON(t, base+"/pause", nil)
	resp.Body.Close()

	// Delete the board
	req, _ := http.NewRequest("DELETE", base, nil)
	resp2, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusOK, resp2.StatusCode)

	// Paused state should be cleared
	resp3, err := http.Get(base + "/paused")
	require.NoError(t, err)
	defer resp3.Body.Close()
	var body map[string]any
	json.NewDecoder(resp3.Body).Decode(&body)
	assert.Equal(t, false, body["paused"])
}

func TestBoardSubscribeAndPost(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	// Subscribe
	resp := postJSON(t, base+"/subscribe", map[string]string{
		"session_id": "sess-abc",
		"job_title":  "Backend Dev",
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// List subscribers
	resp2, err := http.Get(base + "/subscribers")
	require.NoError(t, err)
	defer resp2.Body.Close()

	var subs []map[string]any
	json.NewDecoder(resp2.Body).Decode(&subs)
	assert.Len(t, subs, 1)
	assert.Equal(t, "Backend Dev", subs[0]["job_title"])

	// Post message
	resp3 := postJSON(t, base+"/messages", map[string]string{
		"session_id": "sess-abc",
		"content":    "Test message",
	})
	defer resp3.Body.Close()
	assert.Equal(t, http.StatusOK, resp3.StatusCode)

	var msg map[string]any
	json.NewDecoder(resp3.Body).Decode(&msg)
	assert.Equal(t, "Test message", msg["content"])
	assert.NotEmpty(t, msg["id"])

	// List all messages (dashboard format)
	resp4, err := http.Get(base + "/messages/all?format=dashboard")
	require.NoError(t, err)
	defer resp4.Body.Close()

	var allBody map[string]any
	json.NewDecoder(resp4.Body).Decode(&allBody)
	assert.Equal(t, float64(1), allBody["total"])
}

func TestBoardPostMessage_EmptyContent(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	resp := postJSON(t, base+"/messages", map[string]string{
		"session_id": "sess-1",
		"content":    "",
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestBoardSubscribe_MissingSessionID(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	resp := postJSON(t, base+"/subscribe", map[string]string{
		"job_title": "Dev",
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestBoardListProjects(t *testing.T) {
	server, _ := setupBoardTestServer(t)

	// Subscribe to two different projects
	resp := postJSON(t, server.URL+"/api/board/project1/subscribe", map[string]string{
		"session_id": "s1",
		"job_title":  "Dev",
	})
	resp.Body.Close()
	resp2 := postJSON(t, server.URL+"/api/board/project2/subscribe", map[string]string{
		"session_id": "s2",
		"job_title":  "QA",
	})
	resp2.Body.Close()

	// List projects
	resp3, err := http.Get(server.URL + "/api/board/projects")
	require.NoError(t, err)
	defer resp3.Body.Close()

	var projects []map[string]any
	json.NewDecoder(resp3.Body).Decode(&projects)
	assert.Len(t, projects, 2)
}

func TestBoardCreateTask_DefersAssigneeNotificationWhenBusy(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	resp := postJSON(t, base+"/subscribe", map[string]string{
		"subscriber_id": "Backend Dev",
		"job_title":     "Backend Dev",
	})
	resp.Body.Close()

	resp = postJSON(t, base+"/tasks", map[string]string{
		"title":       "Existing task",
		"created_by":  "Orchestrator",
		"assigned_to": "Backend Dev",
	})
	resp.Body.Close()

	resp = postJSON(t, base+"/tasks/claim", map[string]string{
		"subscriber_id": "Backend Dev",
	})
	resp.Body.Close()

	resp = postJSON(t, base+"/tasks", map[string]string{
		"title":       "Follow-up task",
		"created_by":  "Orchestrator",
		"assigned_to": "Backend Dev",
		"priority":    "high",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	require.Eventually(t, func() bool {
		msgResp, err := http.Get(base + "/messages/all?format=dashboard")
		require.NoError(t, err)
		defer msgResp.Body.Close()
		var body map[string]any
		require.NoError(t, json.NewDecoder(msgResp.Body).Decode(&body))
		messages, ok := body["messages"].([]any)
		if !ok || len(messages) == 0 {
			return false
		}
		last, ok := messages[len(messages)-1].(map[string]any)
		if !ok {
			return false
		}
		content, _ := last["content"].(string)
		return content == "[Task #2 (high) assigned to Backend Dev — notification deferred while they have an active task] Follow-up task"
	}, 2*time.Second, 50*time.Millisecond)
}

func TestBoardReassignTask_DefersAssigneeNotificationWhenBusy(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	resp := postJSON(t, base+"/subscribe", map[string]string{
		"subscriber_id": "Backend Dev",
		"job_title":     "Backend Dev",
	})
	resp.Body.Close()

	resp = postJSON(t, base+"/tasks", map[string]string{
		"title":       "Existing task",
		"created_by":  "Orchestrator",
		"assigned_to": "Backend Dev",
	})
	resp.Body.Close()

	resp = postJSON(t, base+"/tasks/claim", map[string]string{
		"subscriber_id": "Backend Dev",
	})
	resp.Body.Close()

	resp = postJSON(t, base+"/tasks", map[string]string{
		"title":      "Unassigned task",
		"created_by": "Orchestrator",
	})
	resp.Body.Close()

	resp = postJSON(t, base+"/tasks/2/reassign", map[string]string{
		"subscriber_id": "Orchestrator",
		"assignee":      "Backend Dev",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	require.Eventually(t, func() bool {
		msgResp, err := http.Get(base + "/messages/all?format=dashboard")
		require.NoError(t, err)
		defer msgResp.Body.Close()
		var body map[string]any
		require.NoError(t, json.NewDecoder(msgResp.Body).Decode(&body))
		messages, ok := body["messages"].([]any)
		if !ok || len(messages) == 0 {
			return false
		}
		last, ok := messages[len(messages)-1].(map[string]any)
		if !ok {
			return false
		}
		content, _ := last["content"].(string)
		return content == "[Task #2 reassigned to Backend Dev — notification deferred while they have an active task] Unassigned task"
	}, 2*time.Second, 50*time.Millisecond)
}

func boardMessagesContain(t *testing.T, base, needle string) bool {
	t.Helper()
	msgResp, err := http.Get(base + "/messages/all?format=dashboard")
	require.NoError(t, err)
	defer msgResp.Body.Close()

	var body map[string]any
	require.NoError(t, json.NewDecoder(msgResp.Body).Decode(&body))
	messages, ok := body["messages"].([]any)
	if !ok {
		return false
	}
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		content, _ := msg["content"].(string)
		if strings.Contains(content, needle) {
			return true
		}
	}
	return false
}

func TestBoardCompleteTask_DoesNotNudgeOrchestratorForUnassignedTask(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	resp := postJSON(t, base+"/subscribe", map[string]string{
		"subscriber_id": "Orchestrator",
		"job_title":     "Orchestrator",
	})
	resp.Body.Close()

	resp = postJSON(t, base+"/tasks", map[string]string{
		"title":       "Coordinate work",
		"created_by":  "Operator",
		"assigned_to": "Orchestrator",
	})
	resp.Body.Close()

	resp = postJSON(t, base+"/tasks/claim", map[string]string{
		"subscriber_id": "Orchestrator",
	})
	resp.Body.Close()

	resp = postJSON(t, base+"/tasks", map[string]string{
		"title":      "Unassigned implementation",
		"created_by": "Orchestrator",
	})
	resp.Body.Close()

	resp = postJSON(t, base+"/tasks/1/complete", map[string]string{
		"subscriber_id": "Orchestrator",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	require.Eventually(t, func() bool {
		return boardMessagesContain(t, base, "[Task #1 completed by Orchestrator]")
	}, 2*time.Second, 50*time.Millisecond)
	assert.Never(t, func() bool {
		return boardMessagesContain(t, base, "@Orchestrator You have tasks available")
	}, 300*time.Millisecond, 50*time.Millisecond)
}

func TestBoardCompleteTask_NudgesWorkerForUnassignedTask(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	resp := postJSON(t, base+"/subscribe", map[string]string{
		"subscriber_id": "Backend Dev",
		"job_title":     "Backend Dev",
	})
	resp.Body.Close()

	resp = postJSON(t, base+"/tasks", map[string]string{
		"title":       "Initial implementation",
		"created_by":  "Orchestrator",
		"assigned_to": "Backend Dev",
	})
	resp.Body.Close()

	resp = postJSON(t, base+"/tasks/claim", map[string]string{
		"subscriber_id": "Backend Dev",
	})
	resp.Body.Close()

	resp = postJSON(t, base+"/tasks", map[string]string{
		"title":      "Unassigned follow-up",
		"created_by": "Orchestrator",
	})
	resp.Body.Close()

	resp = postJSON(t, base+"/tasks/1/complete", map[string]string{
		"subscriber_id": "Backend Dev",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	require.Eventually(t, func() bool {
		return boardMessagesContain(t, base, "@Backend Dev You have tasks available")
	}, 2*time.Second, 50*time.Millisecond)
}

func TestBoardCompleteTask_NotifiesOrchestrator(t *testing.T) {
	server, handler := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"
	terminal := newMockTerminal()
	terminal.addSession("claude-orchestrator", "/tmp/test")
	handler.SetTerminal(terminal)

	resp := postJSON(t, base+"/subscribe", map[string]string{
		"subscriber_id": "Orchestrator",
		"job_title":     "Orchestrator",
		"session_name":  "claude-orchestrator",
	})
	resp.Body.Close()

	resp = postJSON(t, base+"/subscribe", map[string]string{
		"subscriber_id": "Backend Dev",
		"job_title":     "Backend Dev",
	})
	resp.Body.Close()

	resp = postJSON(t, base+"/tasks", map[string]string{
		"title":       "Implement API",
		"created_by":  "Orchestrator",
		"assigned_to": "Backend Dev",
	})
	resp.Body.Close()

	resp = postJSON(t, base+"/tasks/claim", map[string]string{
		"subscriber_id": "Backend Dev",
	})
	resp.Body.Close()

	resp = postJSON(t, base+"/tasks/1/complete", map[string]string{
		"subscriber_id": "Backend Dev",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	require.Eventually(t, func() bool {
		for _, cmd := range terminal.sentCommands() {
			if strings.Contains(cmd, "Backend Dev finished task #1: Implement API") {
				return true
			}
		}
		return false
	}, 2*time.Second, 50*time.Millisecond)
}

func patchJSON(t *testing.T, url string, payload any) *http.Response {
	t.Helper()
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("PATCH", url, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func TestBoardUpdateTask_PartialEdit(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	// Create a task
	resp := postJSON(t, base+"/tasks", map[string]string{
		"title":      "Original title",
		"body":       "Original body",
		"priority":   "medium",
		"created_by": "Orchestrator",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var created map[string]any
	json.NewDecoder(resp.Body).Decode(&created)
	taskID := created["id"].(float64)

	// PATCH only the title
	resp2 := patchJSON(t, base+"/tasks/"+fmt.Sprintf("%.0f", taskID), map[string]string{
		"title": "Updated title",
	})
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	var updated map[string]any
	json.NewDecoder(resp2.Body).Decode(&updated)
	assert.Equal(t, "Updated title", updated["title"])
	assert.Equal(t, "Original body", updated["body"])
	assert.Equal(t, "medium", updated["priority"])

	// Verify audit message was posted
	require.Eventually(t, func() bool {
		msgResp, err := http.Get(base + "/messages/all?format=dashboard")
		require.NoError(t, err)
		defer msgResp.Body.Close()
		var body map[string]any
		json.NewDecoder(msgResp.Body).Decode(&body)
		messages, _ := body["messages"].([]any)
		for _, m := range messages {
			msg, _ := m.(map[string]any)
			content, _ := msg["content"].(string)
			if content == "[Task #1 edited] Updated title" {
				return true
			}
		}
		return false
	}, 2*time.Second, 50*time.Millisecond)
}

func TestBoardUpdateTask_CompletedTaskRejected(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	// Create and complete a task
	resp := postJSON(t, base+"/tasks", map[string]string{
		"title":      "To complete",
		"created_by": "Orchestrator",
	})
	resp.Body.Close()

	resp2 := postJSON(t, base+"/tasks/claim", map[string]string{
		"subscriber_id": "worker",
	})
	resp2.Body.Close()

	resp3 := postJSON(t, base+"/tasks/1/complete", map[string]string{
		"subscriber_id": "worker",
	})
	resp3.Body.Close()

	// PATCH should fail
	resp4 := patchJSON(t, base+"/tasks/1", map[string]string{
		"title": "Should fail",
	})
	defer resp4.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp4.StatusCode)
}

func TestBoardUpdateTask_InvalidTaskID(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	resp := patchJSON(t, base+"/tasks/abc", map[string]string{
		"title": "Nope",
	})
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestBoardCompleteTask_ViaHTTP(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	// Subscribe worker so claim works
	resp := postJSON(t, base+"/subscribe", map[string]string{
		"subscriber_id": "worker",
		"job_title":     "Dev",
	})
	resp.Body.Close()

	// Create task
	resp = postJSON(t, base+"/tasks", map[string]string{
		"title":      "Complete me",
		"created_by": "Orchestrator",
	})
	resp.Body.Close()

	// Claim it
	resp2 := postJSON(t, base+"/tasks/claim", map[string]string{
		"subscriber_id": "worker",
	})
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	var claimed map[string]any
	json.NewDecoder(resp2.Body).Decode(&claimed)
	require.Equal(t, "in_progress", claimed["status"])

	// Complete it with message
	resp3 := postJSON(t, base+"/tasks/1/complete", map[string]string{
		"subscriber_id": "worker",
		"message":       "All done",
	})
	defer resp3.Body.Close()
	require.Equal(t, http.StatusOK, resp3.StatusCode)
	var completed map[string]any
	json.NewDecoder(resp3.Body).Decode(&completed)
	assert.Equal(t, "completed", completed["status"])
	assert.Equal(t, "All done", completed["completion_message"])

	// Verify task shows completed in list
	resp4, err := http.Get(base + "/tasks")
	require.NoError(t, err)
	defer resp4.Body.Close()
	var listBody map[string]any
	json.NewDecoder(resp4.Body).Decode(&listBody)
	tasks, _ := listBody["tasks"].([]any)
	require.Len(t, tasks, 1)
	task0, _ := tasks[0].(map[string]any)
	assert.Equal(t, "completed", task0["status"])
}

func TestBoardCancelTask_ViaHTTP(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	// Create task
	resp := postJSON(t, base+"/tasks", map[string]string{
		"title":      "Cancel me",
		"created_by": "Orchestrator",
	})
	resp.Body.Close()

	// Cancel it
	resp2 := postJSON(t, base+"/tasks/1/cancel", map[string]string{
		"subscriber_id": "Operator",
	})
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	var cancelled map[string]any
	json.NewDecoder(resp2.Body).Decode(&cancelled)
	assert.Equal(t, "skipped", cancelled["status"])

	// Can't cancel again
	resp3 := postJSON(t, base+"/tasks/1/cancel", map[string]string{
		"subscriber_id": "Operator",
	})
	defer resp3.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp3.StatusCode)
}

// ── Dependency Tests (Route Level) ──────────────────────────────

func TestBoardCreateTask_WithBlockedBy_Shorthand(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	// Create blocker task
	resp := postJSON(t, base+"/tasks", map[string]string{
		"title":      "Blocker task",
		"created_by": "Orchestrator",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	// Create blocked task using shorthand [1]
	resp2, err := http.Post(base+"/tasks", "application/json",
		bytes.NewReader([]byte(`{"title":"Blocked task","created_by":"Orchestrator","blocked_by":[1]}`)))
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusCreated, resp2.StatusCode)
	var created map[string]any
	json.NewDecoder(resp2.Body).Decode(&created)
	assert.Equal(t, "blocked", created["status"])

	// Verify blocked_by is populated in list
	resp3, err := http.Get(base + "/tasks")
	require.NoError(t, err)
	defer resp3.Body.Close()
	var listBody map[string]any
	json.NewDecoder(resp3.Body).Decode(&listBody)
	tasks, _ := listBody["tasks"].([]any)
	require.Len(t, tasks, 2)

	// Find the blocked task
	for _, raw := range tasks {
		task, _ := raw.(map[string]any)
		if task["title"] == "Blocked task" {
			deps, _ := task["blocked_by"].([]any)
			require.Len(t, deps, 1)
			dep, _ := deps[0].(map[string]any)
			assert.Equal(t, float64(1), dep["task_id"])
		}
	}
}

func TestBoardCreateTask_WithBlockedBy_FullFormat(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	// Create blocker
	resp := postJSON(t, base+"/tasks", map[string]string{
		"title":      "Blocker",
		"created_by": "Orchestrator",
	})
	resp.Body.Close()

	// Create blocked task using full format
	resp2, err := http.Post(base+"/tasks", "application/json",
		bytes.NewReader([]byte(`{"title":"Blocked","created_by":"Orchestrator","blocked_by":[{"task_id":1,"board_id":"myproject"}]}`)))
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusCreated, resp2.StatusCode)
	var created map[string]any
	json.NewDecoder(resp2.Body).Decode(&created)
	assert.Equal(t, "blocked", created["status"])
}

func TestBoardCreateTask_BlockedByResolved_StartsPending(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	// Subscribe worker so claim works
	postJSON(t, base+"/subscribe", map[string]string{
		"subscriber_id": "worker", "job_title": "Dev",
	}).Body.Close()

	// Create, claim, complete a task
	postJSON(t, base+"/tasks", map[string]string{
		"title": "Blocker", "created_by": "Orchestrator",
	}).Body.Close()
	postJSON(t, base+"/tasks/claim", map[string]string{
		"subscriber_id": "worker",
	}).Body.Close()
	postJSON(t, base+"/tasks/1/complete", map[string]string{
		"subscriber_id": "worker",
	}).Body.Close()

	// Create task blocked by completed task — should start pending
	resp, err := http.Post(base+"/tasks", "application/json",
		bytes.NewReader([]byte(`{"title":"After blocker","created_by":"Orchestrator","blocked_by":[1]}`)))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var created map[string]any
	json.NewDecoder(resp.Body).Decode(&created)
	assert.Equal(t, "pending", created["status"])
}

func TestBoardCreateTask_CircularDependencyRejected(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	// Create A
	postJSON(t, base+"/tasks", map[string]string{
		"title": "Task A", "created_by": "Orchestrator",
	}).Body.Close()

	// Create B blocked by A
	resp, err := http.Post(base+"/tasks", "application/json",
		bytes.NewReader([]byte(`{"title":"Task B","created_by":"Orchestrator","blocked_by":[1]}`)))
	require.NoError(t, err)
	resp.Body.Close()

	// Try to make A blocked by B (circular)
	resp2 := patchJSON(t, base+"/tasks/1", map[string]any{
		"blocked_by": []int{2},
	})
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp2.StatusCode)
}

func TestBoardCompleteTask_UnblocksDownstream(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	postJSON(t, base+"/subscribe", map[string]string{
		"subscriber_id": "worker", "job_title": "Dev",
	}).Body.Close()

	// Create A, create B blocked by A
	postJSON(t, base+"/tasks", map[string]string{
		"title": "Task A", "created_by": "Orchestrator",
	}).Body.Close()

	resp, _ := http.Post(base+"/tasks", "application/json",
		bytes.NewReader([]byte(`{"title":"Task B","created_by":"Orchestrator","blocked_by":[1]}`)))
	resp.Body.Close()

	// Claim and complete A
	postJSON(t, base+"/tasks/claim", map[string]string{
		"subscriber_id": "worker",
	}).Body.Close()
	postJSON(t, base+"/tasks/1/complete", map[string]string{
		"subscriber_id": "worker",
	}).Body.Close()

	// Verify B is now pending
	resp2, err := http.Get(base + "/tasks")
	require.NoError(t, err)
	defer resp2.Body.Close()
	var listBody map[string]any
	json.NewDecoder(resp2.Body).Decode(&listBody)
	tasks, _ := listBody["tasks"].([]any)
	for _, raw := range tasks {
		task, _ := raw.(map[string]any)
		if task["title"] == "Task B" {
			assert.Equal(t, "pending", task["status"])
		}
	}
}

func TestBoardCancelTask_UnblocksDownstream(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	// Create A, create B blocked by A
	postJSON(t, base+"/tasks", map[string]string{
		"title": "Task A", "created_by": "Orchestrator",
	}).Body.Close()

	resp, _ := http.Post(base+"/tasks", "application/json",
		bytes.NewReader([]byte(`{"title":"Task B","created_by":"Orchestrator","blocked_by":[1]}`)))
	resp.Body.Close()

	// Cancel A — should unblock B
	postJSON(t, base+"/tasks/1/cancel", map[string]string{
		"subscriber_id": "Operator",
	}).Body.Close()

	// Verify B is now pending
	resp2, err := http.Get(base + "/tasks")
	require.NoError(t, err)
	defer resp2.Body.Close()
	var listBody map[string]any
	json.NewDecoder(resp2.Body).Decode(&listBody)
	tasks, _ := listBody["tasks"].([]any)
	for _, raw := range tasks {
		task, _ := raw.(map[string]any)
		if task["title"] == "Task B" {
			assert.Equal(t, "pending", task["status"])
		}
	}
}

func TestBoardUpdateTask_BlockedByViaPatcH(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	// Create A and B (both pending)
	postJSON(t, base+"/tasks", map[string]string{
		"title": "Task A", "created_by": "Orchestrator",
	}).Body.Close()
	postJSON(t, base+"/tasks", map[string]string{
		"title": "Task B", "created_by": "Orchestrator",
	}).Body.Close()

	// PATCH B to be blocked by A
	resp := patchJSON(t, base+"/tasks/2", map[string]any{
		"blocked_by": []int{1},
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var updated map[string]any
	json.NewDecoder(resp.Body).Decode(&updated)
	assert.Equal(t, "blocked", updated["status"])

	// Clear deps via PATCH
	resp2 := patchJSON(t, base+"/tasks/2", map[string]any{
		"blocked_by": []int{},
	})
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	var cleared map[string]any
	json.NewDecoder(resp2.Body).Decode(&cleared)
	assert.Equal(t, "pending", cleared["status"])
}

// ── Draft Tests (Route Level) ───────────────────────────────────

func TestBoardCreateDraftTask(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	resp, err := http.Post(base+"/tasks", "application/json",
		bytes.NewReader([]byte(`{"title":"Draft task","created_by":"Orchestrator","draft":true}`)))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var created map[string]any
	json.NewDecoder(resp.Body).Decode(&created)
	assert.Equal(t, "draft", created["status"])
}

func TestBoardCreateDraftTask_WithDeps(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	// Create blocker first
	postJSON(t, base+"/tasks", map[string]string{
		"title": "Blocker", "created_by": "Orch",
	}).Body.Close()

	// Create draft with deps — should stay draft (not evaluate deps)
	resp, err := http.Post(base+"/tasks", "application/json",
		bytes.NewReader([]byte(`{"title":"Draft with deps","created_by":"Orch","draft":true,"blocked_by":[1]}`)))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var created map[string]any
	json.NewDecoder(resp.Body).Decode(&created)
	assert.Equal(t, "draft", created["status"])
}

func TestBoardDraftCannotBeClaimed(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	postJSON(t, base+"/subscribe", map[string]string{
		"subscriber_id": "worker", "job_title": "Dev",
	}).Body.Close()

	// Create draft and a pending task
	http.Post(base+"/tasks", "application/json",
		bytes.NewReader([]byte(`{"title":"Draft","created_by":"Orch","draft":true,"priority":"high"}`)))
	postJSON(t, base+"/tasks", map[string]string{
		"title": "Pending", "created_by": "Orch", "priority": "low",
	}).Body.Close()

	// Claim should skip draft, pick pending
	resp := postJSON(t, base+"/tasks/claim", map[string]string{
		"subscriber_id": "worker",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var claimed map[string]any
	json.NewDecoder(resp.Body).Decode(&claimed)
	assert.Equal(t, "Pending", claimed["title"])
}

func TestBoardPublishDraft_NoDeps(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	// Create draft
	resp, _ := http.Post(base+"/tasks", "application/json",
		bytes.NewReader([]byte(`{"title":"Draft to publish","created_by":"Orch","draft":true}`)))
	var created map[string]any
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	taskID := fmt.Sprintf("%.0f", created["id"].(float64))

	// Publish
	resp2 := postJSON(t, base+"/tasks/"+taskID+"/publish", nil)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	var published map[string]any
	json.NewDecoder(resp2.Body).Decode(&published)
	assert.Equal(t, "pending", published["status"])
}

func TestBoardPublishDraft_WithUnresolvedDeps(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	// Create blocker
	postJSON(t, base+"/tasks", map[string]string{
		"title": "Blocker", "created_by": "Orch",
	}).Body.Close()

	// Create draft blocked by task 1
	resp, _ := http.Post(base+"/tasks", "application/json",
		bytes.NewReader([]byte(`{"title":"Draft blocked","created_by":"Orch","draft":true,"blocked_by":[1]}`)))
	var created map[string]any
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	assert.Equal(t, "draft", created["status"])
	taskID := fmt.Sprintf("%.0f", created["id"].(float64))

	// Publish — should become blocked (blocker unresolved)
	resp2 := postJSON(t, base+"/tasks/"+taskID+"/publish", nil)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	var published map[string]any
	json.NewDecoder(resp2.Body).Decode(&published)
	assert.Equal(t, "blocked", published["status"])
}

func TestBoardPublishNonDraft_Fails(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	postJSON(t, base+"/tasks", map[string]string{
		"title": "Pending task", "created_by": "Orch",
	}).Body.Close()

	resp := postJSON(t, base+"/tasks/1/publish", nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestBoardDraftCanBeCancelled(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	http.Post(base+"/tasks", "application/json",
		bytes.NewReader([]byte(`{"title":"Draft cancel","created_by":"Orch","draft":true}`)))

	resp := postJSON(t, base+"/tasks/1/cancel", map[string]string{
		"subscriber_id": "Orch",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var cancelled map[string]any
	json.NewDecoder(resp.Body).Decode(&cancelled)
	assert.Equal(t, "skipped", cancelled["status"])
}

func TestBoardDraftCanBeEdited(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"

	http.Post(base+"/tasks", "application/json",
		bytes.NewReader([]byte(`{"title":"Draft edit","created_by":"Orch","draft":true}`)))

	resp := patchJSON(t, base+"/tasks/1", map[string]any{
		"title":    "Updated draft",
		"priority": "high",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var updated map[string]any
	json.NewDecoder(resp.Body).Decode(&updated)
	assert.Equal(t, "Updated draft", updated["title"])
	assert.Equal(t, "high", updated["priority"])
	assert.Equal(t, "draft", updated["status"])
}
