package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
)

// #6198: a conversation was listed in History only at the next index pass. A finished turn (the agent's own "stop" event) asks the indexer for a pass soon, and so does a History list that is empty.
func TestAFinishedTurnAsksTheIndexerForAPass(t *testing.T) {
	t.Setenv("CLAUDE_PROJECTS_DIR", t.TempDir())
	server, h, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	var kicks atomic.Int32
	h.SetIndexKick(func() { kicks.Add(1) })
	sid := "eeeeeeee-6198-4000-8000-000000000001"
	name := "codex-" + sid
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "codex", AgentName: "echo", WorkingDir: t.TempDir()}))
	tok := agentToken(t, h, sid, name)
	post := func(body string, token string) {
		req, _ := http.NewRequest("POST", server.URL+"/api/sessions/live/echo/events", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set(store.SessionTokenHeader, token)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
	}
	post(`{"event_type":"tool_use","summary":"ran a command"}`, tok)
	require.EqualValues(t, 0, kicks.Load(), "a tool call is not a finished turn")
	post(`{"event_type":"stop","summary":"done"}`, "")
	require.EqualValues(t, 0, kicks.Load(), "an event with no session token is refused and asks for nothing")
	post(`{"event_type":"stop","summary":"done"}`, tok)
	require.EqualValues(t, 1, kicks.Load(), "the agent's own stop asks the indexer for a pass")
}

func TestAnEmptyHistoryListAsksTheIndexerForAPass(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenWithKey(ctx, filepath.Join(t.TempDir(), "sessions.db"), "")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h := NewHistoryHandler(db, &config.Config{}, nil)
	var kicks atomic.Int32
	h.SetIndexKick(func() { kicks.Add(1) })
	r := chi.NewRouter()
	r.Get("/api/sessions/history", h.ListSessions)
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)
	get := func(q string) {
		resp, err := http.Get(server.URL + "/api/sessions/history" + q)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, 200, resp.StatusCode)
	}
	get("")
	require.EqualValues(t, 1, kicks.Load(), "an empty list asks for a pass")
	get("?q=nothing")
	require.EqualValues(t, 1, kicks.Load(), "a search that finds nothing is not an empty history")
}
