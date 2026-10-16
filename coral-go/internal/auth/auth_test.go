package auth

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestKeyStore(t *testing.T) *KeyStore {
	t.Helper()
	dir := t.TempDir()
	ks, err := NewKeyStore(dir)
	require.NoError(t, err)
	return ks
}

// ── KeyStore Tests ──────────────────────────────────────────

func TestKeyStore_GeneratesKeyOnFirstRun(t *testing.T) {
	ks := newTestKeyStore(t)
	key := ks.Key()
	assert.Len(t, key, keyLength)

	// Key should be persisted to file
	data, err := os.ReadFile(ks.keyPath)
	require.NoError(t, err)
	assert.Equal(t, key, strings.TrimSpace(string(data)))
}

func TestKeyStore_LoadsExistingKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "api_key")
	os.WriteFile(keyPath, []byte("abcdef1234567890abcdef1234567890\n"), 0600)

	ks, err := NewKeyStore(dir)
	require.NoError(t, err)
	assert.Equal(t, "abcdef1234567890abcdef1234567890", ks.Key())
}

func TestKeyStore_RegenerateKey(t *testing.T) {
	ks := newTestKeyStore(t)
	oldKey := ks.Key()
	newKey, err := ks.RegenerateKey()
	require.NoError(t, err)
	assert.NotEqual(t, oldKey, newKey)
	assert.Len(t, newKey, keyLength)
	assert.Equal(t, newKey, ks.Key())
}

func TestKeyStore_ValidateKey(t *testing.T) {
	ks := newTestKeyStore(t)
	assert.True(t, ks.ValidateKey(ks.Key()))
	assert.False(t, ks.ValidateKey("wrong-key"))
	assert.False(t, ks.ValidateKey(""))
}

func TestKeyStore_RegenerateInvalidatesOldKey(t *testing.T) {
	ks := newTestKeyStore(t)
	oldKey := ks.Key()
	_, err := ks.RegenerateKey()
	require.NoError(t, err)
	assert.False(t, ks.ValidateKey(oldKey))
}

// ── Session Tests ───────────────────────────────────────────

func TestKeyStore_CreateAndValidateSession(t *testing.T) {
	ks := newTestKeyStore(t)
	token, err := ks.CreateSession("192.168.1.5", "Mozilla/5.0")
	require.NoError(t, err)
	assert.Len(t, token, sessionLength)
	assert.True(t, ks.ValidateSession(token))
}

func TestKeyStore_InvalidSession(t *testing.T) {
	ks := newTestKeyStore(t)
	assert.False(t, ks.ValidateSession("nonexistent-token"))
	assert.False(t, ks.ValidateSession(""))
}

func TestKeyStore_SessionExpiry(t *testing.T) {
	ks := newTestKeyStore(t)
	token, err := ks.CreateSession("192.168.1.5", "test")
	require.NoError(t, err)

	// Manually expire the session
	ks.mu.Lock()
	ks.sessions[token].CreatedAt = time.Now().Add(-25 * time.Hour)
	ks.mu.Unlock()

	assert.False(t, ks.ValidateSession(token))
}

func TestKeyStore_SessionLRUEviction(t *testing.T) {
	ks := newTestKeyStore(t)
	var firstToken string
	for i := 0; i < maxSessions+5; i++ {
		token, err := ks.CreateSession("192.168.1.5", "test")
		require.NoError(t, err)
		if i == 0 {
			firstToken = token
		}
	}
	// First sessions should be evicted
	assert.False(t, ks.ValidateSession(firstToken))
}

// #3356: a new key is how a user locks out whoever had the old one, so it ends every session.
func TestKeyStore_KeyRegenerationEndsEverySession(t *testing.T) {
	ks := newTestKeyStore(t)
	a, err := ks.CreateSession("192.168.1.5", "test")
	require.NoError(t, err)
	b, err := ks.CreateSession("192.168.1.6", "test")
	require.NoError(t, err)
	_, err = ks.RegenerateKey()
	require.NoError(t, err)
	assert.False(t, ks.ValidateSession(a))
	assert.False(t, ks.ValidateSession(b))
	// Signing in again with the new key works, and LRU bookkeeping starts fresh.
	c, err := ks.CreateSession("192.168.1.5", "test")
	require.NoError(t, err)
	assert.True(t, ks.ValidateSession(c))
}

func TestKeyStore_RevokeSession(t *testing.T) {
	ks := newTestKeyStore(t)
	a, _ := ks.CreateSession("192.168.1.5", "test")
	b, _ := ks.CreateSession("192.168.1.6", "test")
	ks.RevokeSession(a)
	ks.RevokeSession("no-such-token")
	assert.False(t, ks.ValidateSession(a))
	assert.True(t, ks.ValidateSession(b), "only that session ends")
	assert.NotContains(t, ks.order, a)
}

// ── Rate Limiting Tests ─────────────────────────────────────

func TestKeyStore_RateLimit(t *testing.T) {
	ks := newTestKeyStore(t)
	ip := "10.0.0.1"
	for i := 0; i < rateLimitMax; i++ {
		assert.True(t, ks.CheckRateLimit(ip), "attempt %d should be allowed", i+1)
	}
	// Next attempt should be blocked
	assert.False(t, ks.CheckRateLimit(ip))
}

func TestKeyStore_RateLimitPerIP(t *testing.T) {
	ks := newTestKeyStore(t)
	// Exhaust rate limit for IP1
	for i := 0; i < rateLimitMax+1; i++ {
		ks.CheckRateLimit("10.0.0.1")
	}
	// IP2 should still be allowed
	assert.True(t, ks.CheckRateLimit("10.0.0.2"))
}

func TestKeyStore_RateLimitWindowReset(t *testing.T) {
	ks := newTestKeyStore(t)
	ip := "10.0.0.1"
	for i := 0; i < rateLimitMax+1; i++ {
		ks.CheckRateLimit(ip)
	}
	assert.False(t, ks.CheckRateLimit(ip))

	// Reset the window manually
	ks.mu.Lock()
	ks.rateMap[ip].windowAt = time.Now().Add(-2 * rateLimitWindow)
	ks.mu.Unlock()

	assert.True(t, ks.CheckRateLimit(ip))
}

// ── Localhost Check Tests ───────────────────────────────────

func TestIsLocalhost_IPv4(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:54321"
	assert.True(t, isLocalhost(r))
}

func TestIsLocalhost_IPv6(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "[::1]:54321"
	assert.True(t, isLocalhost(r))
}

func TestIsLocalhost_Remote(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.168.1.5:54321"
	assert.False(t, isLocalhost(r))
}

// ── ExtractAPIKey Tests ─────────────────────────────────────

func TestExtractAPIKey_BearerHeader(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer my-api-key-123")
	assert.Equal(t, "my-api-key-123", extractAPIKey(r))
}

func TestExtractAPIKey_QueryParam(t *testing.T) {
	r := httptest.NewRequest("GET", "/?api_key=my-key-456", nil)
	assert.Equal(t, "my-key-456", extractAPIKey(r))
}

func TestExtractAPIKey_None(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	assert.Equal(t, "", extractAPIKey(r))
}

func TestExtractAPIKey_HeaderPrecedence(t *testing.T) {
	r := httptest.NewRequest("GET", "/?api_key=query-key", nil)
	r.Header.Set("Authorization", "Bearer header-key")
	assert.Equal(t, "header-key", extractAPIKey(r))
}

// ── Middleware Tests ─────────────────────────────────────────

func newMiddlewareTest(t *testing.T) (*KeyStore, http.Handler) {
	t.Helper()
	ks := newTestKeyStore(t)
	handler := Middleware(ks)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	return ks, handler
}

func TestMiddleware_LocalhostBypass(t *testing.T) {
	_, handler := newMiddlewareTest(t)
	r := httptest.NewRequest("GET", "http://localhost/api/sessions", nil)
	r.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestMiddleware_ValidAPIKeyHeader(t *testing.T) {
	ks, handler := newMiddlewareTest(t)
	r := httptest.NewRequest("GET", "/api/sessions", nil)
	r.RemoteAddr = "192.168.1.5:54321"
	r.Header.Set("Authorization", "Bearer "+ks.Key())
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestMiddleware_ValidAPIKeyQueryParam(t *testing.T) {
	ks, handler := newMiddlewareTest(t)
	r := httptest.NewRequest("GET", "/?api_key="+ks.Key(), nil)
	r.RemoteAddr = "192.168.1.5:54321"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
	// Should also set session cookie
	cookies := w.Result().Cookies()
	found := false
	for _, c := range cookies {
		if isSessionCookieName(c.Name) {
			found = true
			break
		}
	}
	assert.True(t, found, "session cookie should be set on query param auth")
}

func TestMiddleware_InvalidAPIKey(t *testing.T) {
	_, handler := newMiddlewareTest(t)
	r := httptest.NewRequest("GET", "/api/sessions", nil)
	r.RemoteAddr = "192.168.1.5:54321"
	r.Header.Set("Authorization", "Bearer wrong-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMiddleware_ValidSessionCookie(t *testing.T) {
	ks, handler := newMiddlewareTest(t)
	token, err := ks.CreateSession("192.168.1.5", "test")
	require.NoError(t, err)
	r := httptest.NewRequest("GET", "/api/sessions", nil)
	r.RemoteAddr = "192.168.1.5:54321"
	r.AddCookie(&http.Cookie{Name: legacyCookieName, Value: token})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestMiddleware_ExpiredSessionCookie(t *testing.T) {
	ks, handler := newMiddlewareTest(t)
	token, err := ks.CreateSession("192.168.1.5", "test")
	require.NoError(t, err)
	// Expire the session
	ks.mu.Lock()
	ks.sessions[token].CreatedAt = time.Now().Add(-25 * time.Hour)
	ks.mu.Unlock()

	r := httptest.NewRequest("GET", "/api/sessions", nil)
	r.RemoteAddr = "192.168.1.5:54321"
	r.AddCookie(&http.Cookie{Name: legacyCookieName, Value: token})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMiddleware_NoAuth_APIRedirectsTo401(t *testing.T) {
	_, handler := newMiddlewareTest(t)
	r := httptest.NewRequest("GET", "/api/sessions", nil)
	r.RemoteAddr = "192.168.1.5:54321"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMiddleware_NoAuth_PageRedirectsToAuth(t *testing.T) {
	_, handler := newMiddlewareTest(t)
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.168.1.5:54321"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	assert.Equal(t, http.StatusTemporaryRedirect, w.Code)
	assert.Equal(t, "/auth?return_to=%2F", w.Header().Get("Location"))
}

func TestMiddleware_PageRedirectPreservesFullReturnPath(t *testing.T) {
	ks := newTestKeyStore(t)
	h := Middleware(ks)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/agent/11111111-2222-4333-8444-555555555555?view=terminal", nil)
	req.RemoteAddr = "192.168.1.5:1234"
	req.Host = "coral.example:8420"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusTemporaryRedirect, rr.Code)
	location, err := url.Parse(rr.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "/auth", location.Path)
	assert.Equal(t, req.URL.RequestURI(), location.Query().Get("return_to"))
}

func TestMiddleware_AuthPageAccessible(t *testing.T) {
	_, handler := newMiddlewareTest(t)
	r := httptest.NewRequest("GET", "/auth", nil)
	r.RemoteAddr = "192.168.1.5:54321"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestMiddleware_StaticAssetsAccessible(t *testing.T) {
	_, handler := newMiddlewareTest(t)
	r := httptest.NewRequest("GET", "/static/style.css", nil)
	r.RemoteAddr = "192.168.1.5:54321"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestMiddleware_RateLimitBlocks(t *testing.T) {
	ks, handler := newMiddlewareTest(t)
	_ = ks
	ip := "10.0.0.99"
	for i := 0; i < rateLimitMax; i++ {
		r := httptest.NewRequest("GET", "/api/sessions", nil)
		r.RemoteAddr = ip + ":54321"
		r.Header.Set("Authorization", "Bearer wrong-key")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
	}
	// Next attempt should hit rate limit
	r := httptest.NewRequest("GET", "/api/sessions", nil)
	r.RemoteAddr = ip + ":54321"
	r.Header.Set("Authorization", "Bearer wrong-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
}

// ── Cookie Attributes Tests ─────────────────────────────────

func TestSetSessionCookie_Attributes(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	newTestKeyStore(t).setSessionCookie(w, r, "test-token-123")
	cookies := w.Result().Cookies()
	require.Len(t, cookies, 1)
	c := cookies[0]
	assert.True(t, isSessionCookieName(c.Name), c.Name)
	assert.Equal(t, "test-token-123", c.Value)
	assert.Equal(t, "/", c.Path)
	assert.True(t, c.HttpOnly)
	assert.False(t, c.Secure, "localhost requests should not set Secure")
	assert.Equal(t, http.SameSiteLaxMode, c.SameSite)
	assert.Equal(t, int(sessionMaxAge.Seconds()), c.MaxAge)
}

func TestSetSessionCookie_SecureOnTLS(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "https://192.168.1.100/", nil)
	r.TLS = &tls.ConnectionState{} // simulate TLS
	r.RemoteAddr = "192.168.1.100:12345"
	newTestKeyStore(t).setSessionCookie(w, r, "test-token-456")
	cookies := w.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.True(t, cookies[0].Secure, "TLS requests should set Secure")
}

func TestSetSessionCookie_NotSecureOnPlainHTTP(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://192.168.1.100/", nil)
	r.RemoteAddr = "192.168.1.100:12345"
	newTestKeyStore(t).setSessionCookie(w, r, "test-token-789")
	cookies := w.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.False(t, cookies[0].Secure, "plain HTTP requests should not set Secure")
}

// A valid key must never be rate limited by its own successful use: a Coral
// hub polls a remote with its key every few seconds from one IP.
func TestMiddleware_ValidKeyNotRateLimited(t *testing.T) {
	ks, handler := newMiddlewareTest(t)
	for i := 0; i < rateLimitMax*5; i++ {
		r := httptest.NewRequest("GET", "/api/sessions", nil)
		r.RemoteAddr = "192.168.1.5:54321"
		r.Header.Set("Authorization", "Bearer "+ks.Key())
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		require.Equal(t, http.StatusOK, w.Code, "request %d", i+1)
	}
}

// Failed guesses are still limited, and once limited even the right key waits.
func TestMiddleware_FailedKeysAreRateLimited(t *testing.T) {
	ks, handler := newMiddlewareTest(t)
	do := func(key string) int {
		r := httptest.NewRequest("GET", "/api/sessions", nil)
		r.RemoteAddr = "192.168.1.9:54321"
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	for i := 0; i < rateLimitMax; i++ {
		require.Equal(t, http.StatusUnauthorized, do("wrong"), "guess %d", i+1)
	}
	assert.Equal(t, http.StatusTooManyRequests, do("wrong"))
	assert.Equal(t, http.StatusTooManyRequests, do(ks.Key()), "brute-force cap must hold for the right key too")
	assert.Equal(t, http.StatusOK, func() int { // another IP is unaffected
		r := httptest.NewRequest("GET", "/api/sessions", nil)
		r.RemoteAddr = "192.168.1.10:1"
		r.Header.Set("Authorization", "Bearer "+ks.Key())
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}())
}

// #3356: behind a reverse proxy that terminates TLS, the cookie is Secure when the proxy says
// https, but only a trusted proxy (a loopback peer, or CORAL_TRUSTED_PROXIES) is believed.
func TestSetSessionCookie_SecureBehindATrustedProxy(t *testing.T) {
	ks := newTestKeyStore(t)
	require.NoError(t, ks.SetTrustedProxies("10.0.0.0/8, 192.168.1.7"))
	secure := func(peer, proto string) bool {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/auth/key", nil)
		r.RemoteAddr = peer
		if proto != "" {
			r.Header.Set("X-Forwarded-Proto", proto)
		}
		ks.setSessionCookie(w, r, "tok")
		return w.Result().Cookies()[0].Secure
	}
	assert.True(t, secure("127.0.0.1:5000", "https"), "a proxy on this machine")
	assert.True(t, secure("[::1]:5000", "HTTPS"), "a proxy on this machine (IPv6, any case)")
	assert.True(t, secure("10.1.2.3:5000", "https"), "a proxy in a trusted CIDR")
	assert.True(t, secure("192.168.1.7:5000", "https, http"), "a trusted proxy IP; the first value wins")
	assert.False(t, secure("192.168.1.8:5000", "https"), "an untrusted peer can't claim https")
	assert.False(t, secure("10.1.2.3:5000", "http"), "a trusted proxy saying http")
	assert.False(t, secure("10.1.2.3:5000", ""), "no header")
}

func TestSetTrustedProxies_RejectsGarbage(t *testing.T) {
	ks := newTestKeyStore(t)
	assert.Error(t, ks.SetTrustedProxies("not-an-ip"))
	assert.Error(t, ks.SetTrustedProxies("10.0.0.0/99"))
	assert.NoError(t, ks.SetTrustedProxies(""))
	assert.NoError(t, ks.SetTrustedProxies(" ::1 , fd00::/8 "))
}

// #3511 follow-up: X-Forwarded-Host is believed only from a trusted proxy (loopback, or
// CORAL_TRUSTED_PROXIES); the LAST value counts, the one the proxy added (#3524); a nil
// KeyStore has none.
func TestForwardedHostOnlyFromATrustedProxy(t *testing.T) {
	ks, err := NewKeyStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, ks.SetTrustedProxies("10.0.0.0/8"))
	at := func(remote, fwd string) string {
		r := httptest.NewRequest("POST", "/api/settings", nil)
		r.RemoteAddr = remote
		if fwd != "" {
			r.Header.Set("X-Forwarded-Host", fwd)
		}
		return ks.ForwardedHost(r)
	}
	assert.Equal(t, "coral.example", at("127.0.0.1:5000", "coral.example"), "a loopback proxy")
	assert.Equal(t, "coral.example", at("10.1.2.3:5000", " evil.example , coral.example "), "a configured proxy that appended: the last value")
	// Appended as a second header line: the client's value first, the proxy's last.
	r := httptest.NewRequest("POST", "/api/settings", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Add("X-Forwarded-Host", "evil.example")
	r.Header.Add("X-Forwarded-Host", "coral.example")
	assert.Equal(t, "coral.example", ks.ForwardedHost(r), "repeated headers: the last")
	assert.Equal(t, "", at("192.168.1.5:5000", "coral.example"), "any other peer is ignored")
	assert.Equal(t, "", at("127.0.0.1:5000", ""), "no header")
	var none *KeyStore
	assert.Equal(t, "", none.ForwardedHost(httptest.NewRequest("POST", "/", nil)))
}
