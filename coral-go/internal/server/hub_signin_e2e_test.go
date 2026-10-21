package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/auth"
	"github.com/subgentic/coral/internal/server/routes"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// #3966 through the real router, strict local auth on: the sign-in paths answer signed out, a
// Hub session is limited by the guard right after the sign-in middleware, and an ended one is
// signed out.
func TestHubSigninThroughTheRouter(t *testing.T) {
	dir := t.TempDir()
	cfg := newE2EConfig(t, dir)
	cfg.StrictLocalAuth = true
	db, err := storetest.Open(t, cfg.DBPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	stack := newTermStack(t)
	srv := mustServer(NewWithKey(cfg, db, stack.backend, stack.terminal, ""))
	t.Cleanup(srv.Close) // its loops and the board database, which Windows won't remove while open (#3914)
	require.NotNil(t, srv.hubSignin)
	local := httptest.NewServer(srv.Router())
	t.Cleanup(local.Close)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, path string, cookie *http.Cookie) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, local.URL+path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", local.URL)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := noFollow.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp
	}

	// Signed out, Hub not configured: each sign-in path answers itself (no sign-in redirect).
	r := do("POST", "/auth/hub/start", nil)
	assert.Equal(t, http.StatusSeeOther, r.StatusCode)
	assert.Equal(t, "/auth?hub=unavailable", r.Header.Get("Location"))
	r = do("GET", "/auth/hub/callback?code=x&state=y", nil)
	assert.Equal(t, "/auth?hub=failed", r.Header.Get("Location"))
	assert.Equal(t, http.StatusNotFound, do("GET", "/auth/hub/link/status", nil).StatusCode)
	assert.Equal(t, http.StatusServiceUnavailable, do("POST", "/auth/hub/link", nil).StatusCode, "no Coral Hub configured: can't link")
	assert.Equal(t, http.StatusUnauthorized, do("GET", "/api/sessions/live", nil).StatusCode)

	// Linked, with Sign in with Coral Hub on: a Hub session (as the callback makes one).
	srv.cloudHandler.SetLinkedCheck(func() bool { return true })
	require.NoError(t, store.NewSessionStore(db).SetSetting(context.Background(), HubSigninEnabledSetting, "true"))
	req := httptest.NewRequest("GET", "/", nil)
	token, err := srv.keyStore.NewHubSession(req, auth.HubGrant{Grant: "g", Account: "a", Display: "Ann", TTL: time.Hour})
	require.NoError(t, err)
	hub := &http.Cookie{Name: srv.keyStore.SessionCookieName(httptest.NewRequest("GET", local.URL+"/", nil)), Value: token}

	assert.Equal(t, http.StatusOK, do("GET", "/api/sessions/live", hub).StatusCode, "a full Operator session")
	for _, p := range [][2]string{
		{"GET", "/api/auth/sessions"}, {"POST", "/api/auth/password/change"}, {"PUT", "/api/auth/hub-signin"},
		{"PUT", "/api/auth/hub-origins"}, {"POST", "/api/system/login-token"}, {"GET", "/api/system/api-key"},
		{"POST", "/api/cloud/disconnect"}, {"PUT", "/api/cloud/max-mode"}, {"PUT", "/api/system/hub-operator"},
	} {
		assert.Equal(t, http.StatusForbidden, do(p[0], p[1], hub).StatusCode, "%s %s", p[0], p[1])
	}

	// Nor through the generic settings PUT (round 1's must-fix): refused, nothing stored.
	for _, k := range []string{routes.MaxModeSetting, routes.AllowHubAutoAcceptSetting, routes.RedactTokensSetting} {
		req, _ := http.NewRequest("PUT", local.URL+"/api/settings", strings.NewReader(`{"`+k+`":"launch"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", local.URL)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.AddCookie(hub)
		resp, err := noFollow.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, k)
		st, _ := store.NewSessionStore(db).GetSettings(context.Background())
		assert.Empty(t, st[k], k)
	}

	// Turned off on this computer (here behind its route's back): signed out within a second.
	require.NoError(t, store.NewSessionStore(db).SetSetting(context.Background(), HubSigninEnabledSetting, "false"))
	require.Eventually(t, func() bool { return do("GET", "/api/sessions/live", hub).StatusCode == http.StatusUnauthorized },
		3*time.Second, 100*time.Millisecond)
	require.NoError(t, store.NewSessionStore(db).SetSetting(context.Background(), HubSigninEnabledSetting, "true"))
	assert.Equal(t, http.StatusUnauthorized, do("GET", "/api/sessions/live", hub).StatusCode, "it stays ended")
}
