package hubproxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cdknorow/coral/pkg/tunnel"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
)

type tunnelResolver struct{ sess *tunnel.Session }

func (r tunnelResolver) Resolve(_ context.Context, id string) (Target, error) {
	if id != "srv" {
		return nil, ErrUnknownServer
	}
	return TunnelTarget{Session: r.sess}, nil
}

// TestProxyOverTunnel runs the real handler through a real tunnel to a
// LocalHandler in front of a fake Coral API: HTTP, SSE and WebSocket.
func TestProxyOverTunnel(t *testing.T) {
	local := http.NewServeMux()
	local.HandleFunc("/api/hello", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello %s auth=%q cookie=%q", r.URL.Query().Get("n"), r.Header.Get("Authorization"), r.Header.Get("Cookie"))
	})
	local.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: 1\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	local.HandleFunc("/ws/echo", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		for {
			typ, b, err := c.Read(r.Context())
			if err != nil {
				return
			}
			c.Write(r.Context(), typ, b)
		}
	})
	ls := httptest.NewServer(local)
	defer ls.Close()

	sessions := make(chan *tunnel.Session, 1)
	ths := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := tunnel.AcceptHTTP(w, r, func(context.Context, string) (any, error) { return "x", nil }, tunnel.SessionConfig{})
		if err == nil {
			sessions <- s
		}
	}))
	defer ths.Close()
	cs, err := tunnel.Dial(context.Background(), "ws"+strings.TrimPrefix(ths.URL, "http"), "tok", tunnel.ClientOptions{
		Handler: &tunnel.LocalHandler{BaseURL: ls.URL, AllowPath: allowAll}, AllowInsecure: true,
	})
	require.NoError(t, err)
	defer cs.Close()
	sess := <-sessions
	defer sess.Close()

	hub := cloudHub(t, tunnelResolver{sess}, nil)

	// HTTP: hub-side credentials never reach the local API.
	req, _ := http.NewRequest("GET", hub.URL+"/api/remote/srv/api/hello?n=bob", nil)
	req.Header.Set("Authorization", "Bearer browser-session")
	req.Header.Set("Cookie", "sid=1")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, `hello bob auth="" cookie=""`, string(b))

	// Unknown server and disallowed paths are rejected by the handler.
	resp, _ = http.Get(hub.URL + "/api/remote/nope/api/hello")
	resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, _ = http.Get(hub.URL + "/api/remote/srv/admin/x")
	resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	// SSE: the first event arrives while the stream is still open.
	req, _ = http.NewRequest("GET", hub.URL+"/api/remote/srv/api/events", nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "data: 1\n", line)
	resp.Body.Close()

	// WebSocket bridge.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(hub.URL, "http")+"/api/remote/srv/ws/echo",
		&websocket.DialOptions{HTTPHeader: http.Header{"Origin": {hub.URL}}})
	require.NoError(t, err)
	require.NoError(t, c.Write(ctx, websocket.MessageText, []byte("ping")))
	_, msg, err := c.Read(ctx)
	require.NoError(t, err)
	assert.Equal(t, "ping", string(msg))
	c.Close(websocket.StatusCode(4002), "bye")
	require.Eventually(t, func() bool { return sess.NumStreams() == 0 }, 3*time.Second, 20*time.Millisecond)
}

func allowAll(_, _ string) bool { return true }

// cloudHub serves a Handler configured like the Cloud Hub: only the hub's own
// origin may open WebSockets. extra tweaks the options (nil for none).
func cloudHub(t *testing.T, res Resolver, extra func(*Options)) *httptest.Server {
	t.Helper()
	var hub *httptest.Server
	hub = httptest.NewUnstartedServer(nil)
	opts := Options{AllowedOrigins: []string{hub.Listener.Addr().String()}}
	if extra != nil {
		extra(&opts)
	}
	r := chi.NewRouter()
	r.Handle("/api/remote/{server}/*", NewWithOptions(res, opts))
	hub.Config.Handler = r
	hub.Start()
	t.Cleanup(hub.Close)
	return hub
}

// connectLocal runs a tunnel to a fake local API and returns the hub-side session.
func connectLocal(t *testing.T, local http.Handler, mod func(*tunnel.LocalHandler)) *tunnel.Session {
	t.Helper()
	ls := httptest.NewServer(local)
	t.Cleanup(ls.Close)
	sessions := make(chan *tunnel.Session, 1)
	ths := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := tunnel.AcceptHTTP(w, r, func(context.Context, string) (any, error) { return "x", nil }, tunnel.SessionConfig{})
		if err == nil {
			sessions <- s
		}
	}))
	t.Cleanup(ths.Close)
	lh := &tunnel.LocalHandler{BaseURL: ls.URL, AllowPath: allowAll}
	if mod != nil {
		mod(lh)
	}
	cs, err := tunnel.Dial(context.Background(), "ws"+strings.TrimPrefix(ths.URL, "http"), "tok", tunnel.ClientOptions{Handler: lh, AllowInsecure: true})
	require.NoError(t, err)
	t.Cleanup(func() { cs.Close() })
	s := <-sessions
	t.Cleanup(func() { s.Close() })
	return s
}

// P-3h: a malicious connector's HTML/JS/SVG must never be served as active
// content from the hub origin, and it must not set cookies or policy.
func TestMaliciousConnectorResponsesAreNeutralised(t *testing.T) {
	local := http.NewServeMux()
	serve := func(path, ct, body string, extra map[string]string) {
		local.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if ct != "" {
				w.Header().Set("Content-Type", ct)
			}
			for k, v := range extra {
				w.Header().Set(k, v)
			}
			io.WriteString(w, body)
		})
	}
	hostile := map[string]string{
		"Set-Cookie": "sid=evil; Path=/", "Content-Security-Policy": "script-src *", "Clear-Site-Data": "\"cache\"",
		"Refresh": "0;url=https://evil.example", "Link": "<https://evil.example/x.js>; rel=preload; as=script",
		"Access-Control-Allow-Origin": "*", "Access-Control-Allow-Credentials": "true", "Service-Worker-Allowed": "/",
		"Report-To": "{}", "Nel": "{}", "Strict-Transport-Security": "max-age=0", "Content-Disposition": "inline",
		"Location": "https://evil.example/phish",
	}
	serve("/api/page.html", "text/html; charset=utf-8", "<script>steal()</script>", hostile)
	serve("/api/evil.js", "application/javascript", "steal()", hostile)
	serve("/api/img.svg", "image/svg+xml", "<svg onload=steal()></svg>", nil)
	serve("/api/xml", "text/xml", "<x/>", nil)
	serve("/api/sniff", "", "<html><script>steal()</script>", nil) // no type: browser would sniff
	serve("/api/data.json", "application/json", `{"ok":true}`, nil)
	serve("/api/note.txt", "text/plain; charset=utf-8", "hi", nil)
	serve("/api/pic.png", "image/png", "\x89PNG", nil)
	hub := cloudHub(t, tunnelResolver{connectLocal(t, local, nil)}, nil)

	get := func(p string) *http.Response {
		resp, err := http.Get(hub.URL + "/api/remote/srv" + p)
		require.NoError(t, err)
		resp.Body.Close()
		return resp
	}
	for _, p := range []string{"/api/page.html", "/api/evil.js", "/api/img.svg", "/api/xml", "/api/sniff"} {
		resp := get(p)
		assert.Equal(t, "application/octet-stream", resp.Header.Get("Content-Type"), p)
		assert.Equal(t, "attachment", resp.Header.Get("Content-Disposition"), p)
	}
	for _, p := range []string{"/api/page.html", "/api/evil.js", "/api/data.json", "/api/note.txt", "/api/pic.png"} {
		resp := get(p)
		assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"), p)
		assert.Equal(t, "sandbox; default-src 'none'", resp.Header.Get("Content-Security-Policy"), p)
		assert.Equal(t, "same-origin", resp.Header.Get("Cross-Origin-Resource-Policy"), p)
	}
	hostileResp := get("/api/page.html")
	for _, k := range []string{"Set-Cookie", "Clear-Site-Data", "Refresh", "Link", "Access-Control-Allow-Origin", "Access-Control-Allow-Credentials",
		"Service-Worker-Allowed", "Report-To", "Nel", "Strict-Transport-Security", "Location"} {
		assert.Empty(t, hostileResp.Header.Get(k), k)
	}
	assert.Empty(t, hostileResp.Cookies())
	// Benign types pass through untouched.
	assert.Equal(t, "application/json", get("/api/data.json").Header.Get("Content-Type"))
	assert.Empty(t, get("/api/data.json").Header.Get("Content-Disposition"))
	assert.Equal(t, "text/plain; charset=utf-8", get("/api/note.txt").Header.Get("Content-Type"))
	assert.Equal(t, "image/png", get("/api/pic.png").Header.Get("Content-Type"))
}

// A relative Location is kept (it cannot leave the origin); absolute ones are dropped.
func TestStrictFilterLocation(t *testing.T) {
	for loc, keep := range map[string]bool{
		"/api/next": true, "https://evil.example/": false, "//evil.example/x": false, "\\evil.example": false, "http://127.0.0.1:8420/x": false,
	} {
		resp := &http.Response{Header: http.Header{"Location": {loc}, "Content-Type": {"application/json"}}}
		require.NoError(t, StrictResponseFilter(resp))
		assert.Equal(t, keep, resp.Header.Get("Location") != "", loc)
	}
}

// W-1: the hub must not dial the user's machine for a hostile page, and only
// its own origin may open WebSockets.
func TestWebSocketOriginChecks(t *testing.T) {
	var dials int32
	res := &countingResolver{}
	local := http.NewServeMux()
	local.HandleFunc("/ws/echo", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		typ, b, err := c.Read(r.Context())
		if err == nil {
			c.Write(r.Context(), typ, b)
		}
	})
	sess := connectLocal(t, local, nil)
	res.inner = tunnelResolver{sess}
	hub := cloudHub(t, res, nil)
	_ = &dials

	dial := func(origin string, hdr bool) (*websocket.Conn, *http.Response, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		t.Cleanup(cancel)
		h := http.Header{}
		if hdr {
			h.Set("Origin", origin)
		}
		return websocket.Dial(ctx, "ws"+strings.TrimPrefix(hub.URL, "http")+"/api/remote/srv/ws/echo", &websocket.DialOptions{HTTPHeader: h})
	}
	for name, tc := range map[string]struct {
		origin string
		set    bool
	}{
		"evil site":            {"https://evil.example", true},
		"null origin":          {"null", true},
		"localhost dev server": {"http://localhost:3000", true},
		"loopback":             {"http://127.0.0.1:3000", true},
		"missing origin":       {"", false},
		"suffix trick":         {hub.URL + ".evil.example", true},
		"scheme garbage":       {"file://" + hub.Listener.Addr().String(), true},
	} {
		before := res.resolves()
		_, resp, err := dial(tc.origin, tc.set)
		require.Error(t, err, name)
		if resp != nil {
			assert.Equal(t, http.StatusForbidden, resp.StatusCode, name)
		}
		// The refusal happens before any stream is opened to the user's machine.
		assert.Equal(t, 0, sess.NumStreams(), name)
		_ = before
	}
	c, _, err := dial(hub.URL, true)
	require.NoError(t, err, "the hub's own origin must work")
	c.Close(websocket.StatusNormalClosure, "")

	// A tunnel target with no configured origin fails closed.
	bare := httptest.NewServer(func() http.Handler {
		r := chi.NewRouter()
		r.Handle("/api/remote/{server}/*", New(tunnelResolver{sess}))
		return r
	}())
	defer bare.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err = websocket.Dial(ctx, "ws"+strings.TrimPrefix(bare.URL, "http")+"/api/remote/srv/ws/echo",
		&websocket.DialOptions{HTTPHeader: http.Header{"Origin": {bare.URL}}})
	assert.Error(t, err, "no AllowedOrigins configured: refuse")
}

type countingResolver struct {
	inner Resolver
	n     int32
	mu    sync.Mutex
}

func (c *countingResolver) Resolve(ctx context.Context, id string) (Target, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return c.inner.Resolve(ctx, id)
}
func (c *countingResolver) resolves() int32 { c.mu.Lock(); defer c.mu.Unlock(); return c.n }

// The self-hosted default is unchanged: localhost origins and a missing Origin work.
func TestSelfHostedOriginDefaultUnchanged(t *testing.T) {
	assert.True(t, originAllowed(reqWithOrigin("http://localhost:3000"), append(legacyOrigins, "hub.local"), false))
	assert.True(t, originAllowed(reqWithOrigin("http://hub.local"), append(legacyOrigins, "hub.local"), false))
	assert.True(t, originAllowed(reqWithOrigin(""), legacyOrigins, false))
	assert.False(t, originAllowed(reqWithOrigin("https://evil.example"), legacyOrigins, false))
	assert.False(t, originAllowed(reqWithOrigin("null"), legacyOrigins, false))
}

func reqWithOrigin(o string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	if o != "" {
		r.Header.Set("Origin", o)
	}
	return r
}

// The hub's CSRF token, cookies and fetch-metadata headers never reach the connector.
func TestRequestHeadersAreAllowlistedToTheConnector(t *testing.T) {
	var got http.Header
	local := http.NewServeMux()
	local.HandleFunc("/api/h", func(w http.ResponseWriter, r *http.Request) { got = r.Header.Clone() })
	hub := cloudHub(t, tunnelResolver{connectLocal(t, local, nil)}, nil)
	req, _ := http.NewRequest("GET", hub.URL+"/api/remote/srv/api/h", nil)
	for k, v := range map[string]string{
		"X-Csrf-Token": "t", "Sec-Fetch-Site": "same-origin", "Cookie": "s=1", "X-Admin": "1",
		"Accept": "application/json", "Accept-Language": "en", "Last-Event-Id": "7",
	} {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.NotNil(t, got)
	for _, k := range []string{"X-Csrf-Token", "Sec-Fetch-Site", "Cookie", "X-Admin"} {
		assert.Empty(t, got.Get(k), k)
	}
	assert.Equal(t, "application/json", got.Get("Accept"))
	assert.Equal(t, "en", got.Get("Accept-Language"))
	assert.Equal(t, "7", got.Get("Last-Event-Id"))
}

// A policy can make a proxied WebSocket read-only and end it when access is withdrawn.
func TestStreamPolicyGatesFramesAndClosesTheBridge(t *testing.T) {
	var got = make(chan string, 8)
	local := http.NewServeMux()
	local.HandleFunc("/ws/terminal/x", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		for {
			_, b, err := c.Read(r.Context())
			if err != nil {
				return
			}
			got <- string(b)
			c.Write(r.Context(), websocket.MessageText, []byte("out:"+string(b)))
		}
	})
	ls := httptest.NewServer(local)
	defer ls.Close()
	sessions := make(chan *tunnel.Session, 1)
	ths := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := tunnel.AcceptHTTP(w, r, func(context.Context, string) (any, error) { return "x", nil }, tunnel.SessionConfig{})
		if err == nil {
			sessions <- s
		}
	}))
	defer ths.Close()
	cs, err := tunnel.Dial(context.Background(), "ws"+strings.TrimPrefix(ths.URL, "http"), "tok", tunnel.ClientOptions{
		Handler: &tunnel.LocalHandler{BaseURL: ls.URL, AllowPath: func(string, string) bool { return true }}, AllowInsecure: true,
	})
	require.NoError(t, err)
	defer cs.Close()
	sess := <-sessions
	defer sess.Close()

	var allow atomic.Bool
	closed := make(chan struct{})
	h := NewWithOptions(tunnelResolver{sess}, Options{AllowedOrigins: []string{"hub.test"}})
	r := chi.NewRouter()
	r.Handle("/api/remote/{server}/*", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := WithStreamPolicy(req.Context(), StreamPolicy{AllowClientFrame: allow.Load, Closed: closed})
		h.ServeHTTP(w, req.WithContext(ctx))
	}))
	hub := httptest.NewServer(r)
	defer hub.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(hub.URL, "http")+"/api/remote/srv/ws/terminal/x", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {"http://hub.test"}},
	})
	require.NoError(t, err)

	// Read-only: input is dropped, not forwarded.
	require.NoError(t, c.Write(ctx, websocket.MessageText, []byte("rm -rf")))
	select {
	case v := <-got:
		t.Fatalf("input reached the terminal while read-only: %q", v)
	case <-time.After(300 * time.Millisecond):
	}
	// Mode raised: input flows.
	allow.Store(true)
	require.NoError(t, c.Write(ctx, websocket.MessageText, []byte("ls")))
	select {
	case v := <-got:
		assert.Equal(t, "ls", v)
	case <-time.After(3 * time.Second):
		t.Fatal("input not forwarded")
	}
	_, msg, err := c.Read(ctx)
	require.NoError(t, err)
	assert.Equal(t, "out:ls", string(msg))

	// Access withdrawn: the bridge ends with 4403.
	close(closed)
	_, _, err = c.Read(ctx)
	assert.Equal(t, StatusAccessChanged, websocket.CloseStatus(err))
}

type countMeter struct{ in, out atomic.Int64 }

func (c *countMeter) WaitIn(_ context.Context, n int) error  { c.in.Add(int64(n)); return nil }
func (c *countMeter) WaitOut(_ context.Context, n int) error { c.out.Add(int64(n)); return nil }

func TestTunnelTargetMetersBothDirections(t *testing.T) {
	local := http.NewServeMux()
	local.HandleFunc("/api/echo", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Write(append([]byte("got:"), b...))
	})
	ls := httptest.NewServer(local)
	defer ls.Close()
	sessions := make(chan *tunnel.Session, 1)
	ths := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := tunnel.AcceptHTTP(w, r, func(context.Context, string) (any, error) { return "x", nil }, tunnel.SessionConfig{})
		if err == nil {
			sessions <- s
		}
	}))
	defer ths.Close()
	cs, err := tunnel.Dial(context.Background(), "ws"+strings.TrimPrefix(ths.URL, "http"), "tok", tunnel.ClientOptions{
		Handler: &tunnel.LocalHandler{BaseURL: ls.URL, AllowPath: func(string, string) bool { return true }}, AllowInsecure: true,
	})
	require.NoError(t, err)
	defer cs.Close()
	sess := <-sessions
	defer sess.Close()

	m := &countMeter{}
	tt := TunnelTarget{Session: sess, Meter: m}
	req, _ := http.NewRequest("POST", "http://x/api/echo", strings.NewReader(strings.Repeat("a", 1000)))
	resp, err := tt.RoundTrip(req)
	require.NoError(t, err)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, 1004, len(b))
	assert.Equal(t, int64(1000), m.in.Load())
	assert.Equal(t, int64(1004), m.out.Load())
}
