package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/server/routes"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
	"github.com/cdknorow/coral/pkg/hubproxy"
	"github.com/cdknorow/coral/pkg/tunnel"
)

type oneTunnel struct{ sess *tunnel.Session }

func (o oneTunnel) Resolve(_ context.Context, id string) (hubproxy.Target, error) {
	if id != "srv" {
		return nil, hubproxy.ErrUnknownServer
	}
	return hubproxy.TunnelTarget{Session: o.sess}, nil
}

// #3404 end to end: the real hub proxy, a real tunnel and the connector's LocalHandler
// (with this process's stamp), in front of the real Coral router. A Coral Hub user at
// Interact acts as the Operator by default, attributed to the Hub user; the opt-out,
// the missing or repeated header, View mode and the local-only settings are refused.
func TestHubOperatorEndToEnd(t *testing.T) {
	dir := t.TempDir()
	cfg := newE2EConfig(t, dir)
	db, err := storetest.Open(t, cfg.DBPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	stack := newTermStack(t)
	srv := New(cfg, db, stack.backend, stack.terminal)
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
		Handler:       &tunnel.LocalHandler{BaseURL: local.URL, AllowRequest: tunnel.AllowRequest(cur), MaxMode: cur, ViaValue: cloud.ViaSecret()},
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

	call := func(base, method, path string, body any, hdr map[string][]string) (int, map[string]any) {
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
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	viaHub := func(method, path string, body any, hubUsers ...string) (int, map[string]any) {
		return call(hub.URL, method, "/api/remote/srv"+path, body, map[string][]string{tunnel.HubUserHeader: hubUsers})
	}
	onComputer := func(method, path string, body any) (int, map[string]any) {
		return call(local.URL, method, path, body, nil)
	}
	const user = "hub-user-1234abcd"

	code, out := onComputer("POST", "/api/board/team/subscribe", map[string]any{"subscriber_id": "Dev", "job_title": "Backend Dev"})
	require.Equal(t, http.StatusOK, code, out)

	// The hub can read the setting: on by default.
	code, out = viaHub("GET", "/api/system/hub-operator", nil)
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, map[string]any{"enabled": true, "disabled": false}, out)

	// A chat post as the Operator: the Operator, attributed, and agents can tell.
	code, out = viaHub("POST", "/api/board/team/messages", map[string]any{"subscriber_id": "Operator", "as": "Operator", "content": "@Dev ship it"}, user)
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, "Operator", out["subscriber_id"])
	assert.Equal(t, "hub_operator", out["origin"])
	assert.Equal(t, user, out["hub_user"])
	assert.Equal(t, "[Operator via Coral Hub, hub-user-1234abcd] @Dev ship it", out["content"])

	// An Operator task action: allowed, recorded with the Hub user.
	code, out = viaHub("POST", "/api/board/team/tasks", map[string]any{"subscriber_id": "Operator", "created_by": "Operator", "title": "from the hub", "assigned_to": "Dev"}, user)
	require.Equal(t, http.StatusCreated, code, out)
	assert.Equal(t, "Operator", out["created_by"])
	// No Hub user, or a repeated one: never the Operator.
	code, out = viaHub("POST", "/api/board/team/tasks", map[string]any{"subscriber_id": "Operator", "created_by": "Operator", "title": "x"})
	assert.Equal(t, http.StatusForbidden, code, out)
	assert.Equal(t, "hub_user_required", out["code"])
	code, out = viaHub("POST", "/api/board/team/tasks", map[string]any{"subscriber_id": "Operator", "created_by": "Operator", "title": "x"}, user, "hub-user-5678abcd")
	assert.Equal(t, http.StatusBadRequest, code, out)
	code, out = viaHub("POST", "/api/board/team/messages", map[string]any{"subscriber_id": "Operator", "content": "x"})
	require.Equal(t, http.StatusBadRequest, code, out)
	assert.Equal(t, "hub_user_required", out["code"], "no Hub user: refused, never posted as anyone (#3743)")

	// Changing the team's working mode (what every agent is told) is the Operator's and is
	// recorded with the Hub user like any board write (#3433).
	code, out = viaHub("PUT", "/api/board/team/working-mode", map[string]any{"mode": "none", "dependency_guidance": false, "custom_instructions": "from the hub"}, user)
	require.Equal(t, http.StatusOK, code, out)

	code, out = onComputer("GET", "/api/system/hub-operator", nil)
	require.Equal(t, http.StatusOK, code, out)
	recent, _ := out["recent_actions"].([]any)
	require.Len(t, recent, 3, out) // every board write through the tunnel is recorded (#3428); the post without a Hub user was refused
	paths := []string{}
	for _, a := range recent {
		m := a.(map[string]any)
		assert.Equal(t, user, m["hub_user"])
		paths = append(paths, m["path"].(string))
	}
	assert.ElementsMatch(t, []string{"/api/board/team/messages", "/api/board/team/tasks", "/api/board/team/working-mode"}, paths)

	// #3646: a webhook URL is refused (Coral sends no webhooks), through the hub too.
	code, out = viaHub("POST", "/api/board/team/subscribe", map[string]any{"subscriber_id": "Operator", "job_title": "Operator", "webhook_url": "https://93.184.216.34/hook"}, user)
	assert.Equal(t, http.StatusBadRequest, code, out)
	assert.Equal(t, "webhooks are not supported", out["error"])

	// Never changed through the hub; the board's settings neither.
	code, _ = viaHub("PUT", "/api/system/hub-operator", map[string]any{"disabled": false})
	assert.Equal(t, http.StatusForbidden, code)
	code, _ = viaHub("PUT", "/api/board/team/settings", map[string]any{"subscriber_id": "Operator", "cross_team_messaging": true}, user)
	assert.Equal(t, http.StatusForbidden, code)

	// The owner's opt-out, on this computer.
	code, out = onComputer("PUT", "/api/system/hub-operator", map[string]any{"disabled": true})
	require.Equal(t, http.StatusOK, code, out)
	code, out = viaHub("GET", "/api/system/hub-operator", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, map[string]any{"enabled": false, "disabled": true}, out)
	code, out = viaHub("POST", "/api/board/team/tasks", map[string]any{"subscriber_id": "Operator", "created_by": "Operator", "title": "x"}, user)
	assert.Equal(t, http.StatusForbidden, code, out)
	assert.Equal(t, "operator_identity_required", out["code"])
	code, out = viaHub("POST", "/api/board/team/messages", map[string]any{"subscriber_id": "Operator", "as": "Operator", "content": "hi"}, user)
	require.Equal(t, http.StatusOK, code, out)
	assert.Equal(t, user, out["subscriber_id"])
	assert.Equal(t, "hi", out["content"])

	// View can't post.
	setMode(tunnel.TierView)
	code, _ = viaHub("POST", "/api/board/team/messages", map[string]any{"subscriber_id": "Operator", "content": "x"}, user)
	assert.Equal(t, http.StatusForbidden, code)
}
