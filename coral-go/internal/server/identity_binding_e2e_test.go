package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// #3495 end to end, the real router in DEFAULT mode (no strict local auth): on the routes
// an agent's tools call, a presented session token decides the session (another
// session_id is 403), routes only an agent calls need the token (401 without), and the
// routes the dashboard also uses still accept the Operator without one. On a board, an
// agent token can't post as another subscriber or as the Operator; a token-less script
// still can in default mode (the operator's decision; strict mode requires a credential).
func TestIdentityBindingOnAgentRoutes(t *testing.T) {
	srv, ts, _ := strictServer(t, false)
	ctx := context.Background()
	ss := store.NewSessionStore(srv.db)
	sidA, sidB := "aaaaaaaa-3495-0000-0000-00000000000a", "bbbbbbbb-3495-0000-0000-00000000000b"
	for _, sid := range []string{sidA, sidB} {
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "agent-" + sid[:4], WorkingDir: "/tmp/" + sid[:4]}))
	}
	tokA, err := srv.db.MintSessionToken(ctx, sidA, "claude-"+sidA)
	require.NoError(t, err)

	asA := http.Header{store.SessionTokenHeader: {tokA}}
	dashboard := http.Header{"Origin": {ts.URL}, "Sec-Fetch-Site": {"same-origin"}}
	do := func(method, path, body string, hdr http.Header) (int, map[string]any) {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, ts.URL+path, rd)
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header[k] = v
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	expect := func(want int, method, path, body string, hdr http.Header, msg string) map[string]any {
		t.Helper()
		code, out := do(method, path, body, hdr)
		assert.Equal(t, want, code, "%s: %s %s %v", msg, method, path, out)
		return out
	}

	// Personal tasks (coral-agent task): agent only, except finishing one, which the dashboard does too.
	expect(401, "POST", "/api/agent/tasks", `{"session_id":"`+sidA+`","title":"t"}`, nil, "no token")
	expect(403, "POST", "/api/agent/tasks", `{"session_id":"`+sidB+`","title":"t"}`, asA, "another session")
	task := expect(201, "POST", "/api/agent/tasks", `{"title":"mine"}`, asA, "own session, filled from the token")
	id := int64(task["id"].(float64))
	expect(401, "GET", "/api/agent/tasks?session_id="+sidA, "", dashboard, "listing is the agent's own CLI only")
	expect(403, "POST", "/api/agent/tasks/claim", `{"session_id":"`+sidB+`"}`, asA, "claim as another")
	expect(200, "POST", "/api/agent/tasks/"+itoa(id)+"/complete", `{"session_id":"`+sidA+`","message":"done"}`, dashboard, "the Operator finishes it")

	// Artifacts (coral-agent artifact upload): agent only.
	expect(401, "POST", "/api/agent/artifacts?session_id="+sidA, "x", http.Header{"X-Artifact-Name": {"a.txt"}}, "no token")
	expect(403, "POST", "/api/agent/artifacts?session_id="+sidB, "x", http.Header{store.SessionTokenHeader: {tokA}, "X-Artifact-Name": {"a.txt"}}, "another session")

	// Agent UI panels: only the agent writes one; the dashboard reads and removes them.
	expect(401, "PUT", "/api/agent/ui/p1?session_id="+sidA, `{"title":"p","html":"<p>x</p>"}`, dashboard, "the Operator doesn't write panels")
	expect(403, "PUT", "/api/agent/ui/p1?session_id="+sidB, `{"title":"p","html":"<p>x</p>"}`, asA, "another session")
	code, _ := do("DELETE", "/api/agent/ui/p1?session_id="+sidA, "", dashboard)
	assert.NotEqual(t, 401, code, "the dashboard removes panels")
	expect(200, "GET", "/api/agent/ui?session_id="+sidA, "", dashboard, "the dashboard reads panels")

	// Clearing an agent's events: its own token, or the Operator.
	expect(401, "DELETE", "/api/sessions/live/x/events?session_id="+sidB, "", nil, "anonymous")
	expect(403, "DELETE", "/api/sessions/live/x/events?session_id="+sidB, "", asA, "another session")
	expect(200, "DELETE", "/api/sessions/live/x/events?session_id="+sidA, "", asA, "own session")
	expect(200, "DELETE", "/api/sessions/live/x/events?session_id="+sidB, "", dashboard, "the Operator")

	// The hooks' token usage and model reports: agent only.
	expect(401, "POST", "/api/sessions/live/x/token-usage", `{"session_id":"`+sidA+`","input_tokens":5}`, dashboard, "not even the dashboard")
	expect(403, "POST", "/api/sessions/live/x/token-usage", `{"session_id":"`+sidB+`","input_tokens":5}`, asA, "another session")
	expect(200, "POST", "/api/sessions/live/x/token-usage", `{"input_tokens":5}`, asA, "own session")
	expect(401, "POST", "/api/sessions/live/x/context-window", `{"session_id":"`+sidA+`","model":"claude-sonnet-4"}`, nil, "no token")
	expect(403, "POST", "/api/sessions/live/x/context-window", `{"session_id":"`+sidB+`","model":"claude-sonnet-4"}`, asA, "another session")

	// Notifications (coral-agent notify): an agent or the Operator, never an anonymous process.
	expect(401, "POST", "/api/notifications", `{"title":"hi"}`, nil, "anonymous")
	expect(200, "POST", "/api/notifications", `{"title":"hi"}`, asA, "an agent")
	expect(200, "POST", "/api/notifications", `{"title":"hi"}`, dashboard, "the Operator")
	// #3622 F2: a link outside docs://, http(s) and dashboard paths is refused, for anyone.
	expect(400, "POST", "/api/notifications", `{"title":"hi","link":{"label":"Open","url":"javascript:alert(1)"}}`, asA, "a javascript: link")
	expect(400, "POST", "/api/notifications", `{"title":"hi","link":{"label":"Open","url":"data:text/html,x"}}`, dashboard, "a data: link, even from the Operator")
	expect(200, "POST", "/api/notifications", `{"title":"hi","link":{"label":"Docs","url":"docs://workflow-quickstart"}}`, asA, "a docs:// link")

	// A terminal session is a plain shell, never given a token: coral-agent run in it
	// works without one in default mode, for that session only. An agent session
	// can't be reached that way (sidA, sidB are claude sessions: 401 above).
	sidT := "tttttttt-3495-0000-0000-00000000000t"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sidT, AgentType: "terminal", AgentName: "term-3495", WorkingDir: "/tmp/term"}))
	tt := expect(201, "POST", "/api/agent/tasks", `{"session_id":"`+sidT+`","title":"terminal task"}`, nil, "a terminal session, no token")
	expect(200, "GET", "/api/agent/tasks?session_id="+sidT, "", nil, "a terminal session lists its tasks")
	expect(200, "POST", "/api/agent/tasks/"+itoa(int64(tt["id"].(float64)))+"/complete", `{"session_id":"`+sidT+`","message":"done"}`, nil, "and finishes one")
	expect(403, "POST", "/api/agent/tasks", `{"session_id":"`+sidT+`","title":"t"}`, asA, "an agent token naming the terminal session")
	expect(401, "POST", "/api/sessions/live/x/token-usage", `{"session_id":"`+sidT+`","input_tokens":5}`, nil, "hook-only routes still need a token")

	// Boards: a presented token decides who the caller is.
	_, err = srv.boardStore.Subscribe(ctx, "b3495", "Alpha", "Alpha", "claude-"+sidA, nil, nil, "all")
	require.NoError(t, err)
	_, err = srv.boardStore.Subscribe(ctx, "b3495", "Beta", "Beta", "claude-"+sidB, nil, nil, "all")
	require.NoError(t, err)
	out := expect(403, "POST", "/api/board/b3495/messages", `{"subscriber_id":"Beta","content":"as Beta"}`, asA, "impersonating another subscriber")
	assert.Equal(t, "identity_mismatch", out["code"])
	expect(403, "POST", "/api/board/b3495/messages", `{"subscriber_id":"Operator","content":"as the Operator"}`, asA, "claiming the Operator")
	expect(200, "POST", "/api/board/b3495/messages", `{"subscriber_id":"Alpha","content":"as itself"}`, asA, "its own subscriber")
	expect(200, "POST", "/api/board/b3495/messages", `{"subscriber_id":"Beta","content":"a bare script"}`, nil, "a token-less script, default mode (unchanged)")
	expect(403, "POST", "/api/board/b3495/subscribe", `{"subscriber_id":"Alpha","session_name":"claude-`+sidB+`"}`, asA, "subscribing under another session's name")

	// #3512: an agent with a token but no subscription yet is checked too. It can join
	// under a new name, but never act as the Operator or as a subscriber another session
	// holds (before, it skipped every board check).
	sidC := "cccccccc-3512-0000-0000-00000000000c"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sidC, AgentType: "claude", AgentName: "agent-cccc", WorkingDir: "/tmp/cccc"}))
	tokC, err := srv.db.MintSessionToken(ctx, sidC, "claude-"+sidC)
	require.NoError(t, err)
	asC := http.Header{store.SessionTokenHeader: {tokC}}
	out = expect(403, "POST", "/api/board/b3495/messages", `{"subscriber_id":"Operator","content":"as the Operator"}`, asC, "unsubscribed, claiming the Operator")
	assert.Equal(t, "identity_mismatch", out["code"])
	expect(403, "PUT", "/api/board/b3495/settings", `{"subscriber_id":"Operator","cross_team_messaging":true}`, asC, "unsubscribed, changing settings as the Operator")
	expect(403, "POST", "/api/board/b3512new/messages", `{"subscriber_id":"operator","content":"x"}`, asC, "unsubscribed, the Operator in any case, on a new board")
	expect(403, "POST", "/api/board/b3495/messages", `{"subscriber_id":"alpha","content":"as Alpha"}`, asC, "unsubscribed, as a subscriber another session holds")
	expect(403, "POST", "/api/board/b3495/subscribe", `{"subscriber_id":"Beta","session_name":"claude-`+sidC+`"}`, asC, "unsubscribed, taking over another session's subscriber")
	expect(403, "POST", "/api/board/b3495/subscribe", `{"subscriber_id":"Gamma","session_name":"claude-`+sidA+`"}`, asC, "unsubscribed, joining under another session's name")
	expect(200, "POST", "/api/board/b3495/subscribe", `{"subscriber_id":"Gamma","session_name":"claude-`+sidC+`","job_title":"Gamma"}`, asC, "joining under a new name")
	expect(200, "POST", "/api/board/b3495/messages", `{"subscriber_id":"Gamma","content":"as itself"}`, asC, "then posting as itself")
	expect(403, "POST", "/api/board/b3495/messages", `{"subscriber_id":"Operator","content":"still not"}`, asC, "and still never the Operator")
}

// With strict local auth, a terminal session's token-less coral-agent request is
// refused like any other (#3495 keeps the terminal exception to default mode).
func TestIdentityBindingTerminalSessionStrict(t *testing.T) {
	srv, ts, _ := strictServer(t, true)
	sidT := "tttttttt-3495-0000-0000-00000000000t"
	require.NoError(t, store.NewSessionStore(srv.db).RegisterLiveSession(context.Background(),
		&store.LiveSession{SessionID: sidT, AgentType: "terminal", AgentName: "term-3495", WorkingDir: "/tmp/term"}))
	for _, ua := range []string{"coral-agent/1.4", ""} {
		req, _ := http.NewRequest("POST", ts.URL+"/api/agent/tasks", strings.NewReader(`{"session_id":"`+sidT+`","title":"t"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", ua)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "user agent %q: %s", ua, b)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// #3512: a board store that can't be read is never a bypass. The subscription lookup
// failing checks the agent as unsubscribed (the Operator is still refused), and a name
// that can't be checked against the board's subscribers is refused with 503.
func TestIdentityBindingBoardStoreErrorFailsClosed(t *testing.T) {
	srv, ts, _ := strictServer(t, false)
	ctx := context.Background()
	sid := "dddddddd-3512-0000-0000-00000000000d"
	require.NoError(t, store.NewSessionStore(srv.db).RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "agent-dddd", WorkingDir: "/tmp/dddd"}))
	tok, err := srv.db.MintSessionToken(ctx, sid, "claude-"+sid)
	require.NoError(t, err)
	_, err = srv.boardStore.Subscribe(ctx, "b3512", "Delta", "Delta", "claude-"+sid, nil, nil, "all")
	require.NoError(t, err)
	require.NoError(t, srv.boardStore.Close()) // every read fails from here on
	post := func(body string) (int, map[string]any) {
		req, _ := http.NewRequest("POST", ts.URL+"/api/board/b3512/messages", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(store.SessionTokenHeader, tok)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	code, out := post(`{"subscriber_id":"Operator","content":"x"}`)
	assert.Equal(t, http.StatusForbidden, code, out)
	assert.Equal(t, "identity_mismatch", out["code"])
	code, out = post(`{"subscriber_id":"Delta","content":"x"}`)
	assert.Equal(t, http.StatusServiceUnavailable, code, out)
	assert.Equal(t, "identity_unavailable", out["code"])
}
