package auth

import (
	"log"
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

	"github.com/subgentic/coral/internal/config"
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
	assert.True(t, isSessionCookieName(c[0].Name), c[0].Name)
	assert.True(t, c[0].HttpOnly)
	assert.Equal(t, http.SameSiteStrictMode, c[0].SameSite)
	assert.True(t, ks.ValidateSession(c[0].Value))

	assert.Equal(t, http.StatusForbidden, get("/?login_token="+token, "127.0.0.1:1").Code, "single use")

	// Never another host: a path starting "//" (or "/\") comes back with one "/" (Security, #3532).
	// A backslash is escaped (%5C), which no browser reads as a slash.
	for page, want := range map[string]string{"//evil.example/x": "/evil.example/x", "/\\evil.example/x": "/%5Cevil.example/x", "///a": "/a"} {
		tok, err := ks.MintLoginToken()
		require.NoError(t, err)
		r := httptest.NewRequest("GET", "/?login_token="+tok, nil)
		r.URL.Path, r.URL.RawPath = page, ""
		r.RemoteAddr, r.Host = "203.0.113.5:1234", "localhost:8420"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		require.Equal(t, http.StatusFound, rec.Code, page)
		assert.Equal(t, want, rec.Header().Get("Location"), page)
	}

	// A dashboard page (#3532): the path comes back exactly as sent, escapes and all.
	for _, page := range []string{"/team/@m2/qa/settings", "/board/%40odd", "/team/a%2Fb", "/chats?q=x"} {
		tok, err := ks.MintLoginToken()
		require.NoError(t, err)
		sep := "?"
		if strings.Contains(page, "?") {
			sep = "&"
		}
		rec := get(page+sep+"login_token="+tok, "203.0.113.5:1234")
		require.Equal(t, http.StatusFound, rec.Code, page)
		assert.Equal(t, page, rec.Header().Get("Location"), page)
	}
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

// #3970 (Security's #3967 finding 1): a sign-in link opened by another OS user of this computer
// is refused like a used one, and the token survives for its owner; where the system can't tell
// the user, links work as before and that is logged once.
func TestLoginTokenRefusesAnotherUser(t *testing.T) {
	var logs strings.Builder
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	ks, err := NewKeyStore(t.TempDir())
	require.NoError(t, err)
	h := Middleware(ks)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) }))
	get := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/?login_token="+token, nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Host = "localhost:8420"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	same, supported := false, true
	ks.SetPeerUser(func(*http.Request) (bool, bool) { return same, supported })
	tok, err := ks.MintLoginToken()
	require.NoError(t, err)
	rec := get(tok)
	assert.Equal(t, http.StatusForbidden, rec.Code, "another user")
	assert.Empty(t, rec.Result().Cookies())
	assert.Contains(t, logs.String(), "opened by another user")
	same = true
	rec = get(tok)
	assert.Equal(t, http.StatusFound, rec.Code, "the owner's link still works")

	supported, same = false, false
	for i := 0; i < 2; i++ {
		tok, _ := ks.MintLoginToken()
		assert.Equal(t, http.StatusFound, get(tok).Code, "unsupported: as before")
	}
	assert.Equal(t, 1, strings.Count(logs.String(), "can't tell which user"), "logged once")
}

// #6100: the Coral app opens its sign-in link with coral_client=app, and the session that link makes is named "Coral app on ..." on the Sessions
// page, so a person can tell it from their own browser and sign it out. The name is the only difference: another value, or another way of
// signing in, gets the ordinary browser label, and the parameter never stays in the address.
func TestTheCoralAppsSignInLinkNamesItsSession(t *testing.T) {
	dir := t.TempDir()
	ks, err := NewKeyStore(dir)
	require.NoError(t, err)
	h := Middleware(ks)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) }))
	const edge = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/155.0.0.0 Safari/537.36 Edg/155.0.0.0"
	signIn := func(query string) (location string) {
		token, err := ks.WriteLoginTokenFile(dir)
		require.NoError(t, err)
		r := httptest.NewRequest("GET", "/?"+query+"login_token="+token, nil)
		r.RemoteAddr = "203.0.113.5:1234"
		r.Host = "localhost:8420"
		r.Header.Set("User-Agent", edge)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		require.Equal(t, http.StatusFound, rec.Code)
		return rec.Header().Get("Location")
	}
	labels := func() []string {
		list, err := ks.backend().ListSessions(t.Context())
		require.NoError(t, err)
		var out []string
		for _, s := range list {
			out = append(out, s.Label)
		}
		return out
	}

	assert.Equal(t, "/", signIn("coral_client=app&"), "the mark is not left in the address")
	assert.Equal(t, []string{"Coral app on Windows"}, labels(), "the app's link names its session")

	assert.Equal(t, "/", signIn(""))
	assert.ElementsMatch(t, []string{"Coral app on Windows", "Edge on Windows"}, labels(), "a link without the mark is the person's own browser")

	assert.Equal(t, "/", signIn("coral_client=other&"))
	assert.ElementsMatch(t, []string{"Coral app on Windows", "Edge on Windows", "Edge on Windows"}, labels(), "another value changes nothing")

	// Only a sign-in link carries the name: a session from the API-key form is a browser's, whatever the address says.
	r := httptest.NewRequest("POST", "/?coral_client=app", nil)
	r.Header.Set("User-Agent", edge)
	assert.Equal(t, "Edge on Windows", newSessionLabel(r, SessionByKey))
	assert.Equal(t, "Coral app on Windows", newSessionLabel(r, SessionByLink))
	assert.Equal(t, "Coral app", appSessionLabel("Mozilla/5.0 (X11; U; unknown)"), "an unknown system: just the app's name")
}
