package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func postKey(ar *Routes, peer, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/auth/key", strings.NewReader(`{"key":"`+key+`"}`))
	r.RemoteAddr = peer
	w := httptest.NewRecorder()
	ar.ValidateKey(w, r)
	return w
}

// #3356: signing in with the right key never counts against the limit (as in Middleware);
// only wrong keys do, and once over the limit even the right key waits.
func TestValidateKeyRoute_OnlyFailuresAreRateLimited(t *testing.T) {
	ks := newTestKeyStore(t)
	ar := NewRoutes(ks)
	for i := 0; i < rateLimitMax*3; i++ {
		require.Equal(t, http.StatusOK, postKey(ar, "192.168.1.5:1000", ks.Key()).Code, "sign-in %d with the right key", i+1)
	}
	for i := 0; i < rateLimitMax; i++ {
		require.Equal(t, http.StatusUnauthorized, postKey(ar, "192.168.1.9:1000", "wrong").Code, "wrong key %d", i+1)
	}
	assert.Equal(t, http.StatusTooManyRequests, postKey(ar, "192.168.1.9:1000", "wrong").Code)
	assert.Equal(t, http.StatusTooManyRequests, postKey(ar, "192.168.1.9:1000", ks.Key()).Code, "over the limit, even the right key waits")
	assert.Equal(t, http.StatusOK, postKey(ar, "192.168.1.5:1000", ks.Key()).Code, "another address is unaffected")
}

// #3356: POST /auth/logout ends the caller's session and clears its cookie, through the
// middleware, even when the session has already expired; a cross-origin request is refused.
func TestLogout(t *testing.T) {
	ks := newTestKeyStore(t)
	ar := NewRoutes(ks)
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/logout", ar.Logout)
	mux.HandleFunc("/api/sessions", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := Middleware(ks)(mux)
	do := func(method, path, token, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://coral.lan:8420"+path, nil)
		r.RemoteAddr = "192.168.1.5:1000"
		if token != "" {
			r.AddCookie(&http.Cookie{Name: cookieName, Value: token})
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	mine, _ := ks.CreateSession("192.168.1.5", "test")
	other, _ := ks.CreateSession("192.168.1.6", "test")
	require.Equal(t, http.StatusOK, do("GET", "/api/sessions", mine, "").Code)

	assert.Equal(t, http.StatusForbidden, do("POST", "/auth/logout", mine, "https://evil.example").Code, "cross-origin")
	assert.True(t, ks.ValidateSession(mine), "a refused sign-out ends nothing")

	w := do("POST", "/auth/logout", mine, "http://coral.lan:8420")
	require.Equal(t, http.StatusOK, w.Code)
	var cleared *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName {
			cleared = c
		}
	}
	require.NotNil(t, cleared, "the cookie is cleared")
	assert.True(t, cleared.MaxAge < 0 && cleared.Value == "")
	assert.False(t, ks.ValidateSession(mine))
	assert.Equal(t, http.StatusUnauthorized, do("GET", "/api/sessions", mine, "").Code)
	assert.True(t, ks.ValidateSession(other), "other sessions stay")

	assert.Equal(t, http.StatusOK, do("POST", "/auth/logout", "stale-token", "").Code, "an ended session can still clear its cookie")
}

// #3356: the /api/license/webhook bypass is gone with the route (#3221).
func TestMiddleware_NoLicenseWebhookBypass(t *testing.T) {
	_, handler := newMiddlewareTest(t)
	r := httptest.NewRequest("POST", "/api/license/webhook", nil)
	r.RemoteAddr = "192.168.1.5:1000"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
