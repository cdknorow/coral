package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/board"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBoardWaitRoutes_RegisterAndRetrieve(t *testing.T) {
	server, handler := setupBoardTestServer(t)
	project := "wait-test"

	// Subscribe agent
	_, err := handler.bs.Subscribe(context.Background(), project, "worker-1", "Developer", "tmux-w1", nil, nil, "all")
	require.NoError(t, err)

	// 1. Initially no active wait
	resp, err := http.Get(fmt.Sprintf("%s/api/board/%s/waits?subscriber_id=worker-1", server.URL, project))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var getOut struct {
		Wait *board.RegisteredWait `json:"wait"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&getOut))
	assert.Nil(t, getOut.Wait)

	// 2. Register wait
	reqBody, _ := json.Marshal(map[string]string{
		"subscriber_id": "worker-1",
		"wait_type":     "message",
		"target_id":     "Orchestrator",
		"reason":        "waiting for accepted design",
		"timeout":       "1h",
	})
	resp, err = http.Post(fmt.Sprintf("%s/api/board/%s/waits", server.URL, project), "application/json", bytes.NewReader(reqBody))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	var regOut board.RegisteredWait
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&regOut))
	assert.Equal(t, "worker-1", regOut.SubscriberID)
	assert.Equal(t, "message", regOut.WaitType)
	assert.Equal(t, "Orchestrator", regOut.TargetID)
	assert.Equal(t, "waiting for accepted design", regOut.Reason)
	assert.Equal(t, "active", regOut.Status)

	// 3. Query active wait by subscriber
	resp, err = http.Get(fmt.Sprintf("%s/api/board/%s/waits?subscriber_id=worker-1", server.URL, project))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var activeOut struct {
		Wait *board.RegisteredWait `json:"wait"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&activeOut))
	require.NotNil(t, activeOut.Wait)
	assert.Equal(t, "worker-1", activeOut.Wait.SubscriberID)
	assert.Equal(t, "Orchestrator", activeOut.Wait.TargetID)

	// 4. List all active waits on project
	resp, err = http.Get(fmt.Sprintf("%s/api/board/%s/waits", server.URL, project))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var listOut struct {
		Waits []board.RegisteredWait `json:"waits"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&listOut))
	require.Len(t, listOut.Waits, 1)
	assert.Equal(t, "worker-1", listOut.Waits[0].SubscriberID)
}

func TestBoardWaitRoutes_Cancel(t *testing.T) {
	server, handler := setupBoardTestServer(t)
	project := "wait-cancel"

	_, err := handler.bs.Subscribe(context.Background(), project, "worker-1", "Developer", "tmux-w1", nil, nil, "all")
	require.NoError(t, err)

	_, err = handler.bs.RegisterWait(context.Background(), project, "worker-1", "tmux-w1", "message", "Lead", "waiting for approval", time.Hour)
	require.NoError(t, err)

	// Cancel via DELETE
	req, err := http.NewRequest("DELETE", fmt.Sprintf("%s/api/board/%s/waits?subscriber_id=worker-1", server.URL, project), nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify it's no longer active
	active, err := handler.bs.GetActiveWait(context.Background(), project, "worker-1")
	require.NoError(t, err)
	assert.Nil(t, active)
}

func TestBoardWaitRoutes_MessageResolutionNotifiesTerminal(t *testing.T) {
	server, handler := setupBoardTestServer(t)
	mockTerm := newMockTerminal()
	mockTerm.addSession("tmux-w1", "/tmp/worker")
	handler.SetTerminal(mockTerm)

	project := "wait-notify"

	_, err := handler.bs.Subscribe(context.Background(), project, "Worker", "Developer", "tmux-w1", nil, nil, "all")
	require.NoError(t, err)
	_, err = handler.bs.Subscribe(context.Background(), project, "Orchestrator", "Lead", "tmux-orch", nil, nil, "all")
	require.NoError(t, err)

	// Worker registers wait for Orchestrator
	_, err = handler.bs.RegisterWait(context.Background(), project, "Worker", "tmux-w1", "message", "Orchestrator", "waiting for review", time.Hour)
	require.NoError(t, err)

	// Orchestrator posts message
	postBody, _ := json.Marshal(map[string]string{
		"subscriber_id": "Orchestrator",
		"content":       "Design approved, you can proceed.",
	})
	resp, err := http.Post(fmt.Sprintf("%s/api/board/%s/messages", server.URL, project), "application/json", bytes.NewReader(postBody))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify wait resolved in DB and mock terminal received wake-up notification
	require.Eventually(t, func() bool {
		active, _ := handler.bs.GetActiveWait(context.Background(), project, "Worker")
		if active != nil {
			return false
		}
		mockTerm.mu.Lock()
		defer mockTerm.mu.Unlock()
		return len(mockTerm.sent["tmux-w1"]) > 0
	}, 2*time.Second, 20*time.Millisecond)

	mockTerm.mu.Lock()
	inputs := mockTerm.sent["tmux-w1"]
	mockTerm.mu.Unlock()

	require.NotEmpty(t, inputs, "Worker should have received a terminal wake-up nudge")
	allInputs := strings.Join(inputs, "\n")
	assert.Contains(t, allInputs, "[Wait resolved]")
	assert.Contains(t, allInputs, "Orchestrator")
	assert.Contains(t, allInputs, "Design approved")
}

func TestBoardWaitRoutes_TaskResolutionNotifiesTerminal(t *testing.T) {
	server, handler := setupBoardTestServer(t)
	mockTerm := newMockTerminal()
	mockTerm.addSession("tmux-w1", "/tmp/worker")
	handler.SetTerminal(mockTerm)

	project := "wait-task-notify"

	_, err := handler.bs.Subscribe(context.Background(), project, "Worker", "Developer", "tmux-w1", nil, nil, "all")
	require.NoError(t, err)
	_, err = handler.bs.Subscribe(context.Background(), project, "Lead", "Lead", "tmux-lead", nil, nil, "all")
	require.NoError(t, err)

	// Create and claim a task
	task, err := handler.bs.CreateTask(context.Background(), project, "Design approval", "Design docs", "high", "lead", "Lead")
	require.NoError(t, err)
	_, err = handler.bs.ClaimTask(context.Background(), project, "Lead")
	require.NoError(t, err)

	// Worker registers wait for task
	taskIDStr := fmt.Sprintf("%d", task.ID)
	_, err = handler.bs.RegisterWait(context.Background(), project, "Worker", "tmux-w1", "task", taskIDStr, "waiting for design completion", time.Hour)
	require.NoError(t, err)

	// Complete task
	msg := "Design finished"
	completeBody, _ := json.Marshal(map[string]any{
		"subscriber_id": "Lead",
		"message":       &msg,
	})
	resp, err := http.Post(fmt.Sprintf("%s/api/board/%s/tasks/%d/complete", server.URL, project, task.ID), "application/json", bytes.NewReader(completeBody))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify wait resolved in DB and mock terminal received wake-up notification
	require.Eventually(t, func() bool {
		active, _ := handler.bs.GetActiveWait(context.Background(), project, "Worker")
		if active != nil {
			return false
		}
		mockTerm.mu.Lock()
		defer mockTerm.mu.Unlock()
		return len(mockTerm.sent["tmux-w1"]) > 0
	}, 2*time.Second, 20*time.Millisecond)

	mockTerm.mu.Lock()
	inputs := mockTerm.sent["tmux-w1"]
	mockTerm.mu.Unlock()

	require.NotEmpty(t, inputs, "Worker should have received a task completion terminal wake-up nudge")
	allInputs := strings.Join(inputs, "\n")
	assert.Contains(t, allInputs, "[Wait resolved]")
	assert.Contains(t, allInputs, fmt.Sprintf("Task #%d", task.ID))
	assert.Contains(t, allInputs, "completed")
}

func TestBoardWaitRoutes_PollWait(t *testing.T) {
	server, handler := setupBoardTestServer(t)
	project := "wait-poll"

	_, err := handler.bs.Subscribe(context.Background(), project, "Worker", "Developer", "tmux-w1", nil, nil, "all")
	require.NoError(t, err)
	_, err = handler.bs.Subscribe(context.Background(), project, "Lead", "Lead", "tmux-lead", nil, nil, "all")
	require.NoError(t, err)

	// Register wait
	_, err = handler.bs.RegisterWait(context.Background(), project, "Worker", "tmux-w1", "message", "Lead", "waiting for response", time.Hour)
	require.NoError(t, err)

	done := make(chan struct{})
	var pollRespStatus int
	var pollResult map[string]any

	go func() {
		resp, err := http.Get(fmt.Sprintf("%s/api/board/%s/waits/poll?subscriber_id=Worker&timeout=5", server.URL, project))
		if err == nil {
			pollRespStatus = resp.StatusCode
			json.NewDecoder(resp.Body).Decode(&pollResult)
			resp.Body.Close()
		}
		close(done)
	}()

	// Wait slightly, then resolve wait by posting message
	time.Sleep(100 * time.Millisecond)
	postBody, _ := json.Marshal(map[string]string{
		"subscriber_id": "Lead",
		"content":       "Here is the answer",
	})
	resp, err := http.Post(fmt.Sprintf("%s/api/board/%s/messages", server.URL, project), "application/json", bytes.NewReader(postBody))
	require.NoError(t, err)
	resp.Body.Close()

	select {
	case <-done:
		assert.Equal(t, http.StatusOK, pollRespStatus)
		assert.Equal(t, "resolved", pollResult["status"])
	case <-time.After(3 * time.Second):
		t.Fatal("poll did not return within expected time")
	}
}
