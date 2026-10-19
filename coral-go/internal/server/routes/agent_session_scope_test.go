package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// #5013: an agent's token reads its own session and the sessions on its own board, not any other's, on every route that is about one session;
// the Operator (no token) is not affected; and the reads across every session, board and team are the Operator's.
func TestAgentTokenReadsOnlyItsOwnSessionAndBoard(t *testing.T) {
	ctx := context.Background()
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ss := store.NewSessionStore(db)
	reg := func(sid, name, board string) {
		ls := &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: name, WorkingDir: t.TempDir()}
		if board != "" {
			ls.BoardName = &board
		}
		require.NoError(t, ss.RegisterLiveSession(ctx, ls))
	}
	const a, b, c, d = "aaaaaaaa-5013-4000-8000-00000000000a", "bbbbbbbb-5013-4000-8000-00000000000b", "cccccccc-5013-4000-8000-00000000000c", "dddddddd-5013-4000-8000-00000000000d"
	reg(a, "alpha", "team-x") // the caller
	reg(b, "beta", "team-x")  // same board
	reg(c, "gamma", "team-y") // another board
	reg(d, "delta", "")       // on no board
	token, err := db.MintSessionToken(ctx, a, "claude-alpha")
	require.NoError(t, err)

	o := NewIdentityObserver(db, nil, func(*http.Request) bool { return false })
	r := chi.NewRouter()
	r.Use(o.Middleware)
	reached := 0
	keys := make([]string, 0, len(agentSessionRoutes))
	for k := range agentSessionRoutes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		method, pattern, _ := strings.Cut(k, " ")
		r.MethodFunc(method, pattern, func(w http.ResponseWriter, _ *http.Request) { reached++; w.WriteHeader(http.StatusOK) })
	}
	require.NoError(t, ValidateAgentSessionRoutes(r), "every entry of agentSessionRoutes is a route the router serves")

	call := func(withToken bool, method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		if withToken {
			req.Header.Set(store.SessionTokenHeader, token)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	// every scoped route, once per kind of target
	for _, k := range keys {
		method, pattern, _ := strings.Cut(k, " ")
		with := func(target string) string {
			p := pattern
			for _, param := range []string{"{name}", "{sessionID}", "{subagentID}", "{ref}"} {
				if param == "{subagentID}" {
					p = strings.ReplaceAll(p, param, "sub-1")
				} else if param == "{ref}" {
					p = strings.ReplaceAll(p, param, "0123456789abcdef0123456789abcdef")
				} else {
					p = strings.ReplaceAll(p, param, target)
				}
			}
			return p
		}
		for _, own := range []string{a, "claude-alpha", "alpha"} {
			assert.Equal(t, http.StatusOK, call(true, method, with(own)).Code, "%s: itself (%s)", k, own)
		}
		assert.Equal(t, http.StatusOK, call(true, method, with(b)).Code, "%s: a session on its own board", k)
		assert.Equal(t, http.StatusOK, call(true, method, with("beta")).Code, "%s: a session on its own board, by name", k)
		for _, other := range []string{c, "gamma", d, "nobody-5013"} {
			rec := call(true, method, with(other))
			assert.Equal(t, http.StatusForbidden, rec.Code, "%s: another board's or an unknown session (%s)", k, other)
			assert.Contains(t, rec.Body.String(), "forbidden_other_session", "%s (%s)", k, other)
		}
		// a name beside the session_id: both must be the agent's to read
		assert.Equal(t, http.StatusForbidden, call(true, method, with(c)+"?session_id="+a).Code, "%s: another board's name with the agent's own session_id", k)
		assert.Equal(t, http.StatusForbidden, call(true, method, with(a)+"?session_id="+c).Code, "%s: the agent's own name with another board's session_id", k)
		assert.Equal(t, http.StatusOK, call(true, method, with("claude-alpha-tmux")+"?session_id="+a).Code, "%s: a name that names nothing beside the agent's own session_id", k)
		assert.Equal(t, http.StatusOK, call(false, method, with(c)).Code, "%s: the Operator (no token) reads any session", k)
	}
	assert.Greater(t, reached, len(keys), "the allowed calls reached their handlers")
	assert.Contains(t, agentSessionRoutes, "GET /api/sessions/live/{name}/attachment/{ref}", "a chat's pasted image is read under the session's scope: an agent of another board gets forbidden_other_session (#5032)")

	// the reads across every session, board and team are the Operator's
	for _, k := range []string{"GET /api/sessions/history", "GET /api/sessions/history/search", "GET /api/board/tasks",
		"GET /api/teams/detail/{name}/knowledge", "GET /api/teams/detail/{name}/knowledge/{agentName}", "GET /api/system/identity-report", "GET /api/token-usage", "GET /api/token-usage/by-branch", "GET /api/goals/metrics"} {
		assert.NotEmpty(t, operatorOnlyRoutes[k], "%s is in operatorOnlyRoutes", k)
	}
}

// #5019 (QA's #5017 review): a folder name shared by two boards, reading oneself by display name, and a tmux-style name (claude-<uuid>).
func TestAgentSessionScopeSharedNamesDisplayNamesAndTmuxNames(t *testing.T) {
	ctx := context.Background()
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ss := store.NewSessionStore(db)
	reg := func(sid, name, board, display string) {
		ls := &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: name, WorkingDir: t.TempDir()}
		ls.BoardName = &board
		if display != "" {
			ls.DisplayName = &display
		}
		require.NoError(t, ss.RegisterLiveSession(ctx, ls))
	}
	const a, b, c, a2, c2 = "aaaaaaaa-5019-4000-8000-00000000000a", "bbbbbbbb-5019-4000-8000-00000000000b", "cccccccc-5019-4000-8000-00000000000c",
		"a2a2a2a2-5019-4000-8000-0000000000a2", "c2c2c2c2-5019-4000-8000-0000000000c2"
	reg(c, "shared", "team-y", "")     // registered first: the first live session named "shared"
	reg(a, "shared", "team-x", "Lead") // the caller: the same folder name on another board
	reg(b, "mate", "team-x", "")       // a board mate
	reg(a2, "twin", "team-x", "")      // here the caller's own session is the FIRST of its name
	reg(c2, "twin", "team-y", "")      // and the other board's the second
	token, err := db.MintSessionToken(ctx, a, "claude-shared-a")
	require.NoError(t, err)

	o := NewIdentityObserver(db, nil, func(*http.Request) bool { return false })
	r := chi.NewRouter()
	r.Use(o.Middleware)
	r.Get("/api/sessions/live/{name}/info", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	get := func(path string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set(store.SessionTokenHeader, token)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}
	live := func(name string, query string) string { return "/api/sessions/live/" + name + "/info" + query }

	// 1. a folder name shared by two boards: the exact session decides, the name is only its own name
	assert.Equal(t, http.StatusOK, get(live("shared", "?session_id="+a)), "the shared name with the agent's own session_id")
	assert.Equal(t, http.StatusOK, get(live("claude-"+a, "?session_id="+a)), "its tmux name with its own session_id")
	assert.Equal(t, http.StatusForbidden, get(live("shared", "?session_id="+c)), "the shared name with the OTHER board's session_id")
	assert.Equal(t, http.StatusForbidden, get(live("shared", "")), "the shared name alone is the first live session of that name, which is another board's")
	assert.Equal(t, http.StatusOK, get(live("twin", "")), "the shared name alone, when the first live session of it is the agent's board's")
	assert.Equal(t, http.StatusForbidden, get(live("twin", "?session_id="+c2)), "...but another board's session_id is still refused")
	assert.Equal(t, http.StatusOK, get(live("twin", "?session_id="+a2)), "the board's own twin by id")

	// 2. a display name is not a name a handler resolves, so it is nobody's here (the tools use the tmux name or the id)
	assert.Equal(t, http.StatusForbidden, get(live("Lead", "")), "the agent's own display name is not resolved")
	assert.Equal(t, http.StatusOK, get(live("Lead", "?session_id="+a)), "...but beside its own session_id it reads nothing and passes")
	assert.Equal(t, http.StatusForbidden, get(live("Nobody-5019", "")), "a name that is nobody's")

	// 3. a tmux-style name of a known session is judged as that session, beside the agent's own session_id too
	assert.Equal(t, http.StatusForbidden, get(live("claude-"+c, "?session_id="+a)), "another board's tmux name beside the agent's own session_id")
	assert.Equal(t, http.StatusForbidden, get(live("claude-"+c, "")), "another board's tmux name")
	assert.Equal(t, http.StatusOK, get(live("claude-"+b, "")), "a board mate's tmux name")
	assert.Equal(t, http.StatusOK, get(live("claude-"+b, "?session_id="+a)), "a board mate's tmux name beside the agent's own session_id")
	assert.Equal(t, http.StatusOK, get(live("claude-"+a, "")), "its own tmux name")
	assert.Equal(t, http.StatusOK, get(live("claude-00000000-0000-4000-8000-000000000000", "?session_id="+a)), "a tmux name of nobody beside its own session_id reads nothing")
}
