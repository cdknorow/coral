package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/config"
)

// #3387: a sign-in link works once, within its time, sets the session cookie
// (HttpOnly, SameSite Strict) and redirects to the same page without the token.
func TestLoginToken(t *testing.T) {
	dir := t.TempDir()
	ks, err := NewKeyStore(dir)
	require.NoError(t, err)
	h := Middleware(ks)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) }))
	get := func(target, remote string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", target, nil)
		r.RemoteAddr = remote
		r.Host = "localhost:8420"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	token, err := ks.WriteLoginTokenFile(dir)
	require.NoError(t, err)
	b, err := os.ReadFile(filepath.Join(dir, LoginTokenFile))
	require.NoError(t, err)
	assert.Equal(t, token, strings.TrimSpace(string(b)))
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(dir, LoginTokenFile))
		assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	}

	rec := get("/?view=team&login_token="+token, "203.0.113.5:1234")
	require.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/?view=team", rec.Header().Get("Location"), "same page, token stripped")
	c := rec.Result().Cookies()
	require.Len(t, c, 1)
	assert.Equal(t, cookieName, c[0].Name)
	assert.True(t, c[0].HttpOnly)
	assert.Equal(t, http.SameSiteStrictMode, c[0].SameSite)
	assert.True(t, ks.ValidateSession(c[0].Value))

	assert.Equal(t, http.StatusForbidden, get("/?login_token="+token, "127.0.0.1:1").Code, "single use")
	assert.Equal(t, http.StatusForbidden, get("/?login_token=made-up", "127.0.0.1:1").Code)

	t2, err := ks.MintLoginToken()
	require.NoError(t, err)
	ks.mu.Lock()
	ks.loginTokens[hashLoginToken(t2)] = time.Now().Add(-time.Second)
	ks.mu.Unlock()
	assert.Equal(t, http.StatusForbidden, get("/?login_token="+t2, "127.0.0.1:1").Code, "expired")

	assert.Equal(t, 299, get("/", "127.0.0.1:1").Code, "phase 1: this computer still works without a cookie")
}

// The expiry knob exists for the stress suite: dev builds only, and it only shortens.
func TestLoginTokenTTLKnob(t *testing.T) {
	t.Setenv("CORAL_TEST_LOGIN_TOKEN_TTL", "3s")
	want := LoginTokenTTL
	if config.TierName == "dev" {
		want = 3 * time.Second
	}
	assert.Equal(t, want, loginTokenTTL())
	t.Setenv("CORAL_TEST_LOGIN_TOKEN_TTL", "48h")
	assert.Equal(t, LoginTokenTTL, loginTokenTTL(), "never longer")
	t.Setenv("CORAL_TEST_LOGIN_TOKEN_TTL", "junk")
	assert.Equal(t, LoginTokenTTL, loginTokenTTL())
}
