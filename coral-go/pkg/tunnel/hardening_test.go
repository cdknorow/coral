package tunnel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Default deny: a LocalHandler with no AllowPath reaches nothing, and the
// connector's own credentials are never attached to a request that was refused.
func TestLocalHandlerDefaultDenyNeverAuthorizes(t *testing.T) {
	hub := newHub(t, fastCfg())
	var hits, authorized atomic.Int32
	sess, _ := connect(t, hub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte("ok"))
	}), func(l *LocalHandler) {
		l.AllowPath = nil // the default
		l.Authorize = func(h http.Header) { authorized.Add(1); h.Set("Authorization", "Bearer local-key") }
	})
	req, _ := http.NewRequest("GET", "http://x/api/agents", nil)
	_, err := sess.RoundTrip(req)
	var se *StreamError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, CodePathNotAllowed, se.Code)
	_, err = sess.DialWS(context.Background(), "/ws/terminal/x", "", nil)
	assert.Error(t, err)
	assert.Zero(t, hits.Load(), "nothing reached the local API")
	assert.Zero(t, authorized.Load(), "Authorize ran for a denied request")
}

func TestPathRulesAllowlistAndGate(t *testing.T) {
	launchOn := false
	rules := PathRules{
		Rules: []PathRule{
			{Prefix: "/api/agents", Exact: true}, {Prefix: "/api/agents/"}, // GET/HEAD only
			{Prefix: "/api/sessions/", Methods: []string{"GET", "POST"}},    // explicit methods
			{Prefix: "/api/health", Exact: true},                            // exact
			{Prefix: "/api/launch", Exact: true, Methods: []string{"POST"}}, // gated below
			{Prefix: "/ws/terminal/"},                                       // WS counts as GET
		},
		Gate: func(method, p string) bool { return !strings.HasPrefix(p, "/api/launch") || launchOn },
	}
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/api/agents", true}, {"GET", "/api/agents/x", true}, {"HEAD", "/api/agents", true},
		{"POST", "/api/agents", false}, {"DELETE", "/api/agents/x", false},
		{"POST", "/api/sessions/1/send", true}, {"PUT", "/api/sessions/1", false},
		{"GET", "/api/health", true}, {"GET", "/api/healthz", false}, {"GET", "/api/health/x", false},
		{"GET", "/api/settings", false}, {"GET", "/api/cloud/token", false}, {"POST", "/api/shutdown", false},
		{"GET", "/ws/terminal/abc", true}, {"GET", "/ws/other", false},
		{"POST", "/api/launch", false}, // gate closed
		{"TRACE", "/api/agents", false}, {"CONNECT", "/api/agents", false},
	} {
		assert.Equal(t, tc.want, rules.Allow(tc.method, tc.path), "%s %s", tc.method, tc.path)
	}
	launchOn = true
	assert.True(t, rules.Allow("POST", "/api/launch"), "gate open")
	assert.False(t, rules.Allow("GET", "/api/launch"), "method rule still applies")
	assert.False(t, (PathRules{}).Allow("GET", "/api/agents"), "no rules, no access")
}

func TestPathRulesEndToEndDeniedPathsNeverAuthorize(t *testing.T) {
	hub := newHub(t, fastCfg())
	var hits, authorized atomic.Int32
	rules := PathRules{Rules: []PathRule{{Prefix: "/api/agents/"}}}
	sess, _ := connect(t, hub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(r.Header.Get("Authorization")))
	}), func(l *LocalHandler) {
		l.AllowPath = rules.Allow
		l.Authorize = func(h http.Header) { authorized.Add(1); h.Set("Authorization", "Bearer local-key") }
	})
	resp := do(t, sess, "GET", "/api/agents/list", "", nil, nil)
	resp.Body.Close()
	assert.Equal(t, int32(1), hits.Load())
	assert.Equal(t, int32(1), authorized.Load())
	for _, p := range []string{"/api/settings", "/api/cloud/token", "/api/admin/keys"} {
		req, _ := http.NewRequest("GET", "http://x"+p, nil)
		_, err := sess.RoundTrip(req)
		assert.Error(t, err, p)
	}
	assert.Equal(t, int32(1), hits.Load(), "denied paths never reached the local API")
	assert.Equal(t, int32(1), authorized.Load(), "denied paths never got credentials")
}

func TestScrubUsesAnAllowlist(t *testing.T) {
	in := http.Header{}
	for k, v := range map[string]string{
		"Accept": "a", "accept-language": "en", "Content-Type": "application/json", "Range": "bytes=0-1", "Last-Event-ID": "9",
		"Cookie": "c", "Authorization": "x", "X-Csrf-Token": "t", "Sec-Fetch-Mode": "cors", "X-Forwarded-For": "1.2.3.4",
		"X-Custom-Trust": "yes", "Origin": "https://hub", "Referer": "https://hub/x", "Host": "evil",
	} {
		in[k] = []string{v} // raw keys, as a hostile hub could send them
	}
	out := scrub(in, http.MethodGet, "/api/health")
	assert.Equal(t, "a", out.Get("Accept"))
	assert.Equal(t, "en", out.Get("Accept-Language"))
	assert.Equal(t, "application/json", out.Get("Content-Type"))
	assert.Equal(t, "9", out.Get("Last-Event-Id"))
	for _, k := range []string{"Cookie", "Authorization", "X-Csrf-Token", "Sec-Fetch-Mode", "X-Forwarded-For", "X-Custom-Trust", "Origin", "Referer", "Host"} {
		assert.Empty(t, out.Get(k), k)
	}
}

func TestValidQueryRejectsSpacesAndNonASCII(t *testing.T) {
	for q, ok := range map[string]bool{
		"": true, "a=1&b=%20x": true, "n=caf%C3%A9": true,
		"a=1 b": false, "a=é": false, "a=\x7f": false, "a=\x00": false, "a#b": false, "a=\tb": false,
	} {
		assert.Equal(t, ok, validQuery(q), "%q", q)
	}
}

// ErrUnauthorized is permanent and reaches the UI through OnState; an outage in
// the hub's AuthFunc is not.
func TestRunSurfacesRevocationAndRetriesOutage(t *testing.T) {
	var revoked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		AcceptHTTP(w, r, func(context.Context, string) (any, error) { revoked.Add(1); return nil, ErrUnauthorized }, fastCfg())
	}))
	defer srv.Close()
	var states []error
	err := Run(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), "t", ClientOptions{
		Handler: &LocalHandler{BaseURL: "http://127.0.0.1:1", AllowPath: allowAll}, AllowInsecure: true,
		Backoff: Backoff{Min: time.Millisecond, Max: 5 * time.Millisecond},
		OnState: func(c bool, err error) { states = append(states, err) },
	})
	assert.ErrorIs(t, err, ErrUnauthorized)
	require.Len(t, states, 1)
	assert.ErrorIs(t, states[0], ErrUnauthorized, "OnState tells the UI the token was revoked")
	assert.Equal(t, int32(1), revoked.Load(), "no retry after unauthorized")

	// AuthFunc outage (DB down): temporary, surfaced, retried, then recovers.
	assert.Equal(t, ErrRetryLater, ErrAuthUnavailable)
	var calls atomic.Int32
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		AcceptHTTP(w, r, func(context.Context, string) (any, error) {
			if calls.Add(1) <= 3 {
				return nil, ErrAuthUnavailable
			}
			return "id", nil
		}, fastCfg())
	}))
	defer srv2.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var seen atomic.Int32
	up := make(chan struct{}, 1)
	go Run(ctx, "ws"+strings.TrimPrefix(srv2.URL, "http"), "t", ClientOptions{
		Handler: &LocalHandler{BaseURL: "http://127.0.0.1:1", AllowPath: allowAll}, AllowInsecure: true, SessionConfig: fastCfg(),
		Backoff: Backoff{Min: time.Millisecond, Max: 5 * time.Millisecond},
		OnState: func(c bool, err error) {
			if err != nil && assert.ObjectsAreEqual(err, ErrRetryLater) {
				seen.Add(1)
			}
			if c {
				select {
				case up <- struct{}{}:
				default:
				}
			}
		},
	})
	select {
	case <-up:
	case <-time.After(5 * time.Second):
		t.Fatal("connector gave up during an auth outage")
	}
	assert.GreaterOrEqual(t, seen.Load(), int32(3), "each refusal is reported as temporary")
}

// The token travels in the first frame to wherever the handshake lands, so a
// redirecting hub must not be able to move it.
func TestDialNeverFollowsRedirects(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/tunnel", http.StatusTemporaryRedirect)
	}))
	defer redir.Close()
	u := "ws" + strings.TrimPrefix(redir.URL, "http")
	h := &LocalHandler{BaseURL: "http://127.0.0.1:1", AllowPath: allowAll}

	_, err := Dial(context.Background(), u, "secret-token", ClientOptions{Handler: h, AllowInsecure: true})
	assert.Error(t, err)
	// Even a caller-supplied client that would follow redirects is overridden.
	follow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	_, err = Dial(context.Background(), u, "secret-token", ClientOptions{Handler: h, AllowInsecure: true, HTTPClient: follow})
	assert.Error(t, err)
	assert.Zero(t, elsewhere.Load(), "the redirect target was contacted")
}

func TestAllowInsecureIsLoopbackOnly(t *testing.T) {
	h := &LocalHandler{BaseURL: "http://127.0.0.1:1", AllowPath: allowAll}
	for _, u := range []string{
		"ws://hub.example/tunnel", "http://hub.example/tunnel", "ws://10.0.0.5/tunnel", "ws://192.168.1.2:8450/tunnel",
		"ws://127.0.0.1.evil.example/tunnel", "ws://localhost.evil.example/tunnel", "ws://[2001:db8::1]/tunnel",
	} {
		_, err := Dial(context.Background(), u, "t", ClientOptions{Handler: h, AllowInsecure: true})
		if assert.Error(t, err, u) {
			assert.Contains(t, err.Error(), "loopback", u)
		}
		assert.Error(t, checkURL(u, true), u)
	}
	for _, u := range []string{"ws://127.0.0.1:8450/t", "ws://localhost:8450/t", "ws://[::1]:8450/t", "http://127.0.0.2/t", "wss://hub.example/tunnel"} {
		assert.NoError(t, checkURL(u, true), u)
	}
	assert.Error(t, checkURL("ws://127.0.0.1/t", false), "cleartext still needs AllowInsecure")
}
