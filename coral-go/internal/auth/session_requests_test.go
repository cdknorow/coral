package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3966 (c): ending a browser session of any kind closes its open requests (its WebSockets),
// and only its own.
func TestEndingASessionClosesItsOpenRequests(t *testing.T) {
	g := newPwRig(t)
	browser := func(method string) *http.Cookie {
		r := g.req("GET", "/", "", "10.0.0.5", nil)
		tok, err := g.ks.NewSession(r, method, false)
		require.NoError(t, err)
		return &http.Cookie{Name: g.ks.cookieName(r), Value: tok}
	}
	// open starts a request that waits on its context, like a terminal socket's read loop.
	open := func(c *http.Cookie) <-chan struct{} {
		entered, done := make(chan struct{}), make(chan struct{})
		h := g.ks.SessionRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			<-r.Context().Done()
			close(done)
		}))
		// Its own context, ended when the test ends, as a real connection's ends when it closes: a
		// socket that rightly stays open must not outlive the test (#4167, goleak).
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go h.ServeHTTP(httptest.NewRecorder(), g.req("GET", "/ws/terminal/a", "", "10.0.0.5", c).WithContext(ctx))
		<-entered
		return done
	}
	closed := func(ch <-chan struct{}) bool {
		select {
		case <-ch:
			return true
		case <-time.After(2 * time.Second):
			return false
		}
	}
	stillOpen := func(ch <-chan struct{}) bool {
		select {
		case <-ch:
			return false
		case <-time.After(100 * time.Millisecond):
			return true
		}
	}

	// Sign out other browsers: theirs close, this one's stays.
	me, link, key := browser(SessionByPassword), browser(SessionByLink), browser(SessionByKey)
	mine, theirs1, theirs2 := open(me), open(link), open(key)
	w, _ := g.do(g.ar.SignOutOthers, g.req("POST", "/api/auth/sessions/sign-out-others", "", "10.0.0.5", me))
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.True(t, closed(theirs1), "the link session's socket closed")
	assert.True(t, closed(theirs2), "the key-form session's socket closed")
	assert.True(t, stillOpen(mine), "this browser's own socket stays")

	// Every session ends (a password change or reset): every socket closes.
	other := browser(SessionByPassword)
	theirs := open(other)
	n, err := g.ks.RevokeAllSessions()
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.True(t, closed(mine))
	assert.True(t, closed(theirs))

	// One session signed out (logout, Settings): only its socket closes.
	a, b := browser(SessionByPassword), browser(SessionByPassword)
	sa, sb := open(a), open(b)
	g.ks.RevokeSession(a.Value)
	assert.True(t, closed(sa))
	assert.True(t, stillOpen(sb))

	// A request without a live session is untouched.
	plain := make(chan struct{})
	g.ks.SessionRequests(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { close(plain) })).
		ServeHTTP(httptest.NewRecorder(), g.req("GET", "/", "", "10.0.0.5", &http.Cookie{Name: g.ks.cookieName(g.req("GET", "/", "", "10.0.0.5", nil)), Value: "nope"}))
	assert.True(t, closed(plain))
}

// Security's fix-forward: rotating the API key ends every session and closes their open
// requests too.
func TestRegenerateKeyClosesOpenRequests(t *testing.T) {
	g := newPwRig(t)
	r := g.req("GET", "/", "", "10.0.0.5", nil)
	tok, err := g.ks.NewSession(r, SessionByPassword, false)
	require.NoError(t, err)
	entered, done := make(chan struct{}), make(chan struct{})
	h := g.ks.SessionRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(done)
	}))
	go h.ServeHTTP(httptest.NewRecorder(), g.req("GET", "/ws/coral", "", "10.0.0.5", &http.Cookie{Name: g.ks.cookieName(r), Value: tok}))
	<-entered
	_, err = g.ks.RegenerateKey()
	require.NoError(t, err)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the open request survived the key rotation")
	}
	assert.False(t, g.ks.ValidateSession(tok))
}

// Security's fix-forward 2: a session that reaches its own expiry closes its open requests too.
func TestAnExpiringSessionClosesItsOpenRequests(t *testing.T) {
	g := newPwRig(t)
	r := g.req("GET", "/", "", "10.0.0.5", nil)
	tok, err := g.ks.NewSession(r, SessionByPassword, false)
	require.NoError(t, err)
	rec, ok, err := g.ks.backend().GetSession(context.Background(), sessionHash(tok))
	require.True(t, ok)
	require.NoError(t, err)
	rec.ExpiresAt = time.Now().Add(300 * time.Millisecond)
	require.NoError(t, g.ks.backend().PutSession(context.Background(), rec))
	entered, done := make(chan struct{}), make(chan struct{})
	h := g.ks.SessionRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(done)
	}))
	go h.ServeHTTP(httptest.NewRecorder(), g.req("GET", "/ws/coral", "", "10.0.0.5", &http.Cookie{Name: g.ks.cookieName(r), Value: tok}))
	<-entered
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the open request outlived its session")
	}
}
