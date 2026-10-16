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
	a.setSessionCookie(rec, plain, sa)
	c := rec.Result().Cookies()[0]
	assert.Equal(t, a.SessionCookieName(plain), c.Name)
	assert.False(t, c.Secure)

	secure := httptest.NewRequest("GET", "https://coral.example/", nil)
	secure.TLS = &tls.ConnectionState{}
	rec = httptest.NewRecorder()
	a.setSessionCookie(rec, secure, sa)
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
