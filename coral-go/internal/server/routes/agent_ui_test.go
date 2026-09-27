package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cdknorow/coral/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

func TestAgentUIRoundTrip(t *testing.T) {
	_, h, _, ss := setupSessionsTestServer(t)
	for _, sid := range []string{"ui-a", "ui-b"} {
		require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentName: sid, AgentType: "codex", WorkingDir: "/tmp"}))
	}
	r := chi.NewRouter()
	h.RegisterAgentUI(r)
	server := httptest.NewServer(r)
	defer server.Close()
	call := func(method, path string, body any, want int) []byte {
		t.Helper()
		data, err := json.Marshal(body)
		require.NoError(t, err)
		req, err := http.NewRequest(method, server.URL+path, bytes.NewReader(data))
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		out, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, want, resp.StatusCode, string(out))
		return out
	}
	path := "/api/agent/ui/demo?session_id=ui-a"
	call("GET", "/api/agent/ui?session_id=unknown", nil, 404)
	body := map[string]string{"title": "Demo", "html": "<button>Choose</button>"}
	var p store.AgentUIPanel
	require.NoError(t, json.Unmarshal(call("PUT", path, body, 200), &p))
	require.EqualValues(t, 1, p.Revision)
	call("GET", "/api/agent/ui/demo/events?session_id=ui-b", nil, 404)
	events := "/api/agent/ui/demo/events?session_id=ui-a"
	event := map[string]any{"revision": 1, "action": "choose", "payload": map[string]string{"option": "A"}}
	call("POST", events, event, 201)
	rows := call("GET", events+"&after=0", nil, 200)
	require.Contains(t, string(rows), `"option":"A"`)
	var parsed []map[string]any
	require.NoError(t, json.Unmarshal(rows, &parsed))
	require.Len(t, parsed, 1)
	call("PUT", path, body, 200)
	call("POST", events, event, 409)
	call("GET", "/api/agent/ui/demo/content?session_id=ui-a&revision=1", nil, 409)
	resp, err := http.Get(server.URL + "/api/agent/ui/demo/content?session_id=ui-a&revision=2")
	require.NoError(t, err)
	content, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)
	require.Contains(t, resp.Header.Get("Content-Security-Policy"), "sandbox allow-scripts")
	require.NotContains(t, resp.Header.Get("Content-Security-Policy"), "allow-same-origin")
	require.Contains(t, string(content), "window.coralUI")
	call("GET", events+"&after=-1", nil, 400)
	event["revision"] = 2
	event["payload"] = strings.Repeat("x", 17000)
	call("POST", events, event, 400)
	body["html"] = strings.Repeat("x", (2<<20)+1)
	call("PUT", path, body, 400)
	call("DELETE", path, nil, 200)
	call("GET", events, nil, 404)
	body["html"] = "new"
	call("PUT", path, body, 200)
	require.JSONEq(t, "[]", string(call("GET", events, nil, 200)))
	require.JSONEq(t, "[]", string(call("GET", "/api/agent/ui?session_id=ui-b", nil, 200)))
}

func TestAgentUIActionNotifications(t *testing.T) {
	_, h, terminal, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: "ui-notify", AgentName: "ui-notify-agent", AgentType: "codex", WorkingDir: "/tmp"}))
	require.NoError(t, terminal.CreateSession(ctx, "ui-notify-agent", "/tmp"))
	_, err := h.db.PutAgentUI(ctx, "ui-notify", "-diagram", "Untrusted title", `<p>Hi</p>`)
	require.NoError(t, err)
	r := chi.NewRouter()
	h.RegisterAgentUI(r)
	post := func(revision int) (int, map[string]any) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"revision": revision, "action": "choose", "payload": map[string]string{"text": "PAYLOAD_MUST_NOT_APPEAR_IN_PROMPT"}})
		req := httptest.NewRequest("POST", "/api/agent/ui/-diagram/events?session_id=ui-notify", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return rec.Code, out
	}
	code, result := post(1)
	require.Equal(t, 201, code)
	require.Equal(t, true, result["notified"])
	require.Len(t, terminal.sent["ui-notify-agent"], 1)
	notice := terminal.sent["ui-notify-agent"][0]
	require.Contains(t, notice, "[Coral UI action #1]")
	require.Contains(t, notice, "revision 1")
	require.Contains(t, notice, "coral-agent ui events --id=-diagram --after=0")
	require.NotContains(t, notice, "PAYLOAD_MUST_NOT_APPEAR_IN_PROMPT")
	require.NotContains(t, notice, "Untrusted title")
	code, _ = post(2)
	require.Equal(t, 409, code)
	require.Len(t, terminal.sent["ui-notify-agent"], 1)
	require.NoError(t, ss.SetSessionSleeping(ctx, "ui-notify", true))
	code, result = post(1)
	require.Equal(t, 201, code)
	require.Equal(t, false, result["notified"])
	require.Equal(t, "agent is sleeping", result["notify_error"])
	require.Len(t, terminal.sent["ui-notify-agent"], 1)
	require.NoError(t, ss.SetSessionSleeping(ctx, "ui-notify", false))
	delete(terminal.sessions, "ui-notify-agent")
	code, result = post(1)
	require.Equal(t, 201, code)
	require.Equal(t, false, result["notified"])
	require.Equal(t, "could not reach agent terminal", result["notify_error"])
	events, err := h.db.AgentUIEvents(ctx, "ui-notify", "-diagram", 0)
	require.NoError(t, err)
	require.Len(t, events, 3)
	// A terminal is a shell, not an agent: never type a notification into it.
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: "ui-shell", AgentName: "ui-shell", AgentType: "terminal", WorkingDir: "/tmp"}))
	require.NoError(t, terminal.CreateSession(ctx, "ui-shell", "/tmp"))
	require.Equal(t, "session is not an agent", h.notifyAgentUI("ui-shell", "diagram", 1, 10))
	require.Empty(t, terminal.sent["ui-shell"])
}
