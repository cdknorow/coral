package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/config"
)

// A loopback request that carries a reverse-proxy header is not a direct local client, so it
// does not get the no-credentials pass; it needs the key or a session, like a remote caller. A
// plain loopback request is unchanged. With require_auth_on_loopback on, even a plain loopback
// request needs auth, while the API key and a dashboard session still pass (#3585).
func TestLoopbackProxyHeaderAndRequireAuthSetting(t *testing.T) {
	ks, err := NewKeyStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { SetRequireAuthOnLoopback(false) })

	var got Method
	h := Middleware(ks)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = MethodFrom(r.Context()) }))
	serve := func(remote string, mod func(*http.Request)) (Method, int) {
		got = MethodNone
		r := httptest.NewRequest("GET", "/api/health", nil)
		r.RemoteAddr = remote
		r.Host = "127.0.0.1:8420"
		if mod != nil {
			mod(r)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return got, rec.Code
	}

	// Plain loopback: the pass, as before.
	m, _ := serve("127.0.0.1:5000", nil)
	assert.Equal(t, MethodLoopback, m, "plain loopback keeps the pass")

	// Loopback + each proxy header family: no pass; without a key it is refused.
	for _, set := range []func(*http.Request){
		func(r *http.Request) { r.Header.Set("X-Forwarded-For", "100.64.0.9") },
		func(r *http.Request) { r.Header.Set("X-Forwarded-Host", "coral.example.test") },
		func(r *http.Request) { r.Header.Set("Forwarded", "for=100.64.0.9") },
		func(r *http.Request) { r.Header.Set("X-Real-Ip", "100.64.0.9") },
		func(r *http.Request) { r.Header.Set("Via", "1.1 proxy") },
		func(r *http.Request) { r.Header.Set("True-Client-Ip", "100.64.0.9") },
		func(r *http.Request) { r.Header.Set("Cf-Connecting-Ip", "100.64.0.9") },
		func(r *http.Request) { r.Header.Set("Tailscale-User-Login", "someone@example.test") },
	} {
		m, code := serve("127.0.0.1:5000", set)
		assert.Equal(t, MethodNone, m, "a proxied loopback request must not get the pass")
		assert.Equal(t, http.StatusUnauthorized, code)
	}

	// Loopback + proxy header + a valid key: it authenticates as a normal remote caller would.
	m, _ = serve("127.0.0.1:5000", func(r *http.Request) {
		r.Header.Set("X-Forwarded-For", "100.64.0.9")
		r.Header.Set("Authorization", "Bearer "+ks.Key())
	})
	assert.Equal(t, MethodAPIKey, m, "a proxied request with the key still passes")

	// Setting ON: auth.Middleware still gives a direct-local request MethodLoopback — it only
	// distinguishes direct from proxied. The require_auth_on_loopback enforcement (a tokenless
	// direct-local request is refused) happens in the identity middleware downstream, covered by
	// the server-level e2e test TestRequireAuthOnLoopbackIsStrictLocal. A proxied request is still
	// refused here with the setting on.
	// A proxied request from off-box but with a valid key still authenticates normally.
	m, _ = serve("10.0.0.2:5000", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+ks.Key()) })
	assert.Equal(t, MethodAPIKey, m, "a remote request with the key passes")

	// Setting ON: auth.Middleware still gives a direct-local request MethodLoopback — the bypass
	// precedes the credential check, so a direct-local request gets the pass whatever it carries.
	// The require_auth_on_loopback enforcement (a tokenless direct-local request is refused)
	// happens in the identity middleware downstream (server e2e: TestRequireAuthOnLoopbackIsStrictLocal).
	// A proxied request is still refused here with the setting on.
	SetRequireAuthOnLoopback(true)
	m, _ = serve("127.0.0.1:5000", nil)
	assert.Equal(t, MethodLoopback, m, "a direct-local request still reaches the handlers (identity enforces the setting)")
	m, code := serve("127.0.0.1:5000", func(r *http.Request) { r.Header.Set("X-Forwarded-For", "100.64.0.9") })
	assert.Equal(t, MethodNone, m, "a proxied request gets no pass even with the setting on")
	assert.Equal(t, http.StatusUnauthorized, code)

	SetRequireAuthOnLoopback(false)
	m, _ = serve("127.0.0.1:5000", nil)
	assert.Equal(t, MethodLoopback, m, "with the setting off, a direct-local request keeps the pass")
}

// CONNECT (the local MITM proxy for agent browsing) stays trusted by loopback IP alone, with its
// external target Host and without the setting or proxy-header gates breaking it (#3585).
func TestLoopbackConnectStaysIPOnly(t *testing.T) {
	ks, err := NewKeyStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { SetRequireAuthOnLoopback(false) })
	var got Method
	h := Middleware(ks)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = MethodFrom(r.Context()) }))
	SetRequireAuthOnLoopback(true)
	r := httptest.NewRequest(http.MethodConnect, "chatgpt.com:443", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Host = "chatgpt.com:443"
	h.ServeHTTP(httptest.NewRecorder(), r)
	assert.Equal(t, MethodLoopback, got, "CONNECT from loopback stays IP-only even with the setting on")
}

// A direct loopback request with an external Host is still refused (DNS rebinding), per listener
// (#3585, point E: trust keys on the peer address, the Host is validated).
func TestLoopbackExternalHostStillRefused(t *testing.T) {
	ks, err := NewKeyStore(t.TempDir())
	require.NoError(t, err)
	h := Middleware(ks)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	r := httptest.NewRequest("GET", "/api/health", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Host = "evil.example.com"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestHasProxyHeaders(t *testing.T) {
	mk := func(k, v string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		if k != "" {
			r.Header.Set(k, v)
		}
		return r
	}
	assert.False(t, hasProxyHeaders(mk("", "")))
	assert.False(t, hasProxyHeaders(mk("Authorization", "Bearer x")), "a token is not a proxy header")
	assert.False(t, hasProxyHeaders(mk("X-Coral-Via-Tunnel", "1")), "the tunnel stamp is not an X-Forwarded/Tailscale header")
	for _, k := range []string{"X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host", "Forwarded", "Via", "X-Real-IP", "True-Client-IP", "CF-Connecting-IP", "X-Original-Forwarded-For", "Tailscale-User-Login", "X_Forwarded_For", "Tailscale_User_Login"} {
		assert.True(t, hasProxyHeaders(mk(k, "x")), k)
	}
}

// The env var overrides the stored setting at startup (the HTTP-free recovery, point C).
func TestResolveRequireAuthOnLoopback(t *testing.T) {
	// No env: the stored value decides.
	assert.True(t, ResolveRequireAuthOnLoopback("true"))
	assert.False(t, ResolveRequireAuthOnLoopback("false"))
	assert.False(t, ResolveRequireAuthOnLoopback(""))
	// Env off forces off even when stored on (recovery from a lockout).
	t.Setenv(RequireAuthOnLoopbackEnv, "off")
	assert.False(t, ResolveRequireAuthOnLoopback("true"))
	assert.True(t, EnvOverridesRequireAuthOnLoopback())
	// Env on forces on even when stored off.
	t.Setenv(RequireAuthOnLoopbackEnv, "true")
	assert.True(t, ResolveRequireAuthOnLoopback(""))
	// An EMPTY env var is NOT an override: the stored value decides, and a blank var never
	// silently turns the switch off (Security's NIT).
	t.Setenv(RequireAuthOnLoopbackEnv, "")
	assert.False(t, EnvOverridesRequireAuthOnLoopback())
	assert.True(t, ResolveRequireAuthOnLoopback("true"), "empty env falls through to the stored value")
	assert.False(t, ResolveRequireAuthOnLoopback("false"))
	t.Setenv(RequireAuthOnLoopbackEnv, "   ")
	assert.False(t, EnvOverridesRequireAuthOnLoopback(), "whitespace-only is not an override")
}

func TestSettingOn(t *testing.T) {
	for _, v := range []string{"true", "True", "1", "yes", "on", " TRUE "} {
		assert.True(t, config.Truthy(v), v)
	}
	for _, v := range []string{"", "false", "0", "no", "off", "nope"} {
		assert.False(t, config.Truthy(v), v)
	}
}
