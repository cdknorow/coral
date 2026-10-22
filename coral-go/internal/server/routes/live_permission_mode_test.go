package routes

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// The mode a Claude session's CLI is in now rides on its hook events (permission_mode) and shows in the live rows as live_permission_mode: kept only from that session's own hooks, for a Claude
// agent, as one of the six words the CLI has; absent for other agents and before the first event.
func TestLivePermissionModeComesFromTheAgentsOwnHooks(t *testing.T) {
	server, h, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	claudeSID, codexSID := "aaaaaaaa-5951-0000-0000-000000000001", "bbbbbbbb-5951-0000-0000-000000000002"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: claudeSID, AgentType: "claude", AgentName: "cl", WorkingDir: "/tmp/x"}))
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: codexSID, AgentType: "codex", AgentName: "cx", WorkingDir: "/tmp/y"}))
	tokClaude := agentToken(t, h, claudeSID, "claude-"+claudeSID)
	tokCodex := agentToken(t, h, codexSID, "codex-"+codexSID)
	post := func(name, body string, hdr map[string]string) int {
		req, _ := http.NewRequest("POST", server.URL+"/api/sessions/live/"+name+"/events", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	ev := func(mode string) string {
		return `{"event_type":"prompt_submit","summary":"User submitted prompt","permission_mode":"` + mode + `"}`
	}
	asClaude, asCodex := map[string]string{store.SessionTokenHeader: tokClaude}, map[string]string{store.SessionTokenHeader: tokCodex}

	assert.Nil(t, h.liveMode(claudeSID), "absent before the first event")
	require.Equal(t, http.StatusOK, post("cl", ev("acceptEdits"), asClaude))
	assert.Equal(t, "acceptEdits", h.liveMode(claudeSID))
	require.Equal(t, http.StatusOK, post("cl", ev("auto"), asClaude))
	assert.Equal(t, "auto", h.liveMode(claudeSID), "the latest event wins: Shift+Tab moved it")
	for _, bad := range []string{"", "AUTO", "yolo", "plan ", "default\n"} {
		post("cl", ev(bad), asClaude)
		assert.Equal(t, "auto", h.liveMode(claudeSID), "%q is dropped, the last good word stays", bad)
	}
	// no token: refused before anything is kept
	assert.Equal(t, http.StatusUnauthorized, post("cl", ev("plan"), nil))
	assert.Equal(t, "auto", h.liveMode(claudeSID), "a request without the agent's token never moves it")
	// another agent type: the word is not kept
	require.Equal(t, http.StatusOK, post("cx", ev("plan"), asCodex))
	assert.Nil(t, h.liveMode(codexSID), "only a Claude agent has one")
	// the session ends: its mode goes with it
	require.Equal(t, http.StatusOK, post("cl", `{"event_type":"session_end","summary":"Session ended","permission_mode":"plan"}`, asClaude))
	assert.Nil(t, h.liveMode(claudeSID), "an ended session keeps no mode")
	// A's token cannot set B's
	assert.Equal(t, http.StatusForbidden, post("cl", `{"event_type":"prompt_submit","summary":"x","session_id":"`+codexSID+`","permission_mode":"plan"}`, asClaude))
	assert.Nil(t, h.liveMode(codexSID))
}
