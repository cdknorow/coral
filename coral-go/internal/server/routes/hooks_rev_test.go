package routes

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/store"
)

// #3655 F3: the gate trusts a Claude agent's signals only when its hooks are the set with the
// request and turn signals, from the revision its own events carry. An older set (events with no
// revision) and an unknown one (nothing heard since this server started) leave the gate on its
// screen rule: never a signal "idle" (QA: fail-safe).
func TestGateTrustsOnlyCurrentHookSets(t *testing.T) {
	t.Setenv("CLAUDE_PROJECTS_DIR", t.TempDir())
	server, h, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	sid := "eeeeeeee-3655-4000-8000-000000000003"
	name := "claude-" + sid
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "echo", WorkingDir: t.TempDir()}))
	tok := agentToken(t, h, sid, name)
	post := func(body string) {
		req, _ := http.NewRequest("POST", server.URL+"/api/sessions/live/echo/events", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(store.SessionTokenHeader, tok)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
	}
	state := func() (ptymanager.AgentState, bool) { return h.gateState(ctx, name, "claude", sid) }

	// Unknown: the session has events, but none since this server started.
	id := sid
	_, err := h.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "echo", SessionID: &id, EventType: "stop", Summary: "done"})
	require.NoError(t, err)
	_, ok := state()
	assert.False(t, ok, "unknown hook set: the screen rule decides")

	// An older set: its events carry no revision.
	post(`{"event_type":"stop","summary":"done"}`)
	_, ok = state()
	assert.False(t, ok, "an older hook set: never a signal idle")

	// The agent is relaunched with the current set.
	post(`{"event_type":"stop","summary":"done","hooks_rev":2}`)
	st, ok := state()
	require.True(t, ok, "the current set: its signals decide")
	assert.Equal(t, ptymanager.StateIdle, st.State)

	// A revision before the request signals is an older set too.
	post(`{"event_type":"stop","summary":"done","hooks_rev":1}`)
	_, ok = state()
	assert.False(t, ok)

	// Other CLIs are not judged by Claude's hook revision.
	post(`{"event_type":"stop","summary":"done"}`)
	_, ok = h.gateState(ctx, "codex-"+sid, "codex", sid)
	assert.True(t, ok, "Codex keeps its signals")
	assert.Equal(t, 2, agent.ClaudeSignalsHooksRev)
}
