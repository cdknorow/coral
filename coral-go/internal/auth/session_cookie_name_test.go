package auth

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3682 (browser storage audit, finding 1): each server has its own session cookie, so two
// Corals on one host name don't sign each other out; over https it is a __Host- cookie (Secure,
// Path /, no Domain); the pre-#3682 shared name is still read, and a cookie that is a valid
// session here wins over a stale one.
func TestSessionCookieIsPerServer(t *testing.T) {
	a, err := NewKeyStore(t.TempDir())
	require.NoError(t, err)
	b, err := NewKeyStore(t.TempDir())
	require.NoError(t, err)
	plain := httptest.NewRequest("GET", "http://127.0.0.1:8420/", nil)
	assert.NotEqual(t, a.SessionCookieName(plain), b.SessionCookieName(plain), "two servers, two names")
	assert.True(t, strings.HasPrefix(a.SessionCookieName(plain), "coral_session_"))

	sa, err := a.CreateSession("127.0.0.1", "test")
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	a.setSessionCookie(rec, plain, sa, false)
	c := rec.Result().Cookies()[0]
	assert.Equal(t, a.SessionCookieName(plain), c.Name)
	assert.False(t, c.Secure)

	secure := httptest.NewRequest("GET", "https://coral.example/", nil)
	secure.TLS = &tls.ConnectionState{}
	rec = httptest.NewRecorder()
	a.setSessionCookie(rec, secure, sa, false)
	c = rec.Result().Cookies()[0]
	assert.Equal(t, "__Host-"+a.SessionCookieName(plain), c.Name)
	assert.True(t, c.Secure)
	assert.Equal(t, "/", c.Path)
	assert.Empty(t, c.Domain)

	// A request carrying both servers' cookies and a stale shared one: each server reads its own.
	sb, err := b.CreateSession("127.0.0.1", "test")
	require.NoError(t, err)
	both := httptest.NewRequest("GET", "http://127.0.0.1:8420/", nil)
	both.AddCookie(&http.Cookie{Name: legacyCookieName, Value: "stale-from-another-coral"})
	both.AddCookie(&http.Cookie{Name: a.SessionCookieName(plain), Value: sa})
	both.AddCookie(&http.Cookie{Name: b.SessionCookieName(plain), Value: sb})
	assert.Equal(t, sa, a.extractSessionCookie(both))
	assert.Equal(t, sb, b.extractSessionCookie(both))
	assert.True(t, a.HasValidSession(both) && b.HasValidSession(both))

	// Only B's cookie: A sees no session (it never reads B's name).
	onlyB := httptest.NewRequest("GET", "http://127.0.0.1:8420/", nil)
	onlyB.AddCookie(&http.Cookie{Name: b.SessionCookieName(plain), Value: sb})
	assert.False(t, a.HasValidSession(onlyB))

	// A sign-in from before #3682 (the shared name) still works where it is valid.
	legacy := httptest.NewRequest("GET", "http://127.0.0.1:8420/", nil)
	legacy.AddCookie(&http.Cookie{Name: legacyCookieName, Value: sa})
	assert.True(t, a.HasValidSession(legacy))
	assert.False(t, b.HasValidSession(legacy), "a session is only valid on the server that made it")
}

// Sign-out (Security's REQUIRED on #3682): every session of this server among the cookie names
// ends; the old shared cookie is cleared only when its token was a session here, and left alone
// when it belongs to another Coral.
func TestSignOutEndsEveryNameAndLeavesAnotherCoralsCookie(t *testing.T) {
	a, err := NewKeyStore(t.TempDir())
	require.NoError(t, err)
	b, err := NewKeyStore(t.TempDir())
	require.NoError(t, err)
	plain := httptest.NewRequest("GET", "http://127.0.0.1:8420/", nil)
	s1, _ := a.CreateSession("127.0.0.1", "t")
	s2, _ := a.CreateSession("127.0.0.1", "t")
	s3, _ := a.CreateSession("127.0.0.1", "t")
	req := httptest.NewRequest("POST", "http://127.0.0.1:8420/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "__Host-" + a.SessionCookieName(plain), Value: s1})
	req.AddCookie(&http.Cookie{Name: a.SessionCookieName(plain), Value: s2})
	req.AddCookie(&http.Cookie{Name: legacyCookieName, Value: s3})
	rec := httptest.NewRecorder()
	a.signOut(rec, req)
	for _, s := range []string{s1, s2, s3} {
		assert.False(t, a.ValidateSession(s), "every name's session ended")
	}
	cleared := map[string]bool{}
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 {
			cleared[c.Name] = true
		}
	}
	assert.True(t, cleared[a.SessionCookieName(plain)], "this server's cookie cleared")
	assert.True(t, cleared[legacyCookieName], "the old shared cookie was this server's session: cleared")

	// The old shared cookie belongs to another Coral (its session there): left alone, and that
	// session still works there.
	other, _ := b.CreateSession("127.0.0.1", "t")
	mine, _ := a.CreateSession("127.0.0.1", "t")
	req = httptest.NewRequest("POST", "http://127.0.0.1:8420/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: a.SessionCookieName(plain), Value: mine})
	req.AddCookie(&http.Cookie{Name: legacyCookieName, Value: other})
	rec = httptest.NewRecorder()
	a.signOut(rec, req)
	assert.False(t, a.ValidateSession(mine))
	assert.True(t, b.ValidateSession(other), "another Coral's session is untouched")
	for _, c := range rec.Result().Cookies() {
		assert.NotEqual(t, legacyCookieName, c.Name, "another Coral's cookie is not cleared")
	}
}
