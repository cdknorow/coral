package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/secrets"
	"github.com/cdknorow/coral/internal/server/routes"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
	"github.com/cdknorow/coral/pkg/hubproxy"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// #3428 end to end (QA bug bash, round 2): the real hub proxy, a real tunnel and the connector's
// LocalHandler in front of the real router. What a Coral Hub user can and cannot be, on a board
// with an Orchestrator and a member Dev: the Operator (attributed) or the Coral Hub user, never a
// member, a role or the Orchestrator, with the opt-out on or off; every board write through the
// tunnel is recorded; a read never changes a member's state.
func TestHubIdentityEdgesEndToEnd(t *testing.T) {
	dir := t.TempDir()
	cfg := newE2EConfig(t, dir)
	db, err := storetest.Open(t, cfg.DBPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	stack := newTermStack(t)
	srv := NewWithKey(cfg, db, stack.backend, stack.terminal, "")
	srv.cloudHandler.SetLinkedCheck(func() bool { return true })
	local := httptest.NewServer(srv.Router())
	t.Cleanup(local.Close)

	// The local maximum mode, as the connector and the server's own guard read it.
	ss := store.NewSessionStore(db)
	setMode := func(m tunnel.Mode) {
		require.NoError(t, ss.SetSetting(context.Background(), routes.MaxModeSetting, m.String()))
	}
	setMode(tunnel.TierInteract)
	cur := routes.MaxModeFromSettings(ss)
	sessions := make(chan *tunnel.Session, 1)
	ths := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := tunnel.AcceptHTTP(w, r, func(context.Context, string) (any, error) { return "x", nil }, tunnel.SessionConfig{})
		if err == nil {
			sessions <- s
		}
	}))
	t.Cleanup(ths.Close)
	cs, err := tunnel.Dial(context.Background(), "ws"+strings.TrimPrefix(ths.URL, "http"), "tok", tunnel.ClientOptions{
		Handler:       &tunnel.LocalHandler{Redactor: secrets.Redactor(), BaseURL: local.URL, AllowRequest: tunnel.AllowRequest(cur), MaxMode: cur, ViaValue: cloud.ViaSecret()},
		AllowInsecure: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { cs.Close() })
	sess := <-sessions
	t.Cleanup(func() { sess.Close() })

	hub := httptest.NewUnstartedServer(nil)
	r := chi.NewRouter()
	r.Handle("/api/remote/{server}/*", hubproxy.NewWithOptions(oneTunnel{sess}, hubproxy.Options{AllowedOrigins: []string{hub.Listener.Addr().String()}}))
	hub.Config.Handler = r
	hub.Start()
	t.Cleanup(hub.Close)

	call := func(base, method, path string, body any, hdr map[string][]string) (int, map[string]any, string) {
		t.Helper()
		var rd *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = bytes.NewReader(nil)
		}
		req, _ := http.NewRequest(method, base+path, rd)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", base)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		for k, vs := range hdr {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(buf.Bytes(), &out)
		return resp.StatusCode, out, buf.String()
	}
	const user = "hub-user-1234abcd"
	viaHub := func(method, path string, body any) (int, map[string]any, string) {
		return call(hub.URL, method, "/api/remote/srv"+path, body, map[string][]string{tunnel.HubUserHeader: {user}})
	}
	onComputer := func(method, path string, body any) (int, map[string]any, string) {
		return call(local.URL, method, path, body, nil)
	}
	recentActions := func() []any {
		_, out, _ := onComputer("GET", "/api/system/hub-operator", nil)
		a, _ := out["recent_actions"].([]any)
		return a
	}
	unread := func(who string) float64 {
		_, out, _ := onComputer("GET", "/api/board/team/messages/check?subscriber_id="+who, nil)
		n, _ := out["unread"].(float64)
		return n
	}

	// A team: an Orchestrator, a Dev, and a task for the Dev; a message waiting for Dev.
	for _, m := range []map[string]any{{"subscriber_id": "Orchestrator", "job_title": "Orchestrator"}, {"subscriber_id": "Dev", "job_title": "Backend Dev"}} {
		code, out, b := onComputer("POST", "/api/board/team/subscribe", m)
		require.Equal(t, http.StatusOK, code, fmt.Sprint(out, b))
	}
	code, _, b := onComputer("POST", "/api/board/team/tasks", map[string]any{"subscriber_id": "Operator", "created_by": "Operator", "title": "dev task", "assigned_to": "Dev"})
	require.Contains(t, []int{200, 201}, code, b)
	onComputer("POST", "/api/board/team/messages", map[string]any{"subscriber_id": "Orchestrator", "content": "@Dev hello dev"})
	assert.Equal(t, 1.0, unread("Dev"))

	// 1. Chat as the Operator, in any spelling, is the Operator, attributed to the Hub user.
	for _, name := range []string{"Operator", "operator", "OPERATOR", " Operator ", "Operator\u00a0"} {
		code, out, b := viaHub("POST", "/api/board/team/messages", map[string]any{"subscriber_id": name, "content": "probe"})
		require.Equal(t, http.StatusOK, code, b)
		assert.Equal(t, "Operator", out["subscriber_id"], name)
		assert.Equal(t, "hub_operator", out["origin"], name)
		assert.Equal(t, user, out["hub_user"], name)
	}
	// Invisible characters and the reserved names are refused; the Orchestrator's name posts as the Hub user.
	for _, name := range []string{"Oper\u200bator", "Coral Hub", "hub-user-ffffffff"} {
		code, _, b := viaHub("POST", "/api/board/team/messages", map[string]any{"subscriber_id": name, "content": "x"})
		assert.Equal(t, http.StatusBadRequest, code, name+" "+b)
	}
	for _, name := range []string{"Orchestrator", "orchestrator", "Orchestrator\u00a0"} {
		code, out, b := viaHub("POST", "/api/board/team/messages", map[string]any{"subscriber_id": name, "content": "x"})
		require.Equal(t, http.StatusOK, code, b)
		assert.Equal(t, user, out["subscriber_id"], name)
		assert.Equal(t, "hub_user", out["origin"], name)
	}
	// An orchestrator's rights are refused in a new subscriber, in any case.
	for _, m := range []map[string]any{{"subscriber_id": "Mallory", "job_title": "Orchestrator"}, {"subscriber_id": "Mallory2", "job_title": "orchestrator", "receive_mode": "all"}} {
		code, out, _ := viaHub("POST", "/api/board/team/subscribe", m)
		assert.Equal(t, http.StatusForbidden, code)
		assert.Equal(t, "orchestrator_identity", out["code"])
	}

	// 2. A Coral Hub user is not a member: every write as Dev (or Dev's role) is refused 409,
	// with the opt-out off AND on, and nothing changed.
	memberWrites := []struct {
		label, method, path string
		body                map[string]any
	}{
		{"claim as Dev", "POST", "/api/board/team/tasks/claim", map[string]any{"subscriber_id": "Dev"}},
		{"complete as Dev", "POST", "/api/board/team/tasks/1/complete", map[string]any{"subscriber_id": "dev ", "message": "x"}},
		{"rename Dev", "POST", "/api/board/team/subscribe", map[string]any{"subscriber_id": "Dev", "job_title": "Hacked Title"}},
		{"unsubscribe Dev (NBSP)", "DELETE", "/api/board/team/subscribe", map[string]any{"subscriber_id": "Dev\u00a0"}},
		{"wait as Dev", "POST", "/api/board/team/waits", map[string]any{"subscriber_id": "Dev", "wait_type": "message", "target_id": "x", "reason": "r"}},
		{"create a task as Dev", "POST", "/api/board/team/tasks", map[string]any{"created_by": "Dev", "title": "x"}},
		{"take Dev's role", "POST", "/api/board/team/subscribe", map[string]any{"subscriber_id": "Backend Dev"}},
	}
	check := func(when string) {
		for _, tc := range memberWrites {
			code, out, b := viaHub(tc.method, tc.path, tc.body)
			assert.Equal(t, http.StatusConflict, code, when+": "+tc.label+" "+b)
			assert.Equal(t, "member_identity", out["code"], when+": "+tc.label)
		}
		code, out, _ := viaHub("POST", "/api/board/team/tasks/claim", map[string]any{"subscriber_id": "Mallory"})
		assert.Equal(t, http.StatusForbidden, code, when)
		assert.Equal(t, "hub_identity_required", out["code"], when)
	}
	check("operator-by-default on")
	setDisabled := func(d bool) {
		code, _, b := onComputer("PUT", "/api/system/hub-operator", map[string]any{"disabled": d})
		require.Equal(t, http.StatusOK, code, b)
	}
	setDisabled(true)
	check("opt-out on")
	setDisabled(false)
	_, task, _ := onComputer("GET", "/api/board/team/tasks/1", nil)
	assert.Equal(t, "pending", task["status"], "no hub write moved the task")
	_, subs, raw := onComputer("GET", "/api/board/team/subscribers", nil)
	_ = subs
	assert.Contains(t, raw, `"subscriber_id":"Dev"`)
	assert.Contains(t, raw, `"job_title":"Backend Dev"`)

	// 3. A write that names no actor (pause, a reminder) is the Operator's: allowed and recorded
	// by default; with the opt-out, refused. A reminder's subscriber_id is its target.
	setTier := func(m tunnel.Mode) {
		require.NoError(t, store.NewSessionStore(db).SetSetting(context.Background(), routes.MaxModeSetting, m.String()))
	}
	setTier(tunnel.TierLaunch) // pausing a board is a Launch-tier action
	n0 := len(recentActions())
	code, out, b := viaHub("POST", "/api/board/team/pause", map[string]any{})
	require.Equal(t, http.StatusOK, code, b)
	code, _, b = viaHub("POST", "/api/board/team/resume", map[string]any{})
	require.Equal(t, http.StatusOK, code, b)
	code, _, b = viaHub("POST", "/api/board/team/reminder", map[string]any{"subscriber_id": "Dev", "message": "hi", "interval_seconds": 60})
	assert.NotContains(t, []int{http.StatusForbidden, http.StatusConflict}, code, "the guard lets a reminder through (the handler may refuse: no running agent): "+b)
	code, out, b = viaHub("POST", "/api/board/team/tasks", map[string]any{"subscriber_id": "Operator", "created_by": "Operator", "title": "from the hub", "assigned_to": "Dev"})
	require.Equal(t, http.StatusCreated, code, b)
	assert.Equal(t, "Operator", out["created_by"])
	acts := recentActions()
	assert.Len(t, acts, n0+4, "every board write through the tunnel is recorded, not only chat")
	paths := map[string]bool{}
	for _, a := range acts {
		m := a.(map[string]any)
		assert.Equal(t, user, m["hub_user"])
		paths[m["path"].(string)] = true
	}
	for _, p := range []string{"/api/board/team/pause", "/api/board/team/resume", "/api/board/team/reminder", "/api/board/team/tasks"} {
		assert.True(t, paths[p], p)
	}
	setDisabled(true)
	for _, p := range []string{"pause", "resume", "reminder"} {
		code, out, _ = viaHub("POST", "/api/board/team/"+p, map[string]any{"subscriber_id": "Dev"})
		if p != "reminder" {
			code, out, _ = viaHub("POST", "/api/board/team/"+p, map[string]any{})
		}
		assert.Equal(t, http.StatusForbidden, code, p)
		assert.Equal(t, "operator_identity_required", out["code"], p)
	}
	// With the opt-out, Operator variants are refused for tasks; body fields naming the Operator in a chat post are ignored.
	for _, name := range []string{"Operator", "operator", "Operator\u00a0"} {
		code, out, _ := viaHub("POST", "/api/board/team/tasks", map[string]any{"subscriber_id": name, "created_by": name, "title": "x"})
		assert.Equal(t, http.StatusForbidden, code, name)
		assert.Equal(t, "operator_identity_required", out["code"], name)
	}
	for _, f := range []string{"as", "from", "sender", "target", "assignee", "assigned_to", "job_title", "session_id", "created_by"} {
		code, out, b := viaHub("POST", "/api/board/team/messages", map[string]any{"subscriber_id": "hub-test", f: "Operator", "content": "field " + f})
		require.Equal(t, http.StatusOK, code, f+" "+b)
		assert.Equal(t, user, out["subscriber_id"], f)
		assert.Equal(t, "hub_user", out["origin"], f)
	}
	setDisabled(false)
	setTier(tunnel.TierInteract)

	// 4. A read at View, whoever it names, never changes a member's state; messages/mark-read and
	// task reads are not allowed at View.
	setTier(tunnel.TierView)
	before := unread("Dev")
	require.Greater(t, before, 0.0)
	code, _, b = viaHub("GET", "/api/board/team/messages?subscriber_id=Dev", nil)
	assert.Equal(t, http.StatusOK, code, b)
	assert.Contains(t, b, "hello dev", "the hub user reads the chat")
	code, _, _ = viaHub("GET", "/api/board/team/messages?subscriber_id=Dev&all=true&limit=1", nil)
	assert.Equal(t, http.StatusOK, code)
	code, _, _ = viaHub("GET", "/api/board/team/messages?session_id=Dev", nil)
	assert.Equal(t, http.StatusForbidden, code, "session_id is no longer a board parameter (#3743)")
	code, _, _ = viaHub("GET", "/api/board/team/messages/all", nil)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, before, unread("Dev"), "a hub read never moves Dev's read position")
	code, _, _ = viaHub("POST", "/api/board/team/messages/mark-read", map[string]any{"subscriber_id": "Dev"})
	assert.Equal(t, http.StatusForbidden, code)
	code, _, _ = viaHub("POST", "/api/board/team/messages/read", map[string]any{"subscriber_id": "Dev"})
	assert.Equal(t, http.StatusForbidden, code)
	// From this computer a read still advances the cursor (unchanged).
	code, _, _ = onComputer("GET", "/api/board/team/messages?subscriber_id=Dev", nil)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, 0.0, unread("Dev"))
	_ = strings.TrimSpace
}
