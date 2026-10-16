package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/cliauth"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

func strictServer(t *testing.T, strict bool) (*Server, *httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := newE2EConfig(t, dir)
	cfg.StrictLocalAuth = strict
	db, err := storetest.Open(t, cfg.DBPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	stack := newTermStack(t)
	srv := New(cfg, db, stack.backend, stack.terminal)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	return srv, ts, dir
}

func codeOfBody(b []byte) string {
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	s, _ := out["code"].(string)
	return s
}

// #3418 end to end, the real router with --strict-local-auth: a local request needs a
// credential; `coral open`'s signed request mints a sign-in link, the link gives the
// browser its cookie, and the dashboard then works; a person's signed command-line request
// works; an agent's token works for its own identity and is refused as anyone else.
func TestStrictLocalAuthEndToEnd(t *testing.T) {
	srv, ts, dir := strictServer(t, true)
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	plain := &http.Client{CheckRedirect: noRedirect}
	get := func(c *http.Client, path string, hdr http.Header) (int, []byte, http.Header) {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		for k, v := range hdr {
			req.Header[k] = v
		}
		resp, err := c.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b, resp.Header
	}

	// Locked.
	code, body, _ := get(plain, "/api/sessions/live", nil)
	assert.Equal(t, http.StatusUnauthorized, code)
	assert.Equal(t, "dashboard_auth_required", codeOfBody(body))
	code, _, hdr := get(plain, "/", nil)
	assert.Equal(t, http.StatusTemporaryRedirect, code)
	assert.True(t, strings.HasPrefix(hdr.Get("Location"), "/auth?locked=1&return_to="), hdr.Get("Location"))
	// /auth on this computer is the locked page (no key form, no script).
	code, body, _ = get(plain, "/auth?locked=1&return_to=%2F", nil)
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, string(body), `name="coral-locked"`)
	assert.NotContains(t, string(body), "api-key")
	code, _, _ = get(plain, "/api/health", nil)
	assert.Equal(t, http.StatusOK, code, "the liveness check stays open")
	// WebSocket upgrades are locked too.
	ws := http.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}, "Sec-Websocket-Version": {"13"}, "Sec-Websocket-Key": {"dGhlIHNhbXBsZSBub25jZQ=="}}
	for _, p := range []string{"/ws/coral", "/ws/terminal/x"} {
		code, body, _ = get(plain, p, ws)
		assert.Equal(t, http.StatusUnauthorized, code, p)
		assert.Equal(t, "dashboard_auth_required", codeOfBody(body), p)
	}

	// coral open: the signed request mints a link (the command-line token from the data folder).
	tok, err := cliauth.ReadToken(dir)
	require.NoError(t, err)
	resp, err := cliauth.CLIClient{Base: ts.URL, Token: tok}.Do(context.Background(), http.MethodPost, "/api/system/login-token", []byte("{}"), nil, 0)
	require.NoError(t, err)
	var link struct {
		Path string `json:"path"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&link))
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotEmpty(t, link.Path)

	// The browser opens it, gets its cookie, and the dashboard works.
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar}
	code, _, _ = get(browser, link.Path, nil)
	assert.Equal(t, http.StatusOK, code)
	code, _, _ = get(browser, "/api/sessions/live", nil)
	assert.Equal(t, http.StatusOK, code, "signed in")
	// (An accepted upgrade is never read to the end: the socket stays open.)
	wsReq, _ := http.NewRequest("GET", ts.URL+"/ws/coral", nil)
	wsReq.Header = ws.Clone()
	wsResp, err := browser.Do(wsReq)
	require.NoError(t, err)
	wsResp.Body.Close()
	assert.Equal(t, http.StatusSwitchingProtocols, wsResp.StatusCode, "the dashboard's feed opens with the sign-in")
	code, body, _ = get(browser, "/api/system/identity-report", http.Header{"Origin": {ts.URL}, "Sec-Fetch-Site": {"same-origin"}})
	require.Equal(t, http.StatusOK, code, string(body))
	var rep map[string]any
	require.NoError(t, json.Unmarshal(body, &rep))
	assert.Equal(t, "strict", rep["mode"])

	// A person's command-line tool: every request signed, the local Operator.
	cli := &http.Client{Transport: cliauth.Transport{Base: http.DefaultTransport, Token: tok}}
	code, _, _ = get(cli, "/api/sessions/live", nil)
	assert.Equal(t, http.StatusOK, code)
	post := func(c *http.Client, path, body string, hdr http.Header) (int, []byte) {
		req, _ := http.NewRequest("POST", ts.URL+path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header[k] = v
		}
		resp, err := c.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	code, body = post(cli, "/api/board/team/messages", `{"subscriber_id":"Operator","content":"from my terminal"}`, nil)
	assert.Equal(t, http.StatusOK, code, string(body))

	// An agent: its own identity yes, anyone else's no. A tool without its token: 401.
	ctx := context.Background()
	_, err = srv.boardStore.Subscribe(ctx, "team", "Dev", "Backend Dev", "claude-dev", nil, "")
	require.NoError(t, err)
	agentTok, err := srv.db.MintSessionToken(ctx, "sid-dev", "claude-dev")
	require.NoError(t, err)
	agent := http.Header{store.SessionTokenHeader: {agentTok}, "User-Agent": {"coral-board/dev"}}
	code, body = post(plain, "/api/board/team/messages", `{"subscriber_id":"Dev","content":"mine"}`, agent)
	assert.Equal(t, http.StatusOK, code, string(body))
	code, body = post(plain, "/api/board/team/messages", `{"subscriber_id":"Operator","content":"as the Operator"}`, agent)
	assert.Equal(t, http.StatusForbidden, code)
	assert.Equal(t, "identity_mismatch", codeOfBody(body))
	code, body = post(plain, "/api/board/team/messages", `{"subscriber_id":"Dev","content":"x"}`, http.Header{"User-Agent": {"coral-board/dev"}})
	assert.Equal(t, http.StatusUnauthorized, code)
	assert.Equal(t, "session_token_required", codeOfBody(body))

	// The report names what was refused by route (matched although refused before routing),
	// and counts the signed command line as its own caller, never as would-break.
	rep2 := srv.identity.Report()
	byRoute := rep2["would_break_by_route"].(map[string]int64)
	assert.EqualValues(t, 1, byRoute["agent_no_token coral-board POST /api/board/{project}/messages"], byRoute)
	assert.Positive(t, byRoute["local_other Go-http-client GET /api/sessions/live"], byRoute)
	callers := rep2["callers"].(map[string]int64)
	assert.Positive(t, callers["cli_signed"])
	assert.Positive(t, callers["open"], "the hello and the health check")
	_, listed := rep2["would_break_in_strict_mode"].(map[string]int64)["cli_signed"]
	assert.False(t, listed)
	refused := rep2["refused"].(map[string]int64)
	assert.EqualValues(t, 1, refused["identity_mismatch"])
	assert.EqualValues(t, 1, refused["session_token_required"])
	assert.Positive(t, refused["dashboard_auth_required"])
}

// Off (the default), nothing changes: phase 1 only reports.
func TestStrictLocalAuthOffByDefault(t *testing.T) {
	srv, ts, dir := strictServer(t, false)
	resp, err := http.Get(ts.URL + "/api/sessions/live")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// #3421 v2: still no credential needed by default (counted as an unsigned caller), but a
	// credential that is PRESENT must be valid: a wrong one is refused, never ignored. A
	// dashboard holding an old cookie is not a wrong credential.
	status := func(c *http.Client, hdr http.Header) (int, string) {
		req, _ := http.NewRequest("GET", ts.URL+"/api/sessions/live", nil)
		for k, v := range hdr {
			req.Header[k] = v
		}
		resp, err := c.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, codeOfBody(b)
	}
	browser := http.Header{"Origin": {ts.URL}, "Sec-Fetch-Site": {"same-origin"}}
	for name, hdr := range map[string]http.Header{
		"bare":                    nil,
		"dashboard, no cookie":    browser,
		"dashboard, stale cookie": {"Origin": {ts.URL}, "Sec-Fetch-Site": {"same-origin"}, "Cookie": {"coral_session=expired-session"}},
		"agent tool, no token":    {"User-Agent": {"coral-board/dev"}},
		"valid API key":           {"Authorization": {"Bearer " + srv.keyStore.Key()}},
	} {
		code, c := status(http.DefaultClient, hdr)
		assert.Equal(t, http.StatusOK, code, "%s: %s", name, c)
	}
	for name, tc := range map[string]struct {
		hdr  http.Header
		code string
	}{
		"wrong API key":       {http.Header{"Authorization": {"Bearer not-the-key"}}, "invalid_credential"},
		"wrong ?api_key=":     {nil, "invalid_credential"},
		"forged tunnel stamp": {http.Header{"X-Coral-Via-Tunnel": {"1"}}, "invalid_credential"},
		"bad signature":       {http.Header{cliauth.CLIAuthHeader: {"made.up"}}, "cli_signature_required"},
	} {
		c := http.DefaultClient
		var code int
		var got string
		if name == "wrong ?api_key=" {
			resp, err := c.Get(ts.URL + "/api/sessions/live?api_key=not-the-key")
			require.NoError(t, err)
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			code, got = resp.StatusCode, codeOfBody(b)
		} else {
			code, got = status(c, tc.hdr)
		}
		assert.Equal(t, http.StatusUnauthorized, code, name)
		assert.Equal(t, tc.code, got, name)
	}
	tok, err := cliauth.ReadToken(dir)
	require.NoError(t, err)
	code, _ := status(&http.Client{Transport: cliauth.Transport{Base: http.DefaultTransport, Token: tok, Dir: dir}}, nil)
	assert.Equal(t, http.StatusOK, code, "a signed command-line request")
	callers := srv.identity.Report()["callers"].(map[string]int64)
	assert.Positive(t, callers["local_other"], "the unsigned caller is reported")
	resp, err = http.Get(ts.URL + "/auth")
	require.NoError(t, err)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.NotContains(t, string(b), `name="coral-locked"`, "the API-key page as before")
}
