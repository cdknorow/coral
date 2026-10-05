package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/tracking"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Task outcomes are distinguished: first_task_completed keeps meaning "any
// completion", first_task_succeeded is the distinct success milestone.
func TestTaskCompletionReportsOutcomeAndSeparateSuccessMilestone(t *testing.T) {
	sink := captureAnalytics(t)
	server, handler, bs := setupTask1670Server(t)
	_ = handler
	ctx := context.Background()
	complete := func(title, outcome string) {
		task, err := bs.CreateTask(ctx, "proj", title, "", "medium", "lead")
		require.NoError(t, err)
		b, _ := json.Marshal(map[string]any{"subscriber_id": "lead", "outcome": outcome})
		resp, err := http.Post(server.URL+"/api/board/proj/tasks/"+itoa(task.ID)+"/complete", "application/json", bytes.NewReader(b))
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, 200, resp.StatusCode)
	}

	complete("fails", "failed")
	failed := sink.wait(t, tracking.EventTaskCompleted, 1)[0]
	assert.Equal(t, "failed", failed["outcome"])
	first := sink.wait(t, tracking.EventFirstTaskCompleted, 1)[0]
	assert.Equal(t, "failed", first["outcome"], "legacy milestone still fires on any completion")
	sink.none(t, tracking.EventFirstTaskSucceeded)

	complete("works", "success")
	sink.wait(t, tracking.EventFirstTaskSucceeded, 1)
	outcomes := []any{}
	for _, p := range sink.wait(t, tracking.EventTaskCompleted, 2) {
		outcomes = append(outcomes, p["outcome"])
	}
	assert.ElementsMatch(t, []any{"failed", "success"}, outcomes)
	assert.Len(t, sink.named(tracking.EventFirstTaskCompleted), 1, "first_task_completed stays once per install")
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

// first_prompt_submitted is emitted only when the terminal accepted the
// input over HTTP /send, once per install, and never carries the prompt.
func TestFirstPromptSubmittedOnlyAfterTheTerminalAcceptsHTTPSend(t *testing.T) {
	sink := captureAnalytics(t)
	_, handler, terminal, _ := setupSessionsTestServerWithConfig(t, config.Load(t.TempDir()))
	terminal.mu.Lock()
	terminal.sessions["agent-1"] = &ptymanager.PaneInfo{SessionName: "agent-1"}
	terminal.mu.Unlock()
	r := chi.NewRouter()
	r.Post("/api/sessions/live/{name}/send", handler.Send)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	// Unknown session: the terminal refuses, nothing is recorded.
	require.Equal(t, 500, postLaunch(t, srv.URL, "/api/sessions/live/missing/send", map[string]any{"command": "secret prompt text"}))
	sink.none(t, tracking.EventFirstPromptSubmitted)

	require.Equal(t, 200, postLaunch(t, srv.URL, "/api/sessions/live/agent-1/send", map[string]any{"command": "secret prompt text"}))
	p := sink.wait(t, tracking.EventFirstPromptSubmitted, 1)[0]
	assert.Equal(t, "http_send", p["source"])
	require.Equal(t, 200, postLaunch(t, srv.URL, "/api/sessions/live/agent-1/send", map[string]any{"command": "again"}))
	sink.none(t, tracking.EventPromptSubmitRequested)
	assert.Len(t, sink.named(tracking.EventFirstPromptSubmitted), 1, "once per install")
	assert.NotContains(t, sink.allRaw(), "secret prompt text")
}

// The browser route accepts only dashboard events and keeps server lifecycle
// events out; values are validated by the tracking package.
func TestBrowserRouteAcceptsDashboardEventsAndRejectsServerEvents(t *testing.T) {
	sink := captureAnalytics(t)
	post := func(body string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/tracking/event", bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		NewTrackingHandler(t.TempDir()).TrackEvent(rr, req)
		return rr.Code
	}
	for _, e := range []string{"launch_requested", "launch_result", "task_completed", "first_task_succeeded", "first_prompt_submitted", "app_opened"} {
		assert.Equal(t, 400, post(`{"event":"`+e+`","props":{"outcome":"success"}}`), e)
	}
	assert.Equal(t, 200, post(`{"event":"dashboard_ready","props":{"page_id":"9b2f6f0a-1111-4222-8333-444455556666"}}`))
	assert.Equal(t, 200, post(`{"event":"dashboard_failed","props":{"code":"sessions_fetch_http","path":"/Users/alice","error":"boom"}}`))
	assert.Equal(t, 200, post(`{"event":"prompt_submit_requested","props":{"source":"dashboard_composer","prompt":"hello"}}`))
	assert.Equal(t, 400, post(`{"event":"dashboard_ready","props":{"a":"1","b":"2","c":"3","d":"4","e":"5","f":"6","g":"7","h":"8","i":"9"}}`), "too many properties")

	ready := sink.wait(t, tracking.EventDashboardReady, 1)[0]
	assert.Equal(t, "9b2f6f0a-1111-4222-8333-444455556666", ready["page_id"])
	failedProps := sink.wait(t, tracking.EventDashboardFailed, 1)[0]
	assert.Equal(t, "sessions_fetch_http", failedProps["code"])
	sink.wait(t, tracking.EventPromptSubmitRequested, 1)
	raw := sink.allRaw()
	for _, leaked := range []string{"/Users/alice", "boom", "hello"} {
		assert.NotContains(t, raw, leaked)
	}
	_ = board.Task{}
}
