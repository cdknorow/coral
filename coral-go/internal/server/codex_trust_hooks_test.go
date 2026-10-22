package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #153: "Trust Coral's hooks in Codex" is on by default, written only by its own route on this computer (the dashboard or
// the signed command line; TestCloudRoutesSameUserGate covers who may call it), never by PUT /api/settings and never
// through the tunnel, whose table doesn't have the route.
func TestCodexTrustHooksSetting(t *testing.T) {
	r := newE2ERemote(t, "codex-trust", nil)
	h := r.srv.Router()
	r.srv.cloudHandler.SetPeerCheck(func(*http.Request) (bool, bool) { return true, true })
	dashboard := map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "same-origin", "Origin": "http://127.0.0.1:8420"}
	status := func() map[string]any {
		rec := call(h, "GET", "/api/system/codex-trust-hooks", "", dashboard)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}
	assert.Equal(t, true, status()["trust"], "on by default")

	rec := call(h, "PUT", "/api/system/codex-trust-hooks", `{"trust":false}`, dashboard)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, false, status()["trust"])
	assert.Equal(t, false, status()["trusted"], "off: never trusted, whatever the folder")
	s, err := store.NewSessionStore(r.db).GetSettings(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "false", s["codex_trust_coral_hooks"])

	rec = call(h, "PUT", "/api/system/codex-trust-hooks", `{}`, dashboard)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "trust is required")

	rec = call(h, "PUT", "/api/settings", `{"codex_trust_coral_hooks":"true"}`, dashboard)
	assert.NotEqual(t, http.StatusOK, rec.Code, "PUT /api/settings refuses it: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "/api/system/codex-trust-hooks", "and names its route")
	assert.Equal(t, false, status()["trust"], "unchanged")

	for _, m := range []string{"GET", "PUT"} {
		assert.Equal(t, tunnel.TierNever, tunnel.Classify(m, "/api/system/codex-trust-hooks", ""), "%s is never reachable through the tunnel", m)
	}
}

// Who may call the routes, as for the Coral Hub switches (TestCloudRoutesSameUserGate, which runs out of command-line
// hellos with more routes, so these are checked on their own server): the dashboard and the signed command line, as
// Coral's user; never a plain loopback caller, another user, or the tunnel.
func TestCodexTrustHooksGate(t *testing.T) {
	r := newE2ERemote(t, "codex-trust-gate", nil)
	h := r.srv.Router()
	token, err := cloud.ReadCLIToken(r.cfg.CoralDir())
	require.NoError(t, err)
	dashboard := map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://127.0.0.1:8420"}
	do := func(method, body string, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/system/codex-trust-hooks", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:50000"
		req.Host = "127.0.0.1:8420"
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	admitted := func(rec *httptest.ResponseRecorder) bool {
		return rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized
	}
	for _, rt := range []struct{ method, body string }{{"GET", ""}, {"PUT", `{"trust":true}`}} {
		cli := func() map[string]string {
			return map[string]string{cloud.CLIAuthHeader: cliSign(t, h, token, rt.method, "/api/system/codex-trust-hooks", rt.body)}
		}
		r.srv.cloudHandler.SetPeerCheck(func(*http.Request) (bool, bool) { return true, true })
		assert.False(t, admitted(do(rt.method, rt.body, nil)), "%s: a plain loopback caller", rt.method)
		assert.True(t, admitted(do(rt.method, rt.body, dashboard)), "%s: the dashboard", rt.method)
		assert.True(t, admitted(do(rt.method, rt.body, cli())), "%s: the signed command line", rt.method)
		assert.False(t, admitted(do(rt.method, rt.body, mergeHdr(dashboard, map[string]string{tunnel.ViaHeader: cloud.ViaSecret()}))), "%s: through the tunnel", rt.method)
		assert.False(t, admitted(do(rt.method, rt.body, mergeHdr(cli(), map[string]string{tunnel.ViaHeader: cloud.ViaSecret()}))), "%s: the command line through the tunnel", rt.method)
		r.srv.cloudHandler.SetPeerCheck(func(*http.Request) (bool, bool) { return false, true })
		assert.False(t, admitted(do(rt.method, rt.body, dashboard)), "%s: another user, with dashboard headers", rt.method)
	}
}
