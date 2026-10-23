package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #6329: Edit reminder opens with the instruction in force, read by a person's GET and never part of the shared availability read or the Hub's view.
func TestTheReminderInForceIsReadByAPersonAlone(t *testing.T) {
	server, handler := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"
	for _, id := range []string{"Orchestrator", "Dev"} {
		resp := postJSON(t, base+"/subscribe", map[string]string{"subscriber_id": id, "job_title": id, "session_name": "claude-00000001-3864-4864-8864-00000000000" + map[string]string{"Orchestrator": "1", "Dev": "2"}[id]})
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}
	read := func(sub string) (int, map[string]any) {
		resp, err := http.Get(base + "/reminder?subscriber_id=" + sub)
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	code, out := read("Orchestrator")
	assert.Equal(t, 200, code)
	assert.Equal(t, map[string]any{"active": false}, out, "no reminder: nothing but that")

	resp := postJSON(t, base+"/reminder", map[string]any{"subscriber_id": "Orchestrator", "message": "Check the board.  Then report", "interval_seconds": 150})
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	code, out = read("Orchestrator")
	assert.Equal(t, 200, code)
	assert.Equal(t, map[string]any{"active": true, "subscriber_id": "Orchestrator", "message": "Check the board.  Then report", "interval_seconds": float64(150)}, out, "the exact interval and the text as stored")

	code, out = read("Dev")
	assert.Equal(t, map[string]any{"active": false}, out, "another agent's reminder is not this one's")
	resp, err := http.Get(base + "/reminder")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "a subscriber is required")
	other, err := http.Get(server.URL + "/api/board/otherproject/reminder?subscriber_id=Orchestrator")
	require.NoError(t, err)
	var o map[string]any
	_ = json.NewDecoder(other.Body).Decode(&o)
	other.Body.Close()
	assert.Equal(t, map[string]any{"active": false}, o, "nor another board's")

	// removed: the read says so (and the test leaves no reminder running)
	del, err := http.NewRequest(http.MethodDelete, base+"/reminder", strings.NewReader(`{"subscriber_id":"Orchestrator"}`))
	require.NoError(t, err)
	del.Header.Set("Content-Type", "application/json")
	dresp, err := http.DefaultClient.Do(del)
	require.NoError(t, err)
	dresp.Body.Close()
	require.Equal(t, http.StatusOK, dresp.StatusCode)
	_, out = read("Orchestrator")
	assert.Equal(t, map[string]any{"active": false}, out, "a removed reminder reads as none")

	// not a person: an agent's session token, and a request of no known kind, are refused whatever the reminder is
	for name, mod := range map[string]func(*http.Request){
		"an agent's session token": func(r *http.Request) { r.Header.Set(store.SessionTokenHeader, "agent-token") },
		"no caller kind":           func(r *http.Request) {},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/board/myproject/reminder?subscriber_id=Orchestrator", nil)
		req = req.WithContext(req.Context())
		mod(req)
		rec := httptest.NewRecorder()
		handler.GetSubscriberReminder(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code, name)
		assert.NotContains(t, rec.Body.String(), "Check the board", name+": the instruction is not in the refusal")
	}

	// never through the Hub
	assert.Equal(t, tunnel.TierNever, tunnel.Classify("GET", "/api/board/myproject/reminder", "subscriber_id=Orchestrator"))
}
