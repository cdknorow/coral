package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/auth"
	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/secrets"
	"github.com/cdknorow/coral/internal/server/routes"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
	"github.com/cdknorow/coral/pkg/hubproxy"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// #3949 end to end, strict local auth on (the default): the real router, a real tunnel.
func TestPasswordEndToEnd(t *testing.T) {
	const secret = "e2e-canary-password-3949"
	dir := t.TempDir()
	cfg := newE2EConfig(t, dir)
	cfg.StrictLocalAuth = true
	db, err := storetest.Open(t, cfg.DBPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	stack := newTermStack(t)
	srv := NewWithKey(cfg, db, stack.backend, stack.terminal, "")
	srv.cloudHandler.SetLinkedCheck(func() bool { return true })
	local := httptest.NewServer(srv.Router())
	t.Cleanup(local.Close)

	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	post := func(c *http.Client, base, path, body string, hdr map[string]string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("POST", base+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", base)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := c.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp
	}
	get := func(c *http.Client, path string) int {
		resp, err := c.Get(local.URL + path)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}

	// Signed out: the dashboard's API is refused.
	assert.Equal(t, http.StatusUnauthorized, get(browser, "/api/sessions/live"))
	// Create the first password with the setup code (always accepted, local or not).
	code := auth.FormatSetupCode(srv.keyStore.SetupCode())
	require.NotEmpty(t, code)
	resp := post(browser, local.URL, "/auth/password/create", `{"password":"`+secret+`","keep":true,"code":"`+code+`"}`, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, http.StatusOK, get(browser, "/api/sessions/live"), "signed in: the cookie opens the API")

	// An agent's token can't touch the password or the sessions (403 forbidden_for_agent).
	token, err := db.MintSessionToken(context.Background(), "sid-agent", "claude-agent")
	require.NoError(t, err)
	agent := &http.Client{}
	for _, p := range []string{"/api/auth/password/set", "/api/auth/password/change", "/api/auth/password/reset", "/api/auth/sessions/sign-out-others"} {
		r := post(agent, local.URL, p, `{"password":"x"}`, map[string]string{store.SessionTokenHeader: token, "User-Agent": "coral-board/dev"})
		assert.Equal(t, http.StatusForbidden, r.StatusCode, p)
	}

	// Through Coral Hub: none of the password routes, whatever the hub user sends.
	ss := store.NewSessionStore(db)
	require.NoError(t, ss.SetSetting(context.Background(), routes.MaxModeSetting, tunnel.TierLaunch.String()))
	cur := routes.MaxModeFromSettings(ss)
	sessions := make(chan *tunnel.Session, 1)
	ths := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s, err := tunnel.AcceptHTTP(w, r, func(context.Context, string) (any, error) { return "x", nil }, tunnel.SessionConfig{}); err == nil {
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
	hr := chi.NewRouter()
	hr.Handle("/api/remote/{server}/*", hubproxy.NewWithOptions(oneTunnel{sess}, hubproxy.Options{AllowedOrigins: []string{hub.Listener.Addr().String()}}))
	hub.Config.Handler = hr
	hub.Start()
	t.Cleanup(hub.Close)
	u, _ := url.Parse(local.URL)
	var ourCookie string
	for _, c := range jar.Cookies(u) {
		ourCookie = c.Name + "=" + c.Value
	}
	for _, p := range []string{"/auth/password", "/auth/password/create", "/api/auth/password/set", "/api/auth/password/change",
		"/api/auth/password/reset", "/api/auth/sessions/sign-out-others"} {
		r := post(http.DefaultClient, hub.URL, "/api/remote/srv"+p, `{"password":"`+secret+`"}`, map[string]string{tunnel.HubUserHeader: "hub-user-1234abcd", "Cookie": ourCookie})
		assert.NotEqual(t, http.StatusOK, r.StatusCode, p)
		assert.Empty(t, r.Header.Values("Set-Cookie"), p)
	}
	req, _ := http.NewRequest("GET", hub.URL+"/api/remote/srv/api/auth/sessions", nil)
	req.Header.Set(tunnel.HubUserHeader, "hub-user-1234abcd")
	r, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	r.Body.Close()
	assert.NotEqual(t, http.StatusOK, r.StatusCode)

	// A restart keeps the browser signed in (the session is in sessions.db).
	srv2 := NewWithKey(cfg, db, stack.backend, stack.terminal, "")
	local2 := httptest.NewServer(srv2.Router())
	t.Cleanup(local2.Close)
	u2, _ := url.Parse(local2.URL)
	jar.SetCookies(u2, jar.Cookies(u))
	resp2, err := browser.Get(local2.URL + "/api/sessions/live")
	require.NoError(t, err)
	resp2.Body.Close()
	assert.Equal(t, http.StatusOK, resp2.StatusCode, "still signed in after a restart")

	// The password is in no file of the data folder (the hash only).
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			b, _ := os.ReadFile(p)
			assert.False(t, bytes.Contains(b, []byte(secret)), p)
		}
		return nil
	})
}
