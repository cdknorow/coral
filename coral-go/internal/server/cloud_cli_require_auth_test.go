package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/auth"
	"github.com/subgentic/coral/internal/cloud"
)

// The signed `coral cloud connect` handshake (the hello, then a token-signed request) still works
// with require_auth_on_loopback ON: the signature is the credential, so the setting does not block
// it. A plain unsigned loopback request is still refused, and the setting-gated power (the input
// log delete) is refused for a tokenless local caller (#3585, Security's SHOULD).
func TestSignedCloudConnectWorksWithRequireAuthOnLoopback(t *testing.T) {
	r := newE2ERemote(t, "cloud-require-auth", nil)
	h := r.srv.Router()
	token, err := cloud.ReadCLIToken(r.cfg.CoralDir())
	require.NoError(t, err)
	r.srv.cloudHandler.SetPeerCheck(func(*http.Request) (bool, bool) { return true, true })

	auth.SetRequireAuthOnLoopback(true)
	t.Cleanup(func() { auth.SetRequireAuthOnLoopback(false) })

	do := func(path, body string, hdr map[string]string) int {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:50000"
		req.Host = "127.0.0.1:8420"
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	admitted := func(code int) bool { return code != http.StatusForbidden && code != http.StatusUnauthorized }

	// The signed redeem (the hello inside cliSign succeeds even with the setting on) is admitted.
	sig := cliSign(t, h, token, "POST", "/api/cloud/redeem", `{"check":true}`)
	assert.True(t, admitted(do("/api/cloud/redeem", `{"check":true}`, map[string]string{cloud.CLIAuthHeader: sig})),
		"a signed cloud-connect request must work with require_auth_on_loopback on")
	// A plain unsigned loopback request is still refused.
	assert.False(t, admitted(do("/api/cloud/redeem", `{"check":true}`, nil)), "an unsigned request is refused")

	// A setting-gated local power (delete the input log) is refused for a tokenless local caller.
	assert.False(t, admitted(do("/api/system/debug-input-log", `{}`, nil)),
		"a setting-gated local power is non-local with the setting on")
}
