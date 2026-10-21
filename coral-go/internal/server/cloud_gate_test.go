package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/peercred"
	"github.com/subgentic/coral/pkg/tunnel"
)

// The /api/cloud routes (#3149 ruling 3): another local user is
// refused whatever it sends; then the dashboard (browser rules) or the command
// line with the 0600 token from Coral's data folder, per route; a loopback
// caller with neither is refused; never through the tunnel.
func TestCloudRoutesSameUserGate(t *testing.T) {
	r := newE2ERemote(t, "cloud-gate", nil)
	h := r.srv.Router()
	token, err := cloud.ReadCLIToken(r.cfg.CoralDir())
	require.NoError(t, err, "the server wrote its command-line token at startup")

	type route struct {
		method, path, body string
		dashboard, cli     bool
	}
	routes := []route{
		{"GET", "/api/cloud/status", "", true, true},
		{"POST", "/api/cloud/connect/cancel", `{}`, true, true},
		{"POST", "/api/cloud/connect", `{}`, true, true},
		{"POST", "/api/cloud/disconnect", `{}`, true, true},
		{"POST", "/api/cloud/rekey", `{"confirm":true}`, true, true}, // #3698 (the key is usable here: 409)
		{"PUT", "/api/cloud/max-mode", `{"mode":"view"}`, true, true},
		{"PUT", "/api/cloud/allow-auto-accept", `{"allow":true}`, true, true},
		{"PUT", "/api/cloud/hub-relaunch", `{"allow":true}`, true, true}, // #5280
		{"POST", "/api/cloud/redeem", `{"check":true}`, false, true},
	}
	dashboard := map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://127.0.0.1:8420"}
	// The command line signs each request after a hello (cloud.CLIAuth).
	cli := func(rt route, tok string) map[string]string {
		return map[string]string{cloud.CLIAuthHeader: cliSign(t, h, tok, rt.method, rt.path, rt.body)}
	}
	do := func(rt route, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(rt.method, rt.path, strings.NewReader(rt.body))
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
	// Refused by the route (403) or, for a wrong credential, before it (401, #3421 v2).
	admitted := func(rec *httptest.ResponseRecorder) bool {
		return rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized
	}

	for _, peer := range []struct {
		name            string
		same, supported bool
	}{{"a system with no peer check", false, false}, {"Coral's user", true, true}} {
		r.srv.cloudHandler.SetPeerCheck(func(*http.Request) (bool, bool) { return peer.same, peer.supported })
		for _, rt := range routes {
			name := peer.name + " " + rt.method + " " + rt.path
			assert.False(t, admitted(do(rt, nil)), "a plain loopback caller: %s", name)
			assert.Equal(t, rt.dashboard, admitted(do(rt, dashboard)), "the dashboard: %s", name)
			assert.Equal(t, rt.cli, admitted(do(rt, cli(rt, token))), "the command line, signed with the token: %s", name)
			replay := cli(rt, token)
			do(rt, replay)
			assert.False(t, admitted(do(rt, replay)), "a signature used once already: %s", name)
			assert.False(t, admitted(do(rt, cli(rt, token+"x"))), "signed with a wrong token: %s", name)
			assert.False(t, admitted(do(rt, map[string]string{cloud.CLIAuthHeader: ""})), "an empty signature: %s", name)
			assert.False(t, admitted(do(rt, map[string]string{"X-Coral-CLI-Token": token})), "the raw token is never accepted: %s", name)
			assert.False(t, admitted(do(rt, mergeHdr(cli(rt, token), map[string]string{tunnel.ViaHeader: cloud.ViaSecret()}))), "through the tunnel: %s", name)
			assert.False(t, admitted(do(rt, mergeHdr(dashboard, map[string]string{tunnel.ViaHeader: cloud.ViaSecret()}))), "through the tunnel: %s", name)
		}
	}

	// Another user (or a sidecar running as another uid): refused with
	// code other_user even with perfect dashboard headers and the right token.
	signed := map[string][]map[string]string{}
	for _, rt := range routes { // signed while the hello still admits Coral's user
		signed[rt.path] = []map[string]string{cli(rt, token), cli(rt, token)}
	}
	r.srv.cloudHandler.SetPeerCheck(func(*http.Request) (bool, bool) { return false, true })
	for _, rt := range routes {
		for _, hdr := range []map[string]string{dashboard, signed[rt.path][0], mergeHdr(dashboard, signed[rt.path][1])} {
			rec := do(rt, hdr)
			assert.Equal(t, http.StatusForbidden, rec.Code, rt.path)
			assert.Contains(t, rec.Body.String(), `"code":"other_user"`, rt.path)
		}
	}

	// The hello answers only a command-line caller on this computer, as Coral's
	// user: never another user, the tunnel, a browser or another machine.
	hello := func(hdr map[string]string, remote string) *httptest.ResponseRecorder {
		cn, _ := cloud.NewCLINonce()
		req := httptest.NewRequest("POST", cloud.CLIHelloPath, strings.NewReader(`{"nonce":"`+cn+`"}`))
		req.RemoteAddr, req.Host = remote, "127.0.0.1:8420"
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	rec := hello(nil, "127.0.0.1:50000")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), `"code":"other_user"`)
	r.srv.cloudHandler.SetPeerCheck(func(*http.Request) (bool, bool) { return true, true })
	assert.Equal(t, http.StatusOK, hello(nil, "127.0.0.1:50000").Code)
	for name, hdr := range map[string]map[string]string{
		"tunnel":  {tunnel.ViaHeader: cloud.ViaSecret()},
		"browser": {"Sec-Fetch-Site": "same-origin"},
		"origin":  {"Origin": "http://127.0.0.1:8420"},
	} {
		assert.Equal(t, http.StatusForbidden, hello(hdr, "127.0.0.1:50000").Code, name)
	}
	assert.NotEqual(t, http.StatusOK, hello(nil, "192.168.1.20:5000").Code, "another machine")
}

// #4041: hellos are limited per caller, and every local peer is one caller, so a flood from
// both loopback addresses gets the marked per-action 429 (the dashboard does not take it for
// an overloaded server) instead of filling the pending table.
func TestCLIHelloFloodGetsAMarked429(t *testing.T) {
	r := newE2ERemote(t, "cli-hello-limit", nil)
	h := r.srv.Router()
	r.srv.cloudHandler.SetPeerCheck(func(*http.Request) (bool, bool) { return true, true })
	ok, limited := 0, 0
	for i := 0; i < 300; i++ {
		cn, _ := cloud.NewCLINonce()
		req := httptest.NewRequest("POST", cloud.CLIHelloPath, strings.NewReader(`{"nonce":"`+cn+`"}`))
		req.RemoteAddr = []string{"127.0.0.1:50000", "[::1]:50002"}[i%2]
		req.Host = "127.0.0.1:8420"
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		switch rec.Code {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
			limited++
			assert.Equal(t, "action", rec.Header().Get("X-Coral-Limit"))
			assert.Contains(t, rec.Body.String(), `"code":"cli_hello_limit"`)
		default:
			t.Fatalf("hello %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	assert.GreaterOrEqual(t, ok, 100, "the burst is served")
	assert.Less(t, ok, 200, "one caller, whatever its loopback address")
	assert.Equal(t, 300, ok+limited)
}

// Through a real socket the kernel's answer is used: Coral's own user passes
// (the lookup finds this process's socket on Linux, macOS and Windows; on other
// systems it is not supported and the dashboard rules apply).
func TestCloudRoutesRealSocketSameUser(t *testing.T) {
	r := newE2ERemote(t, "cloud-gate-real", nil)
	r.srv.cloudHandler.SetPeerCheck(peercred.SameUser)
	srv := httptest.NewServer(r.srv.Router())
	defer srv.Close()
	req, err := http.NewRequest("GET", srv.URL+"/api/cloud/status", nil)
	require.NoError(t, err)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Origin", srv.URL)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// #3285 (security review): no command-line token outlives a graceful shutdown.
func TestCloseCloudRemovesTheCLIToken(t *testing.T) {
	r := newE2ERemote(t, "cloud-close", nil)
	path := filepath.Join(r.cfg.CoralDir(), cloud.CLITokenFile)
	require.FileExists(t, path, "written at startup")
	r.srv.CloseCloud()
	assert.NoFileExists(t, path)
}
