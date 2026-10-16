package routes

import (
	"context"
	"net/http"
	"strconv"
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

	// Security's SHOULD: an event without a revision (an old Coral hook left in the user's
	// global Claude settings fires next to the current one) never turns the current set back.
	post(`{"event_type":"stop","summary":"done"}`)
	_, ok = state()
	assert.True(t, ok, "no flapping: the current set stays")

	// A revision before the request signals is an older set too.
	post(`{"event_type":"stop","summary":"done","hooks_rev":1}`)
	_, ok = state()
	assert.False(t, ok)

	// The session ends: its signals still say so (Gone holds every send), the revision is kept.
	post(`{"event_type":"stop","summary":"done","hooks_rev":2}`)
	post(`{"event_type":"session_end","summary":"Session ended","hooks_rev":2}`)
	st, ok = state()
	require.True(t, ok)
	assert.Equal(t, ptymanager.StateGone, st.State)

	// Other CLIs are not judged by Claude's hook revision.
	post(`{"event_type":"stop","summary":"done"}`)
	_, ok = h.gateState(ctx, "codex-"+sid, "codex", sid)
	assert.True(t, ok, "Codex keeps its signals")
	assert.Equal(t, 2, agent.ClaudeSignalsHooksRev)
}

// #3665: the hook set is revision 3 (absolute hook paths, #3660). The gate still trusts the
// signals of revision 2 and later, so an agent launched on v1.4.53 keeps them until it is
// moved onto revision 3.
func TestGateTrustsRevisionTwoAndThree(t *testing.T) {
	t.Setenv("CLAUDE_PROJECTS_DIR", t.TempDir())
	server, h, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	require.Equal(t, 3, agent.ClaudeHooksRev)
	require.Equal(t, 2, agent.ClaudeSignalsHooksRev)
	for i, rev := range []int{2, 3} {
		sid := "eeeeeeee-3665-4000-8000-00000000000" + strconv.Itoa(i)
		name := "claude-" + sid
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "rev" + strconv.Itoa(rev), WorkingDir: t.TempDir()}))
		tok := agentToken(t, h, sid, name)
		req, _ := http.NewRequest("POST", server.URL+"/api/sessions/live/rev"+strconv.Itoa(rev)+"/events",
			strings.NewReader(`{"event_type":"stop","summary":"done","hooks_rev":`+strconv.Itoa(rev)+`}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(store.SessionTokenHeader, tok)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		_, ok := h.gateState(ctx, name, "claude", sid)
		assert.True(t, ok, "revision %d: its signals decide", rev)
	}
}
