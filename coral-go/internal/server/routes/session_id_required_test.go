package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
)

// #3211 part 2: every per-agent route that reaches a terminal names its session. A bare agent
// or folder name is shared by teammates and would reach whichever terminal matched it.
func TestBareNameRoutesRequireSessionID(t *testing.T) {
	server, _, terminal, _ := setupSessionsTestServer(t)
	terminal.addSession("team", "/tmp/team") // a teammate's pane the bare name would match

	for _, c := range []struct{ method, path, body string }{
		{"GET", "", ""},
		{"GET", "/capture", ""},
		{"GET", "/poll", ""},
		{"GET", "/info", ""},
		{"GET", "/prompt-options", ""},
		{"GET", "/capture?agent_type=claude", ""},
		{"POST", "/kill", `{}`},
		{"POST", "/kill", `{"agent_type":"claude"}`},
		{"POST", "/restart", `{"agent_type":"claude"}`},
		{"POST", "/resume", `{"session_id":"11111111-2222-4333-8444-555555555555","agent_type":"claude"}`},
		{"POST", "/attach", `{"agent_type":"claude"}`},
	} {
		req, err := http.NewRequest(c.method, server.URL+"/api/sessions/live/team"+c.path, bytes.NewBufferString(c.body))
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		var got map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		resp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "%s %s", c.method, c.path)
		assert.Equal(t, "session_id_required", got["code"], "%s %s", c.method, c.path)
	}

	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	assert.Empty(t, terminal.killSessionCalls, "no terminal was killed")
	assert.Empty(t, terminal.captures, "no screen was read")
	assert.NotNil(t, terminal.sessions["team"], "the teammate's pane is untouched")
}

// The agent's exact terminal name ("claude-<uuid>") names one session, so it passes the gate.
func TestExactTerminalNameNeedsNoSessionID(t *testing.T) {
	server, _, terminal, _ := setupSessionsTestServer(t)
	name := "claude-00000000-0000-0000-0000-0000000000e1"
	terminal.addSession(name, "/tmp/e1")
	terminal.outputs[name] = "screen"
	resp, err := http.Get(server.URL + "/api/sessions/live/" + name + "/capture")
	require.NoError(t, err)
	defer resp.Body.Close()
	var got map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "screen", got["capture"])
}

// The terminal WebSocket by bare name closes with 1008 before any attach: no output, no input channel.
func TestWSTerminalBareNameIsRefused(t *testing.T) {
	server, _ := setupTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, q := range []string{"", "?agent_type=claude"} {
		conn, _, err := websocket.Dial(ctx, "ws"+server.URL[4:]+"/ws/terminal/team"+q, nil)
		require.NoError(t, err)
		_, _, err = conn.Read(ctx)
		assert.Equal(t, websocket.StatusPolicyViolation, websocket.CloseStatus(err), q)
		var ce websocket.CloseError
		if assert.ErrorAs(t, err, &ce) {
			assert.Equal(t, "session_id is required", ce.Reason)
		}
		conn.CloseNow()
	}
}
