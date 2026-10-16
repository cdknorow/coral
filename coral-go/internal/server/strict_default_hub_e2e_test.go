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
	"github.com/cdknorow/coral/internal/secrets"
	"github.com/cdknorow/coral/internal/server/routes"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
	"github.com/cdknorow/coral/pkg/hubproxy"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// #3936 (Coral Hub Security's five checks): with strict local auth on, as it is by default, the
// real hub proxy, tunnel and connector in front of the real router. (1) a request on this computer
// with a forged tunnel stamp is refused; (2) the connector's own stamp on an allowed route is
// admitted; (3) a stamp the hub user sends is replaced by the connector's, never passed on; (4) a
// route the tunnel table never allows is refused whatever credential the hub user adds; (5) a
// tunnelled request is never the person at this computer: no API key, no locality.
func TestStrictDefaultHubTunnelEndToEnd(t *testing.T) {
	dir := t.TempDir()
	cfg := newE2EConfig(t, dir)
	cfg.StrictLocalAuth = true // startup.Start's default since #3936
	db, err := storetest.Open(t, cfg.DBPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	stack := newTermStack(t)
	srv := NewWithKey(cfg, db, stack.backend, stack.terminal, "")
	srv.cloudHandler.SetLinkedCheck(func() bool { return true })
	local := httptest.NewServer(srv.Router())
	t.Cleanup(local.Close)
	key := srv.keyStore.Key()

	ss := store.NewSessionStore(db)
	require.NoError(t, ss.SetSetting(context.Background(), routes.MaxModeSetting, tunnel.TierInteract.String()))
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

	call := func(base, method, path string, hdr map[string]string) (int, map[string]any, string) {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, nil)
		req.Header.Set("Origin", base)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		for k, v := range hdr {
			req.Header.Set(k, v)
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
	viaHub := func(path string, hdr map[string]string) (int, map[string]any, string) {
		h := map[string]string{tunnel.HubUserHeader: "hub-user-1234abcd"}
		for k, v := range hdr {
			h[k] = v
		}
		return call(hub.URL, "GET", "/api/remote/srv"+path, h)
	}

	// Strict is on: this computer without a credential is refused (call sends a same-origin Origin
	// and Sec-Fetch-Site, which alone are not one); with the key, either header, it is served.
	code, out, _ := call(local.URL, "GET", "/api/sessions/live", nil)
	assert.Equal(t, http.StatusUnauthorized, code)
	assert.Equal(t, "dashboard_auth_required", out["code"])
	code, _, b := call(local.URL, "GET", "/api/sessions/live", map[string]string{"X-API-Key": key})
	assert.Equal(t, http.StatusOK, code, b)
	code, _, b = call(local.URL, "GET", "/api/sessions/live", map[string]string{"Authorization": "Bearer " + key})
	assert.Equal(t, http.StatusOK, code, b)
	code, out, _ = call(local.URL, "GET", "/api/sessions/live", map[string]string{"X-API-Key": "wrong"})
	assert.Equal(t, http.StatusUnauthorized, code)
	assert.Equal(t, "invalid_credential", out["code"])

	// (1) A forged stamp on this computer: refused, with or without the API key beside it.
	code, out, _ = call(local.URL, "GET", "/api/sessions/live", map[string]string{tunnel.ViaHeader: "forged"})
	assert.Equal(t, http.StatusUnauthorized, code)
	assert.Equal(t, "invalid_credential", out["code"])
	code, _, _ = call(local.URL, "GET", "/api/system/api-key", map[string]string{tunnel.ViaHeader: "forged", "Authorization": "Bearer " + key})
	assert.NotEqual(t, http.StatusOK, code, "a stamped request never reads the key, even with it")

	// (2) The connector's own stamp on an allowed route: admitted.
	code, _, b = viaHub("/api/sessions/live", nil)
	assert.Equal(t, http.StatusOK, code, b)

	// (3) A stamp the hub user sends is not passed on: the request is still admitted with the
	// connector's (a forged value reaching the server would be refused, as in (1)).
	code, _, b = viaHub("/api/sessions/live", map[string]string{tunnel.ViaHeader: "forged-by-hub-user"})
	assert.Equal(t, http.StatusOK, code, b)

	// (4) and (5) Routes the table never allows, with every credential a hub user could add: never
	// served, never the key, never "local".
	creds := map[string]string{"Authorization": "Bearer " + key, "X-API-Key": key, "Cookie": "coral_session=x"}
	for _, p := range []string{"/api/system/api-key", "/api/system/auth-status", "/api/system/editors"} {
		code, _, b := viaHub(p, creds)
		assert.NotEqual(t, http.StatusOK, code, p+" "+b)
		assert.NotContains(t, b, key, p)
		assert.NotContains(t, b, "localhost", p)
	}
}
