package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board/boardtest"
	"github.com/subgentic/coral/pkg/tunnel"
)

type crossTeamEnv struct {
	t        *testing.T
	url      string
	h        *BoardHandler
	notified atomic.Int32
	now      time.Time
}

func newCrossTeamEnv(t *testing.T) *crossTeamEnv {
	t.Helper()
	bs, err := boardtest.Open(t.TempDir() + "/board.db")
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	e := &crossTeamEnv{t: t, h: NewBoardHandler(bs), now: time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)}
	e.h.SetNotifyFn(func(string) { e.notified.Add(1) })
	e.h.crossTeam.now = func() time.Time { return e.now }
	r := chi.NewRouter()
	r.Get("/api/board/{project}/settings", e.h.GetBoardSettings)
	r.Put("/api/board/{project}/settings", e.h.UpdateBoardSettings)
	r.Post("/api/board/{project}/external-messages", e.h.PostExternalMessage)
	r.Get("/api/board/{project}/messages/all", e.h.ListAllMessages)
	srv := httptest.NewServer(r)
	// The server stops first, then the work its requests started after answering
	// (webhooks, message waits) finishes, and only then the store closes.
	t.Cleanup(func() { srv.Close(); e.h.WaitBackground() })
	e.url = srv.URL
	ctx := context.Background()
	for _, s := range []struct{ board, id, role string }{
		{"alpha", "Orchestrator", "Orchestrator"}, {"alpha", "Dev", "Backend Dev"},
		{"beta", "Orchestrator", "Orchestrator"}, {"beta", "QA", "QA Engineer"},
	} {
		_, err := bs.Subscribe(ctx, s.board, s.id, s.role, "tmux-"+s.board+s.id, nil, "mentions")
		require.NoError(t, err)
	}
	return e
}

func (e *crossTeamEnv) do(method, path string, body any) (int, map[string]any) {
	e.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, e.url+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (e *crossTeamEnv) enable(boardName string, on bool) {
	e.t.Helper()
	code, out := e.do("PUT", "/api/board/"+boardName+"/settings", map[string]any{"subscriber_id": "Operator", "cross_team_messaging": on})
	require.Equal(e.t, http.StatusOK, code, out)
	require.Equal(e.t, on, out["cross_team_messaging"])
}

// send posts as sender from its own session (the tmux name the env gave it).
func (e *crossTeamEnv) send(from, sender, to, content string) (int, map[string]any) {
	return e.sendAs(from, sender, "tmux-"+from+sender, to, content)
}

func (e *crossTeamEnv) sendAs(from, sender, sessionName, to, content string) (int, map[string]any) {
	return e.do("POST", "/api/board/beta/external-messages", map[string]any{"from_board": from, "subscriber_id": sender, "session_name": sessionName, "content": content, "to": to})
}

func TestCrossTeamSettingsAreOperatorOnly(t *testing.T) {
	e := newCrossTeamEnv(t)
	code, out := e.do("GET", "/api/board/alpha/settings", nil)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, true, out["cross_team_messaging"], "on by default: a kill switch (#5916)")
	for _, who := range []string{"Orchestrator", "Dev", ""} {
		code, _ = e.do("PUT", "/api/board/alpha/settings", map[string]any{"subscriber_id": who, "cross_team_messaging": false})
		require.Equal(t, http.StatusForbidden, code, "%q cannot change its board's switch", who)
	}
	e.enable("alpha", true)
}

func TestCrossTeamMessageRules(t *testing.T) {
	e := newCrossTeamEnv(t)

	// Both boards disabled, then only one: the error names the disabled board(s).
	code, out := e.send("alpha", "Orchestrator", "", "hi")
	require.Equal(t, http.StatusForbidden, code)
	require.Contains(t, out["error"], `"alpha" and "beta"`)
	require.Contains(t, out["error"], "Turn it on in Team settings > Messaging.")
	e.enable("alpha", true)
	code, out = e.send("alpha", "Orchestrator", "", "hi")
	require.Equal(t, http.StatusForbidden, code)
	require.Contains(t, out["error"], `Cross-team messaging is off on "beta".`)
	require.NotContains(t, out["error"], `"alpha"`)
	e.enable("beta", true)

	// Workers, unknown and inactive senders are refused; the sender's role comes from its subscription.
	code, _ = e.send("alpha", "Dev", "", "hi")
	require.Equal(t, http.StatusForbidden, code, "a worker cannot send")
	code, _ = e.send("alpha", "Nobody", "", "hi")
	require.Equal(t, http.StatusForbidden, code)
	code, _ = e.send("alpha", "QA", "", "hi") // subscribed on beta, not alpha
	require.Equal(t, http.StatusForbidden, code)
	code, _ = e.send("beta", "Orchestrator", "", "hi")
	require.Equal(t, http.StatusBadRequest, code, "own board")
	code, _ = e.send("alpha", "Orchestrator", "QA", "hi")
	require.Equal(t, http.StatusNotFound, code, "--to must be an Orchestrator on the target board")

	// An Orchestrator can send: delivered with a mention, mirrored, envelope on both.
	before := e.notified.Load()
	code, out = e.send("alpha", "Orchestrator", "", "Please review #12")
	require.Equal(t, http.StatusOK, code, out)
	require.Greater(t, e.notified.Load(), before, "the notifier runs a pass")
	require.Equal(t, []any{"Orchestrator"}, out["delivered_to"])
	msg := out["message"].(map[string]any)
	require.Equal(t, "@Orchestrator Please review #12", msg["content"])
	require.Equal(t, "Orchestrator (alpha)", msg["subscriber_id"])
	ext := msg["external"].(map[string]any)
	assert.Regexp(t, `^x-[a-z2-7]{16}$`, ext["from_cross_team_id"], "the sender board's own id is in the envelope (#5508)")
	delete(ext, "from_cross_team_id")
	require.Equal(t, map[string]any{"direction": "inbound", "server_id": "local", "board": "alpha", "sender": "Orchestrator", "sender_role": "Orchestrator", "target_server_id": "local", "target_board": "beta"}, ext)
	mirror := out["mirror"].(map[string]any)
	require.Equal(t, "Orchestrator", mirror["subscriber_id"])
	require.Equal(t, msg["id"], mirror["external"].(map[string]any)["target_message_id"])
	n, err := e.h.bs.CheckUnread(context.Background(), "beta", "Orchestrator")
	require.NoError(t, err)
	require.Equal(t, 1, n, "the target Orchestrator has the mention to read")

	// The dashboard list shows the envelope.
	code, list := e.do("GET", "/api/board/beta/messages/all?format=dashboard", nil)
	require.Equal(t, http.StatusOK, code)
	listed := list["messages"].([]any)[0].(map[string]any)
	require.Equal(t, "inbound", listed["external"].(map[string]any)["direction"])

	// The Operator can send from a board it is not subscribed to.
	code, out = e.send("alpha", "Operator", "Orchestrator", "From the operator")
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, "Operator (alpha)", out["message"].(map[string]any)["subscriber_id"])
	code, _ = e.send("nosuchboard", "Operator", "", "x")
	require.Equal(t, http.StatusNotFound, code)
}

func TestCrossTeamSenderSessionMustMatch(t *testing.T) {
	e := newCrossTeamEnv(t)
	e.enable("alpha", true)
	e.enable("beta", true)
	// A worker naming its Orchestrator, from its own session, is refused.
	code, out := e.sendAs("alpha", "Orchestrator", "tmux-alphaDev", "", "spoofed")
	require.Equal(t, http.StatusForbidden, code)
	require.Contains(t, out["error"], "session_name does not match")
	code, _ = e.sendAs("alpha", "Orchestrator", "", "", "no session")
	require.Equal(t, http.StatusForbidden, code, "session_name is required")
	// The Orchestrator's own session is delivered.
	code, out = e.sendAs("alpha", "Orchestrator", "tmux-alphaOrchestrator", "", "real")
	require.Equal(t, http.StatusOK, code, out)
	// The Operator path does not take a session.
	code, out = e.sendAs("alpha", "Operator", "", "", "from the dashboard")
	require.Equal(t, http.StatusOK, code, out)
}

func TestCrossTeamRateLimit(t *testing.T) {
	e := newCrossTeamEnv(t)
	e.enable("alpha", true)
	e.enable("beta", true)
	// Burst of 3, then refused with Retry-After until a token refills (one per 6s).
	for i := 0; i < 3; i++ {
		code, out := e.send("alpha", "Orchestrator", "", "ping")
		require.Equal(t, http.StatusOK, code, out)
	}
	code, out := e.send("alpha", "Orchestrator", "", "ping")
	require.Equal(t, http.StatusTooManyRequests, code)
	require.Contains(t, out["error"], "retry in 6s")
	require.Equal(t, "rate_limited", out["code"])
	// A limit on this action, marked so the dashboard doesn't pause every poller (#3434).
	body, _ := json.Marshal(map[string]any{"from_board": "alpha", "subscriber_id": "Orchestrator", "session_name": "tmux-alphaOrchestrator", "content": "ping"})
	resp, err := http.Post(e.url+"/api/board/beta/external-messages", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Equal(t, "action", resp.Header.Get("X-Coral-Limit"))
	assert.NotEmpty(t, resp.Header.Get("Retry-After"))
	e.now = e.now.Add(5 * time.Second)
	code, _ = e.send("alpha", "Orchestrator", "", "ping")
	require.Equal(t, http.StatusTooManyRequests, code)
	e.now = e.now.Add(time.Second)
	code, _ = e.send("alpha", "Orchestrator", "", "ping")
	require.Equal(t, http.StatusOK, code, "one token per 6s")
	// Over a minute at most 3 + 10 get through.
	sent := 0
	for i := 0; i < 60; i++ {
		e.now = e.now.Add(time.Second)
		if c, _ := e.send("alpha", "Orchestrator", "", "loop"); c == http.StatusOK {
			sent++
		}
	}
	require.Equal(t, 10, sent)
	// The bucket is per sender: the Operator still has its own burst.
	code, _ = e.send("alpha", "Operator", "", "x")
	require.Equal(t, http.StatusOK, code)
}

func TestCrossTeamUnknownTargetIsNotFound(t *testing.T) {
	e := newCrossTeamEnv(t)
	e.enable("alpha", true)
	code, out := e.do("POST", "/api/board/no-such-board/external-messages", map[string]any{
		"from_board": "alpha", "subscriber_id": "Orchestrator", "session_name": "tmux-alphaOrchestrator", "content": "hi"})
	require.Equal(t, http.StatusNotFound, code)
	require.Contains(t, out["error"], `board "no-such-board" not found`)
}

// #4379: cross_team_artifacts is off by default, the Operator's alone to turn on, and never
// through Coral Hub.
func TestCrossTeamArtifactsSwitch(t *testing.T) {
	e := newCrossTeamEnv(t)
	code, out := e.do("GET", "/api/board/alpha/settings", nil)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, true, out["cross_team_artifacts"], "on by default: a kill switch (#5916)")
	for _, who := range []string{"Orchestrator", "Dev", ""} {
		code, _ = e.do("PUT", "/api/board/alpha/settings", map[string]any{"subscriber_id": who, "cross_team_artifacts": false})
		require.Equal(t, http.StatusForbidden, code, "%q cannot turn it off", who)
	}
	code, out = e.do("PUT", "/api/board/alpha/settings", map[string]any{"subscriber_id": "Operator", "cross_team_artifacts": false})
	require.Equal(t, http.StatusOK, code, out)
	put := func(hub bool) (int, map[string]any) {
		b, _ := json.Marshal(map[string]any{"subscriber_id": "Operator", "cross_team_artifacts": true})
		req, _ := http.NewRequest("PUT", e.url+"/api/board/alpha/settings", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		if hub {
			req.Header.Set(tunnel.ViaHeader, "1")
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	code, out = put(true)
	require.Equal(t, http.StatusForbidden, code, "not through Coral Hub")
	// Since #5307 the PUT is the owner's at Launch: a request without the owner marker is refused as owner_only (an owner's own attempt at a
	// cross-team field is local_only: hub_owner_settings_test.go).
	require.Equal(t, "owner_only", out["code"])
	code, out = e.do("GET", "/api/board/alpha/settings", nil)
	require.Equal(t, false, out["cross_team_artifacts"], "still off")
	code, out = put(false)
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, true, out["cross_team_artifacts"])
	require.Equal(t, true, out["cross_team_messaging"], "a separate switch: still its default")
}
