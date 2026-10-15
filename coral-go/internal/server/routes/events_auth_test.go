package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// #3479 (Security): an agent event is accepted only from that agent's own hooks, by its
// session token, in every mode. Another session's token, no credential, an unknown type
// or an oversized detail are refused; the dashboard may post only a goal edit.
func TestCreateEventIsBoundToTheAgentsToken(t *testing.T) {
	server, h, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	sidA, sidB := "aaaaaaaa-3479-0000-0000-000000000001", "bbbbbbbb-3479-0000-0000-000000000002"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sidA, AgentType: "claude", AgentName: "alpha", WorkingDir: "/tmp/shared"}))
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sidB, AgentType: "claude", AgentName: "beta", WorkingDir: "/tmp/shared"}))
	tokA := agentToken(t, h, sidA, "claude-"+sidA)

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
	asA := map[string]string{store.SessionTokenHeader: tokA}
	stop := `{"event_type":"stop","summary":"Agent stopped: unknown","session_id":"%s"}`
	needs := func(sid string) bool { return h.deriveSingleSessionState(ctx, sid, 0).NeedsInput }

	// No credential: refused, whatever it claims.
	assert.Equal(t, http.StatusUnauthorized, post("beta", `{"event_type":"notification","summary":"Notification: Claude needs your permission to use Bash","session_id":"`+sidB+`"}`, nil))
	assert.False(t, needs(sidB), "a forged permission request raises nothing")
	assert.Equal(t, http.StatusUnauthorized, post("beta", strings.Replace(stop, "%s", sidB, 1), map[string]string{store.SessionTokenHeader: "not-a-token"}))

	// A's token can't speak for B, by session_id.
	assert.Equal(t, http.StatusForbidden, post("beta", strings.Replace(stop, "%s", sidB, 1), asA))

	// A's token with B's name in the path and no session_id: recorded as A's (the path
	// name comes from the hook's folder, which A and B share).
	require.Equal(t, http.StatusOK, post("beta", `{"event_type":"notification","summary":"Notification: Claude needs your permission to use Bash"}`, asA))
	assert.True(t, needs(sidA), "recorded for the token's own session")
	assert.False(t, needs(sidB), "never for the agent named in the path")
	evs, err := h.ts.ListAgentEvents(ctx, "alpha", 10, &sidA)
	require.NoError(t, err)
	require.NotEmpty(t, evs)
	assert.Equal(t, "alpha", evs[0].AgentName)

	// Only known event types, and a bounded detail.
	assert.Equal(t, http.StatusBadRequest, post("alpha", `{"event_type":"auto_accept","summary":"x"}`, asA), "Coral's own types aren't postable")
	assert.Equal(t, http.StatusBadRequest, post("alpha", `{"event_type":"made_up","summary":"x"}`, asA))
	big, _ := json.Marshal(map[string]any{"event_type": "tool_use", "summary": "Ran: ls", "detail_json": map[string]string{"x": strings.Repeat("a", maxEventDetailBytes)}})
	assert.Equal(t, http.StatusBadRequest, post("alpha", string(big), asA))
	assert.Equal(t, http.StatusOK, post("alpha", `{"event_type":"stop","summary":"`+strings.Repeat("s", 3*maxEventSummaryBytes)+`"}`, asA), "a long summary is cut, not refused")

	// The dashboard's goal edit is the one event accepted without the agent's token, and
	// only a goal: a local script without the dashboard's headers can't post one either.
	dash := map[string]string{"Origin": server.URL, "Sec-Fetch-Site": "same-origin"}
	assert.Equal(t, http.StatusOK, post("beta", `{"event_type":"goal","summary":"Operator's goal","session_id":"`+sidB+`"}`, dash))
	assert.Equal(t, http.StatusUnauthorized, post("beta", strings.Replace(stop, "%s", sidB, 1), dash), "the dashboard can't post state events")
	assert.Equal(t, http.StatusUnauthorized, post("beta", `{"event_type":"goal","summary":"x","session_id":"`+sidB+`"}`, nil))
}

// A refused sender is logged once per claimed session, and never more than max keys.
func TestRefusedEventSendersAreLoggedOnce(t *testing.T) {
	s := &onceSet{max: 2}
	assert.True(t, s.add("a"))
	assert.False(t, s.add("a"), "once per session")
	assert.True(t, s.add("b"))
	assert.False(t, s.add("c"), "full: quiet, not one line per event")
}

// #3481 part 1b (Backend's finding): the pending tool, which auto-accept and the gate
// read, is set only by the agent's own hook, by its session token.
func TestPendingToolIsBoundToTheAgentsToken(t *testing.T) {
	server, h, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	sidA, sidB := "aaaaaaaa-3481-1b00-0000-000000000001", "bbbbbbbb-3481-1b00-0000-000000000002"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sidA, AgentType: "claude", AgentName: "alpha", WorkingDir: "/tmp/s"}))
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sidB, AgentType: "claude", AgentName: "beta", WorkingDir: "/tmp/s"}))
	tokA := agentToken(t, h, sidA, "claude-"+sidA)
	post := func(body string, tok string) int {
		req, _ := http.NewRequest("POST", server.URL+"/api/sessions/live/beta/pending-tool", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if tok != "" {
			req.Header.Set(store.SessionTokenHeader, tok)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	now := func(sid string) (pendingTool, bool) { return h.pending.get(sid, time.Now()) }

	assert.Equal(t, http.StatusUnauthorized, post(`{"session_id":"`+sidB+`","tool_name":"Bash","tool_input":{"command":"ls"}}`, ""))
	assert.Equal(t, http.StatusForbidden, post(`{"session_id":"`+sidB+`","tool_name":"Bash"}`, tokA))
	_, ok := now(sidB)
	assert.False(t, ok, "nobody set B's pending tool")
	require.Equal(t, http.StatusOK, post(`{"tool_name":"`+strings.Repeat("T", 500)+`","tool_use_id":"t1"}`, tokA))
	p, ok := now(sidA)
	require.True(t, ok, "recorded for the token's own session")
	assert.LessOrEqual(t, len(p.ToolName), 200+len("…"), "capped")
}
