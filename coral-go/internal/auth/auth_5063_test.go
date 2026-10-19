package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An IPv6 client is one client per /64 for the password throttle, as for the failed-key limit (#4964): a network is handed 2^64 addresses, and a budget
// of five free tries per address would let it guess without limit (#5063).
func TestPasswordThrottleCountsAnIPv6NetworkOnce(t *testing.T) {
	g := newPwRig(t)
	g.setPassword("right")
	try := func(addr, pw string) (*httptest.ResponseRecorder, map[string]any) {
		r := g.req("POST", "/auth/password", `{"password":"`+pw+`"}`, "10.0.0.5", nil)
		r.RemoteAddr = "[" + addr + "]:5000"
		return g.do(g.ar.SignInWithPassword, r)
	}
	for i := 0; i < freeFails; i++ { // five wrong passwords, each from another address of 2001:db8:1:2::/64
		try("2001:db8:1:2::"+string(rune('a'+i)), "wrong")
	}
	w, out := try("2001:db8:1:2:ffff::1", "right") // a sixth address of the same /64: it must wait
	assert.Equal(t, http.StatusTooManyRequests, w.Code, "the whole /64 is one client")
	assert.Equal(t, "too_many_attempts", out["code"])
	w, _ = try("2001:db8:1:3::1", "right") // another /64 is another client
	assert.Equal(t, http.StatusOK, w.Code)
}

// A link or the key form replaces the cookie the browser had, and the old session ends with it, as a password sign-in does (#5063).
func TestLinkAndKeySignInsEndTheSessionTheyReplace(t *testing.T) {
	g := newPwRig(t)
	old, err := g.ks.NewSession(g.req("GET", "/", "", "10.0.0.5", nil), SessionByKey, false)
	require.NoError(t, err)
	oldCookie := &http.Cookie{Name: g.ks.cookieBase, Value: old}
	require.True(t, g.ks.ValidateSession(old))

	tok, err := g.ks.MintLoginToken()
	require.NoError(t, err)
	h := Middleware(g.ks)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	r := g.req("GET", "/?login_token="+tok, "", "10.0.0.5", oldCookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, http.StatusFound, w.Code)
	fresh := sessionCookie(t, w)
	assert.True(t, g.ks.ValidateSession(fresh.Value))
	assert.False(t, g.ks.ValidateSession(old), "the sign-in link ended the session it replaced")

	r = g.req("POST", "/auth/key", `{"key":"`+g.ks.Key()+`"}`, "10.0.0.5", fresh)
	w2, _ := g.do(g.ar.ValidateKey, r)
	require.Equal(t, http.StatusOK, w2.Code)
	assert.False(t, g.ks.ValidateSession(fresh.Value), "the key form ended the session it replaced")
	assert.True(t, g.ks.ValidateSession(sessionCookie(t, w2).Value))
}

// A password sign-in that matched the old password must not leave a session once the password has been changed: the change ends every session, and a
// sign-in in flight would otherwise create its own after that (#5063).
func TestSignInInFlightDuringAPasswordChangeLeavesNoSession(t *testing.T) {
	g := newPwRig(t)
	g.setPassword("old-password")
	tok, err := g.ks.NewSession(g.req("GET", "/", "", "10.0.0.5", nil), SessionByLink, false)
	require.NoError(t, err)
	me := &http.Cookie{Name: g.ks.cookieBase, Value: tok}

	// Hold the sign-in between its hash check and its session (the session list is read at that moment), change the password there, then let it go.
	gate := make(chan struct{})
	resume := make(chan struct{})
	var first atomic.Bool
	g.ks.SetSessionBackend(&pausingBackend{SessionBackend: g.ks.backend(), onPut: func() {
		if first.CompareAndSwap(false, true) { // only the sign-in's own session is held; the change's is not
			close(gate)
			<-resume
		}
	}})
	var w *httptest.ResponseRecorder
	var out map[string]any
	done := make(chan struct{})
	go func() {
		defer close(done)
		w, out = g.do(g.ar.SignInWithPassword, g.req("POST", "/auth/password", `{"password":"old-password"}`, "10.0.0.6", nil))
	}()
	<-gate
	cw, _ := g.do(g.ar.ChangePassword, g.req("POST", "/api/auth/password/change", `{"current":"old-password","password":"new-password"}`, "10.0.0.5", me))
	require.Equal(t, http.StatusOK, cw.Code, cw.Body.String())
	close(resume)
	<-done

	assert.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	assert.Equal(t, "password_changed", out["code"])
	for _, c := range w.Result().Cookies() {
		assert.False(t, isSessionCookieName(c.Name) && c.Value != "", "no session cookie after a changed password")
	}
	list, err := g.ks.backend().ListSessions(t.Context())
	require.NoError(t, err)
	assert.Len(t, list, 1, "only the browser that changed the password has a session")
}

// pausingBackend runs onPut when a session is being stored (a test seam for the order of two requests).
type pausingBackend struct {
	SessionBackend
	onPut func()
}

func (p *pausingBackend) PutSession(ctx context.Context, s SessionRecord) error {
	p.onPut()
	return p.SessionBackend.PutSession(ctx, s)
}

// A script polling with ?api_key is authenticated like the header form and makes no session; a browser navigation gets one once, and reuses it (#5067).
func TestQueryKeyMintsASessionOnlyForANavigationThatHoldsNone(t *testing.T) {
	ks := newTestKeyStore(t)
	h := Middleware(ks)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	count := func() int {
		list, err := ks.backend().ListSessions(t.Context())
		require.NoError(t, err)
		return len(list)
	}
	get := func(setup func(*http.Request)) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/sessions/live?api_key="+ks.Key(), nil)
		r.RemoteAddr = "192.168.1.5:54321"
		setup(r)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		require.Equal(t, http.StatusOK, w.Code)
		return w
	}
	for i := 0; i < 100; i++ { // a script: curl's headers, then a fetch's
		get(func(r *http.Request) { r.Header.Set("Accept", "*/*") })
		get(func(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "cors") })
	}
	assert.Equal(t, 0, count(), "100 query-key requests from a script make no session")

	w := get(func(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "navigate") })
	cookie := sessionCookie(t, w)
	assert.Equal(t, 1, count(), "a navigation gets one")
	for i := 0; i < 100; i++ { // the browser follows the link again, and its page polls with the key still in the URL
		get(func(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "navigate"); r.AddCookie(cookie) })
	}
	assert.Equal(t, 1, count(), "a navigation that already holds a session reuses it")
	get(func(r *http.Request) { r.Header.Set("Accept", "text/html,application/xhtml+xml") }) // a browser that sends no Sec-Fetch headers, no cookie yet
	assert.Equal(t, 2, count())
	get(func(r *http.Request) {
		r.Header.Set("Sec-Fetch-Mode", "navigate")
		r.Header.Set("Sec-Fetch-Dest", "iframe")
	})
	assert.Equal(t, 2, count(), "a framed navigation gets none")
}
