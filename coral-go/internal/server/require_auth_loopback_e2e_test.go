package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/auth"
	"github.com/cdknorow/coral/internal/cliauth"
	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// require_auth_on_loopback ON (with strict-local-auth OFF) makes a direct-local request
// strict-local, not remote (#3585): a tokenless loopback request is refused; an agent session
// token and a signed command line are verified and work; a bad token is refused; and a request
// a local proxy forwarded stays remote (key or session only), so even a valid agent token on it
// is refused. CONNECT and the health check stay open.
func TestRequireAuthOnLoopbackIsStrictLocal(t *testing.T) {
	// The setting gives the same results whether StrictLocalAuth is off (the setting does the work)
	// or already on (the two are the same enforcement; turning both on changes nothing) -- Security's NIT.
	for _, strictLocal := range []bool{false, true} {
		t.Run(map[bool]string{false: "strict-local-auth off", true: "strict-local-auth also on"}[strictLocal], func(t *testing.T) {
			requireAuthStrictLocalCase(t, strictLocal)
		})
	}
}

func requireAuthStrictLocalCase(t *testing.T, strictLocal bool) {
	srv, ts, dir := strictServer(t, strictLocal)
	auth.SetRequireAuthOnLoopback(true)
	t.Cleanup(func() { auth.SetRequireAuthOnLoopback(false) })

	post := func(path, body string, hdr http.Header) (int, []byte) {
		req, _ := http.NewRequest("POST", ts.URL+path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header[k] = v
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	get := func(path string, hdr http.Header) int {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		for k, v := range hdr {
			req.Header[k] = v
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}

	// A plain tokenless loopback request is refused.
	assert.Equal(t, http.StatusUnauthorized, get("/api/sessions/live", nil), "tokenless loopback needs auth")
	// The health check stays open.
	assert.Equal(t, http.StatusOK, get("/api/health", nil))

	// A signed command line (the recovery path, coral open) works.
	tok, err := cliauth.ReadToken(dir)
	require.NoError(t, err)
	cli := &http.Client{Transport: cliauth.Transport{Base: http.DefaultTransport, Token: tok}}
	req, _ := http.NewRequest("POST", ts.URL+"/api/system/login-token", bytes.NewBufferString("{}"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := cli.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "a signed login-token request works with the setting on")

	// An agent session token works for its own board identity; a bad token is refused.
	ctx := context.Background()
	_, err = srv.boardStore.Subscribe(ctx, "team", "Dev", "Backend Dev", "claude-dev", nil, nil, "")
	require.NoError(t, err)
	agentTok, err := srv.db.MintSessionToken(ctx, "sid-dev", "claude-dev")
	require.NoError(t, err)
	agent := http.Header{store.SessionTokenHeader: {agentTok}, "User-Agent": {"coral-board/dev"}}
	code, body := post("/api/board/team/messages", `{"subscriber_id":"Dev","content":"mine"}`, agent)
	assert.Equal(t, http.StatusOK, code, "a valid agent token posts: %s", body)
	code, _ = post("/api/board/team/messages", `{"subscriber_id":"Dev","content":"x"}`, http.Header{store.SessionTokenHeader: {"not-a-real-token"}, "User-Agent": {"coral-board/dev"}})
	assert.Equal(t, http.StatusUnauthorized, code, "a bad agent token is refused")

	// A coral-hook-* / coral-board request (its token) on a direct-local request works: same as
	// the agent-token post above, which is what those binaries send.

	// A request a local proxy forwarded stays REMOTE: even a valid agent token on it is refused,
	// because a proxied request never gets the loopback pass that lets the token be honoured.
	proxied := http.Header{store.SessionTokenHeader: {agentTok}, "User-Agent": {"coral-board/dev"}, "X-Forwarded-For": {"100.64.0.9"}}
	code, _ = post("/api/board/team/messages", `{"subscriber_id":"Dev","content":"via proxy"}`, proxied)
	assert.Equal(t, http.StatusUnauthorized, code, "a proxied request with a valid agent token is still 401")

	// A forged tunnel stamp on a loopback request gets only the stricter treatment, never trust.
	forged := http.Header{tunnelViaHeader(): {"forged"}, store.SessionTokenHeader: {agentTok}, "User-Agent": {"coral-board/dev"}}
	code, _ = post("/api/board/team/messages", `{"subscriber_id":"Dev","content":"forged stamp"}`, forged)
	assert.NotEqual(t, http.StatusOK, code, "a forged X-Coral-Via-Tunnel is not trusted")

	// A signed cloud-connect redeem passes refuseCloud (the earlier handshake question).
	sig := cliSign(t, srv.Router(), tok, "POST", "/api/cloud/redeem", `{"check":true}`)
	code, _ = post("/api/cloud/redeem", `{"check":true}`, http.Header{cloud.CLIAuthHeader: {sig}})
	assert.NotEqual(t, http.StatusForbidden, code, "coral cloud connect redeem passes refuseCloud with the setting on")
	assert.NotEqual(t, http.StatusUnauthorized, code)
}

func tunnelViaHeader() string { return tunnel.ViaHeader }
