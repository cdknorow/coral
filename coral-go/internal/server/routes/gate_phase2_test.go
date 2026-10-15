package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/store"
)

// phase2InTurn turns gate phase 2 on and puts every session in a turn, for one test.
func phase2InTurn(t *testing.T) {
	t.Helper()
	t.Setenv("CORAL_GATE_MODE", "")
	ptymanager.SetSignalsSettingFunc(func() bool { return true })
	ptymanager.SetStateFunc(func(context.Context, string, string, string) (ptymanager.AgentState, bool) {
		return ptymanager.AgentState{State: ptymanager.StateInTurn, Since: time.Now(), LastEvent: time.Now()}, true
	})
	t.Cleanup(func() { ptymanager.SetSignalsSettingFunc(nil); ptymanager.SetStateFunc(nil) })
}

// Security's point on #3578: what a person types in the dashboard (or the signed command line)
// is never held, whatever the agent's state; the same send from a script or an agent is
// automation and waits for the turn, like a board nudge.
func TestPhase2NeverHoldsTheDashboardsInput(t *testing.T) {
	phase2InTurn(t)
	_, handler, mock, ss := setupSessionsTestServer(t)
	sid := "00000000-0000-4000-8000-000000003578"
	name := naming.SessionName("claude", sid)
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "repo", WorkingDir: "/tmp/test"}))
	mock.addSession(name, "/tmp/test")
	t.Cleanup(func() { ptymanager.DropHeld(name, "test done") })
	send := func(text string, human bool) int {
		body := bytes.NewBufferString(`{"command": "` + text + `", "agent_type": "claude", "session_id": "` + sid + `"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+name+"/send", body)
		req.Header.Set("Content-Type", "application/json")
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("name", name)
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		if human {
			ctx = context.WithValue(ctx, callerKindKey{}, callerDashCookie)
		}
		rec := httptest.NewRecorder()
		handler.Send(rec, req.WithContext(ctx))
		return rec.Code
	}
	sentTo := func() []string {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return append([]string(nil), mock.sent[name]...)
	}

	assert.Equal(t, http.StatusOK, send("please stop and read the board", true))
	assert.Contains(t, sentTo(), "please stop and read the board", "a person's input is typed at once, mid-turn")
	assert.Equal(t, 0, ptymanager.HeldCount(name))

	assert.Equal(t, http.StatusAccepted, send("a script's text", false))
	assert.NotContains(t, sentTo(), "a script's text", "a script's send is automation: it waits for the turn")
	assert.Equal(t, 1, ptymanager.HeldCount(name))
}

// The dashboard's waiting indicator reads held_inputs from the live list and the feed.
func TestHeldInputsInTheLiveList(t *testing.T) {
	phase2InTurn(t)
	server, handler, mock, ss := setupSessionsTestServer(t)
	sid := "00000000-0000-4000-8000-000000003579"
	name := naming.SessionName("claude", sid)
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "repo", WorkingDir: "/tmp/test"}))
	mock.addSession(name, "/tmp/test")
	t.Cleanup(func() { ptymanager.DropHeld(name, "test done") })

	d, err := ptymanager.DeliverAutomated(context.Background(), ptymanager.AutomatedInput{Term: mock, Name: name, AgentType: "claude", SessionID: sid,
		Key: "board-unread:team", Text: "You have 1 unread message", What: "board nudge",
		Send: func(context.Context) error { return nil }})
	require.NoError(t, err)
	require.Equal(t, ptymanager.Held, d)

	resp, err := http.Get(server.URL + "/api/sessions/live")
	require.NoError(t, err)
	var list []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&list))
	resp.Body.Close()
	held := -1.0
	for _, s := range list {
		if s["session_id"] == sid {
			held, _ = s["held_inputs"].(float64)
		}
	}
	assert.Equal(t, 1.0, held, "the HTTP list")

	wsList, err := handler.buildSessionListForWS(httptest.NewRequest(http.MethodGet, "/ws/coral", nil))
	require.NoError(t, err)
	wsHeld := -1
	for _, s := range wsList {
		if s["session_id"] == sid {
			wsHeld, _ = s["held_inputs"].(int)
		}
	}
	assert.Equal(t, 1, wsHeld, "the feed")
}
