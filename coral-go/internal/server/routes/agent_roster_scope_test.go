package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// #5041: an agent's token sees who is running on other boards (the roster), not what they work on; its own row and its board mates' rows are whole.
func rosterRows() []map[string]any {
	row := func(sid, name, board string, extra map[string]any) map[string]any {
		r := map[string]any{"session_id": sid, "name": name, "agent_type": "claude", "board_project": board, "status": "Working", "working": true,
			"prompt": "PROMPT-" + name, "first_prompt": "FIRST-" + name, "summary": "SUMMARY-" + name, "waiting_summary": "WAITING-" + name,
			"branch": "BRANCH-" + name, "working_directory": "/home/u/" + name, "log_path": "/logs/" + name, "git_state": map[string]any{"branch": "BRANCH-" + name},
			"display_name": "Name " + name, "future_free_text_field": "FUTURE-" + name}
		for k, v := range extra {
			r[k] = v
		}
		return r
	}
	return []map[string]any{
		row("sid-me", "me", "team-x", nil),
		row("sid-mate", "mate", "team-x", nil),
		row("sid-other", "other", "team-y", nil),
		row("sid-none", "loner", "", nil),
		row("sid-remote", "remote", "team-x", map[string]any{"server": "box-2"}),
	}
}

func TestScopeRowsForAgentReducesOnlyOtherBoards(t *testing.T) {
	rows := rosterRows()
	got := scopeRowsForAgent(rows, "sid-me")
	require.Len(t, got, 5)
	for _, i := range []int{0, 1} { // itself and its board mate: whole
		assert.Equal(t, rows[i], got[i])
		assert.Equal(t, "PROMPT-"+rows[i]["name"].(string), got[i]["prompt"])
	}
	for _, i := range []int{2, 3, 4} { // another board, no board, another server: the roster only
		for _, k := range []string{"prompt", "first_prompt", "summary", "waiting_summary", "branch", "working_directory", "log_path", "git_state", "future_free_text_field"} {
			assert.NotContains(t, got[i], k, "%v: %s", rows[i]["name"], k)
		}
		assert.Equal(t, rows[i]["name"], got[i]["name"])
		assert.Equal(t, rows[i]["session_id"], got[i]["session_id"])
		assert.Equal(t, "Working", got[i]["status"])
		assert.Equal(t, "Name "+rows[i]["name"].(string), got[i]["display_name"])
	}
	assert.Contains(t, rows[2], "prompt", "the input rows are never changed")
}

func TestScopeRowsForAnAgentWithNoBoardSeesOnlyItself(t *testing.T) {
	got := scopeRowsForAgent(rosterRows(), "sid-none")
	assert.Contains(t, got[3], "prompt", "its own row")
	assert.NotContains(t, got[0], "prompt", "a board's row, though the agent is on none")
	assert.NotContains(t, got[1], "prompt")
}

func TestScopeTickForAgentLeavesTheSharedTickAloneAndDiffsConsistently(t *testing.T) {
	rows := rosterRows()
	tick := &wsTick{sessions: rows, current: map[string]string{}, byKey: map[string]map[string]any{}, hasRemotes: true, serversJSON: `[{"id":"box-2"}]`}
	for i, r := range rows {
		key := r["session_id"].(string)
		if i == 4 {
			key = remoteKey("box-2", key)
		}
		b, _ := json.Marshal(r)
		tick.current[key], tick.byKey[key] = string(b), r
	}
	scoped := scopeTickForAgent(tick, "sid-me")
	assert.Contains(t, tick.byKey["sid-other"], "prompt", "the shared tick is untouched")
	assert.Equal(t, 5, len(tick.sessions))
	require.Len(t, scoped.sessions, 4, "another server's rows are left out")
	assert.False(t, scoped.hasRemotes)
	assert.Empty(t, scoped.serversJSON)
	assert.Contains(t, scoped.byKey["sid-me"], "prompt")
	assert.Contains(t, scoped.byKey["sid-mate"], "prompt")
	assert.NotContains(t, scoped.byKey["sid-other"], "prompt")
	assert.NotContains(t, scoped.current["sid-other"], "PROMPT-other", "the diff form the socket sends carries no prompt either")
	assert.NotContains(t, scoped.current["sid-other"], "FUTURE-other")
	assert.Contains(t, scoped.current["sid-other"], `"name":"other"`)
	assert.Equal(t, tick.current["sid-me"], scoped.current["sid-me"], "kept rows keep their serialized form")
	_, hasRemote := scoped.byKey[remoteKey("box-2", "sid-remote")]
	assert.False(t, hasRemote)
	again := scopeTickForAgent(tick, "sid-me")
	assert.Equal(t, scoped.current, again.current, "the same tick scopes to the same serialized forms (no false diffs)")
}

// the real handlers: sleeping sessions are the rows the database holds (a prompt each), so no terminal is needed.
func registerRosterSessions(t *testing.T, ss *store.SessionStore) {
	t.Helper()
	reg := func(sid, name, board, prompt string) {
		ls := &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: name, WorkingDir: "/work/" + name, IsSleeping: 1, Prompt: &prompt}
		ls.BoardName = &board
		require.NoError(t, ss.RegisterLiveSession(context.Background(), ls))
	}
	reg("aaaaaaaa-5041-4000-8000-00000000000a", "me", "team-x", "PROMPT-me")
	reg("bbbbbbbb-5041-4000-8000-00000000000b", "mate", "team-x", "PROMPT-mate")
	reg("cccccccc-5041-4000-8000-00000000000c", "other", "team-y", "PROMPT-other")
}

func TestLiveListForAnAgentTokenReducesOtherBoards(t *testing.T) {
	_, h, _, ss := setupSessionsTestServer(t)
	registerRosterSessions(t, ss)
	get := func(agent string) map[string]map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/live", nil)
		if agent != "" {
			req = req.WithContext(context.WithValue(req.Context(), verifiedSessionKey{}, &store.SessionTokenOwner{SessionID: agent, SessionName: "claude-" + agent}))
		}
		rec := httptest.NewRecorder()
		h.List(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var rows []map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
		out := map[string]map[string]any{}
		for _, r := range rows {
			out[r["name"].(string)] = r
		}
		return out
	}
	op := get("")
	require.Len(t, op, 3)
	assert.Equal(t, "PROMPT-other", op["other"]["prompt"], "the Operator sees every row whole")

	agent := get("aaaaaaaa-5041-4000-8000-00000000000a")
	require.Len(t, agent, 3, "the roster is still there")
	assert.Equal(t, "PROMPT-me", agent["me"]["prompt"])
	assert.Equal(t, "PROMPT-mate", agent["mate"]["prompt"], "a board mate's row is whole")
	assert.NotContains(t, agent["other"], "prompt", "another board's prompt")
	assert.NotContains(t, agent["other"], "working_directory")
	assert.NotContains(t, agent["other"], "first_prompt")
	assert.Equal(t, "team-y", agent["other"]["board_project"], "its board and state stay")
	assert.Equal(t, "Sleeping", agent["other"]["status"])
}

func TestWSCoralForAnAgentTokenReducesOtherBoards(t *testing.T) {
	_, handler := setupTestServer(t)
	registerRosterSessions(t, handler.ss)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if sid := req.Header.Get("X-Test-Agent"); sid != "" {
				req = req.WithContext(context.WithValue(req.Context(), verifiedSessionKey{}, &store.SessionTokenOwner{SessionID: sid, SessionName: "claude-" + sid}))
			}
			next.ServeHTTP(w, req)
		})
	})
	r.Get("/ws/coral", handler.WSCoral)
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	first := func(agent string) map[string]map[string]any {
		var opts *websocket.DialOptions
		if agent != "" {
			opts = &websocket.DialOptions{HTTPHeader: http.Header{"X-Test-Agent": {agent}}}
		}
		conn, _, err := websocket.Dial(ctx, "ws"+server.URL[4:]+"/ws/coral", opts)
		require.NoError(t, err)
		defer conn.CloseNow()
		var msg struct {
			Type     string           `json:"type"`
			Sessions []map[string]any `json:"sessions"`
		}
		require.NoError(t, wsjson.Read(ctx, conn, &msg))
		require.Equal(t, "coral_update", msg.Type)
		out := map[string]map[string]any{}
		for _, s := range msg.Sessions {
			out[s["name"].(string)] = s
		}
		return out
	}
	op := first("")
	require.Len(t, op, 3)
	assert.Equal(t, "PROMPT-other", op["other"]["prompt"], "the dashboard sees every row whole")
	agent := first("aaaaaaaa-5041-4000-8000-00000000000a")
	require.Len(t, agent, 3)
	assert.Equal(t, "PROMPT-me", agent["me"]["prompt"])
	assert.Equal(t, "PROMPT-mate", agent["mate"]["prompt"])
	assert.NotContains(t, agent["other"], "prompt", "another board's prompt is not on an agent's socket")
	assert.NotContains(t, agent["other"], "working_directory")
	assert.Equal(t, "team-y", agent["other"]["board_project"])
}

// The usage summary's by_agent is one row per session of every board (id, name, board, working directory): an agent's token gets the totals and the
// by-type, by-model and by-thinking tables, not that list; the Operator's reply is whole (#5041, QA's sweep).
func TestUsageSummaryForAnAgentTokenHasNoPerSessionRows(t *testing.T) {
	ts, _, router := setupTokenUsageTest(t)
	board := "team-b"
	require.NoError(t, ts.RecordUsage(t.Context(), &store.TokenUsage{SessionID: "other-session", AgentName: "other-agent", AgentType: "claude", BoardName: &board,
		InputTokens: 10, OutputTokens: 5, TotalTokens: 15, CostUSD: 0.01}))

	get := func(agent bool) (map[string]any, string) {
		req := httptest.NewRequest(http.MethodGet, "/api/token-usage/summary", nil)
		if agent {
			req = req.WithContext(context.WithValue(req.Context(), verifiedSessionKey{}, &store.SessionTokenOwner{SessionID: "me", SessionName: "claude-me"}))
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		return body, w.Body.String()
	}

	op, opRaw := get(false)
	assert.Contains(t, op, "by_agent")
	assert.Contains(t, opRaw, "other-session", "the Operator's reply is whole")

	ag, agRaw := get(true)
	assert.NotContains(t, ag, "by_agent")
	for _, secret := range []string{"other-session", "other-agent", "team-b"} {
		assert.NotContains(t, agRaw, secret)
	}
	assert.Contains(t, ag, "by_agent_type")
	assert.Contains(t, ag, "by_model")
	assert.Equal(t, 15.0, ag["totals"].(map[string]any)["total_tokens"], "the totals stay")
}

// GET /api/sessions/{id}/resolve (an agent tool calls it) answers a session of another board with the roster fields only: no working directory, no prompt (#5041, QA's sweep).
func TestResolveForAnAgentTokenReducesOtherBoards(t *testing.T) {
	_, h, _, ss := setupSessionsTestServer(t)
	registerRosterSessions(t, ss)
	resolve := func(agent, sid string) map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sid+"/resolve", nil)
		rc := chi.NewRouteContext()
		rc.URLParams.Add("sessionID", sid)
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
		if agent != "" {
			ctx = context.WithValue(ctx, verifiedSessionKey{}, &store.SessionTokenOwner{SessionID: agent, SessionName: "claude-" + agent})
		}
		rec := httptest.NewRecorder()
		h.ResolveSession(rec, req.WithContext(ctx))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}
	const me, mate, other = "aaaaaaaa-5041-4000-8000-00000000000a", "bbbbbbbb-5041-4000-8000-00000000000b", "cccccccc-5041-4000-8000-00000000000c"

	assert.Equal(t, "/work/other", resolve("", other)["working_directory"], "the Operator's answer is whole")
	assert.Equal(t, "/work/me", resolve(me, me)["working_directory"])
	assert.Equal(t, "/work/mate", resolve(me, mate)["working_directory"], "a board mate's answer is whole")

	got := resolve(me, other)
	assert.NotContains(t, got, "working_directory")
	assert.NotContains(t, got, "first_prompt")
	assert.Equal(t, other, got["session_id"], "the roster fields stay")
	assert.Equal(t, "team-y", got["board_project"])
	assert.Equal(t, "other", got["name"])
}
