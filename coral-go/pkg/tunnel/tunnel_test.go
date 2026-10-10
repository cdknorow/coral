package tunnel

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
)

func fastCfg() SessionConfig {
	return SessionConfig{KeepAlive: 100 * time.Millisecond, DeadAfter: 400 * time.Millisecond}
}

func do(t *testing.T, s *Session, method, path, query string, body io.Reader, hdr http.Header) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, "http://ignored"+path, body)
	require.NoError(t, err)
	req.URL.RawQuery = query
	for k, v := range hdr {
		req.Header[k] = v
	}
	resp, err := s.RoundTrip(req)
	require.NoError(t, err)
	return resp
}

func TestHTTPRoundTrip(t *testing.T) {
	hub := newHub(t, fastCfg())
	var gotAuth, gotCookie, gotOrigin, gotCustom, gotHost, gotAccept string
	sess, _ := connect(t, hub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotCookie, gotOrigin = r.Header.Get("Authorization"), r.Header.Get("Cookie"), r.Header.Get("Origin")
		gotCustom, gotHost = r.Header.Get("X-Custom")+r.Header.Get("X-Csrf-Token")+r.Header.Get("Sec-Fetch-Site"), r.Host
		gotAccept = r.Header.Get("Accept")
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Reply", "yes")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, "%s %s?%s body=%s", r.Method, r.URL.EscapedPath(), r.URL.RawQuery, b)
	}), func(l *LocalHandler) {
		l.Authorize = func(h http.Header) { h.Set("Authorization", "Bearer local-key") }
	})
	assert.Equal(t, 1, sess.Version())
	assert.Equal(t, "server-1", sess.Identity())
	assert.Equal(t, "test", sess.CoralVersion())

	resp := do(t, sess, "POST", "/api/things/a%20b", "x=1&y=2", strings.NewReader("hello"), http.Header{
		"Authorization": {"Bearer hub-attacker"}, "Cookie": {"s=1"}, "Origin": {"http://evil"}, "X-Custom": {"v"}, "X-Csrf-Token": {"t"}, "Sec-Fetch-Site": {"cross-site"}, "Accept": {"application/json"},
	})
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Equal(t, "yes", resp.Header.Get("X-Reply"))
	assert.Equal(t, "POST /api/things/a%20b?x=1&y=2 body=hello", string(b))
	assert.Equal(t, "Bearer local-key", gotAuth, "hub credentials replaced by the connector's own")
	assert.Empty(t, gotCookie)
	assert.Empty(t, gotOrigin)
	assert.Empty(t, gotCustom, "header allowlist: unknown headers are not forwarded")
	assert.True(t, strings.HasPrefix(gotHost, "127.0.0.1:"), gotHost)
	assert.Equal(t, "application/json", gotAccept, "allowlisted headers still pass")
}

func TestLargeBodyBothWays(t *testing.T) {
	hub := newHub(t, fastCfg())
	sess, _ := connect(t, hub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(w, r.Body) // echo
	}), nil)
	payload := strings.Repeat("0123456789abcdef", 1<<16) // 1 MiB
	resp := do(t, sess, "PUT", "/api/echo", "", strings.NewReader(payload), nil)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, len(payload), len(b))
	assert.True(t, string(b) == payload)
}

func TestSSEStreams(t *testing.T) {
	hub := newHub(t, fastCfg())
	release := make(chan struct{})
	sess, _ := connect(t, hub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		fmt.Fprint(w, "data: one\n\n")
		fl.Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "data: two\n\n")
		fl.Flush()
	}), nil)
	resp := do(t, sess, "GET", "/api/events", "", nil, http.Header{"Accept": {"text/event-stream"}})
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	line, err := br.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "data: one\n", line, "first event arrives before the upstream finishes")
	close(release)
	rest, err := io.ReadAll(br)
	require.NoError(t, err)
	assert.Contains(t, string(rest), "data: two")
}

func TestCancelPropagatesToLocalRequest(t *testing.T) {
	hub := newHub(t, fastCfg())
	cancelled := make(chan struct{})
	sess, _ := connect(t, hub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(cancelled)
	}), nil)
	resp := do(t, sess, "GET", "/api/slow", "", nil, nil)
	resp.Body.Close() // browser went away
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("local request was not cancelled")
	}
	require.Eventually(t, func() bool { return sess.NumStreams() == 0 }, 2*time.Second, 10*time.Millisecond)
}

func TestWebSocketEchoAndCloseCode(t *testing.T) {
	hub := newHub(t, fastCfg())
	sess, _ := connect(t, hub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"coral"}})
		if err != nil {
			return
		}
		for {
			typ, b, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if string(b) == "bye" {
				c.Close(websocket.StatusCode(4001), "done")
				return
			}
			c.Write(r.Context(), typ, append([]byte("echo:"), b...))
		}
	}), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := sess.DialWS(ctx, "/ws/term", "a=1", []string{"coral"})
	require.NoError(t, err)
	assert.Equal(t, "coral", c.Subprotocol())
	require.NoError(t, c.Write(ctx, websocket.MessageText, []byte("hi")))
	typ, b, err := c.Read(ctx)
	require.NoError(t, err)
	assert.Equal(t, websocket.MessageText, typ)
	assert.Equal(t, "echo:hi", string(b))
	require.NoError(t, c.Write(ctx, websocket.MessageBinary, []byte{1, 2, 3}))
	typ, b, err = c.Read(ctx)
	require.NoError(t, err)
	assert.Equal(t, websocket.MessageBinary, typ)
	assert.Equal(t, []byte("echo:\x01\x02\x03"), b)

	require.NoError(t, c.Write(ctx, websocket.MessageText, []byte("bye")))
	_, _, err = c.Read(ctx)
	assert.Equal(t, websocket.StatusCode(4001), websocket.CloseStatus(err))
	var ce websocket.CloseError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "done", ce.Reason)
	require.Eventually(t, func() bool { return sess.NumStreams() == 0 }, 2*time.Second, 10*time.Millisecond)
}

func TestWebSocketHandshakeRejection(t *testing.T) {
	hub := newHub(t, fastCfg())
	sess, _ := connect(t, hub, http.NotFoundHandler(), nil)
	_, err := sess.DialWS(context.Background(), "/ws/missing", "", nil)
	var se *StreamError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, http.StatusNotFound, se.Status)
}

func TestManyConcurrentStreams(t *testing.T) {
	cfg := fastCfg()
	cfg.MaxStreams = 512
	hub := newHub(t, cfg)
	sess, _ := connect(t, hub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s|%s", r.URL.Path, b)
	}), nil)
	var wg sync.WaitGroup
	var failures atomic.Int32
	for i := 0; i < 300; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("POST", fmt.Sprintf("http://x/api/n/%d", i), strings.NewReader(fmt.Sprint("b", i)))
			resp, err := sess.RoundTrip(req)
			if err != nil {
				failures.Add(1)
				return
			}
			b, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || string(b) != fmt.Sprintf("/api/n/%d|b%d", i, i) {
				failures.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.Zero(t, failures.Load())
	require.Eventually(t, func() bool { return sess.NumStreams() == 0 }, 3*time.Second, 10*time.Millisecond)
}

func TestHubStreamCap(t *testing.T) {
	cfg := fastCfg()
	cfg.MaxStreams = 2
	hub := newHub(t, cfg)
	block := make(chan struct{})
	defer close(block)
	sess, _ := connect(t, hub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-block
	}), nil)
	r1 := do(t, sess, "GET", "/api/a", "", nil, nil)
	r2 := do(t, sess, "GET", "/api/b", "", nil, nil)
	req, _ := http.NewRequest("GET", "http://x/api/c", nil)
	_, err := sess.RoundTrip(req)
	assert.ErrorIs(t, err, ErrTooManyStreams)
	r1.Body.Close()
	require.Eventually(t, func() bool { // slot freed (the connector releases its side a moment later)
		req, _ := http.NewRequest("GET", "http://x/api/c", nil)
		resp, err := sess.RoundTrip(req)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return true
	}, 3*time.Second, 20*time.Millisecond)
	r2.Body.Close()
}

func TestConnectorStreamCap(t *testing.T) {
	// The connector enforces its own cap even if the hub does not.
	hub := newHub(t, SessionConfig{KeepAlive: 100 * time.Millisecond, DeadAfter: 400 * time.Millisecond, MaxStreams: 100})
	block := make(chan struct{})
	ls := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-block
	}))
	defer ls.Close()
	defer close(block) // runs before ls.Close
	cs, err := Dial(context.Background(), hub.URL(), testToken, ClientOptions{
		SessionConfig: SessionConfig{KeepAlive: 100 * time.Millisecond, DeadAfter: 400 * time.Millisecond, MaxStreams: 1},
		Handler:       &LocalHandler{BaseURL: ls.URL, AllowPath: allowAll}, AllowInsecure: true,
	})
	require.NoError(t, err)
	defer cs.Close()
	sess := hub.next(t)
	r1 := do(t, sess, "GET", "/api/a", "", nil, nil)
	defer r1.Body.Close()
	req, _ := http.NewRequest("GET", "http://x/api/b", nil)
	_, err = sess.RoundTrip(req)
	var se *StreamError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, CodeTooManyStreams, se.Code)
}

func TestConnectorCannotOpenStreams(t *testing.T) {
	hub := newHub(t, fastCfg())
	sess, cs := connect(t, hub, http.NotFoundHandler(), nil)
	st, err := cs.ys.OpenStream()
	require.NoError(t, err)
	_ = st.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := st.Read(make([]byte, 1))
	assert.Equal(t, 0, n)
	assert.ErrorIs(t, err, io.EOF, "hub closes streams the connector opens")
	_ = sess
}

func TestDropMidStreamAndReconnect(t *testing.T) {
	hub := newHub(t, fastCfg())
	proxy := newFlakyProxy(t, strings.TrimPrefix(hub.Server.URL, "http://"))
	started := make(chan struct{})
	var startedOnce sync.Once
	ls := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: 1\n\n")
		w.(http.Flusher).Flush()
		startedOnce.Do(func() { close(started) })
		<-r.Context().Done()
	}))
	defer ls.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var states []string
	var mu sync.Mutex
	runDone := make(chan error, 1)
	go func() {
		runDone <- Run(ctx, "ws://"+proxy.Addr(), testToken, ClientOptions{
			SessionConfig: fastCfg(), Handler: &LocalHandler{BaseURL: ls.URL, AllowPath: allowAll}, AllowInsecure: true,
			Backoff: Backoff{Min: 20 * time.Millisecond, Max: 200 * time.Millisecond},
			OnState: func(c bool, err error) {
				mu.Lock()
				states = append(states, fmt.Sprint(c))
				mu.Unlock()
			},
		})
	}()
	s1 := hub.next(t)
	resp := do(t, s1, "GET", "/api/events", "", nil, nil)
	br := bufio.NewReader(resp.Body)
	line, err := br.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "data: 1\n", line)
	<-started

	proxy.Drop() // network failure mid-stream

	_, err = io.ReadAll(br)
	assert.Error(t, err, "a dropped stream must not look like a clean end")
	resp.Body.Close()
	select {
	case <-s1.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("hub session did not end")
	}
	s2 := hub.next(t) // connector reconnected on its own
	resp2 := do(t, s2, "GET", "/api/events", "", nil, nil)
	line, err = bufio.NewReader(resp2.Body).ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "data: 1\n", line)
	resp2.Body.Close()

	cancel()
	select {
	case err := <-runDone:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop")
	}
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "true", states[0])
	assert.Contains(t, states, "false")
}

func TestKeepaliveTimeout(t *testing.T) {
	hub := newHub(t, fastCfg())
	proxy := newFlakyProxy(t, strings.TrimPrefix(hub.Server.URL, "http://"))
	cs, err := Dial(context.Background(), "ws://"+proxy.Addr(), testToken, ClientOptions{
		SessionConfig: fastCfg(), Handler: &LocalHandler{BaseURL: "http://127.0.0.1:1", AllowPath: allowAll}, AllowInsecure: true,
	})
	require.NoError(t, err)
	defer cs.Close()
	s := hub.next(t)
	select {
	case <-cs.Done():
		t.Fatal("session died while healthy")
	case <-time.After(500 * time.Millisecond): // several keepalive intervals
	}
	start := time.Now()
	proxy.Blackhole(true) // sockets stay open, no bytes move
	select {
	case <-cs.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("connector never declared the tunnel dead")
	}
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("hub never declared the tunnel dead")
	}
	assert.Less(t, time.Since(start), 3*time.Second)
}

func rawHandshake(t *testing.T, hub *hubHarness, hello any) (Welcome, *websocket.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, hub.URL(), nil)
	require.NoError(t, err)
	b, _ := json.Marshal(hello)
	require.NoError(t, c.Write(ctx, websocket.MessageBinary, b))
	_, msg, err := c.Read(ctx)
	require.NoError(t, err)
	var w Welcome
	require.NoError(t, json.Unmarshal(msg, &w))
	return w, c
}

func TestVersionNegotiation(t *testing.T) {
	hub := newHub(t, fastCfg())
	// A newer connector is served at the hub's version.
	w, c := rawHandshake(t, hub, Hello{ProtocolVersion: 99, ServerToken: testToken, CoralVersion: "future"})
	assert.Equal(t, ProtocolVersion, w.AcceptedVersion)
	assert.Empty(t, w.Error)
	c.CloseNow()
	hub.next(t)

	// Too old: refused with a safe error.
	w, c = rawHandshake(t, hub, Hello{ProtocolVersion: 0, ServerToken: testToken})
	assert.Equal(t, CodeUnsupportedVersion, w.Error)
	assert.Zero(t, w.AcceptedVersion)
	c.CloseNow()

	// Through the client API.
	w, c = rawHandshake(t, hub, Hello{ProtocolVersion: -3, ServerToken: testToken})
	assert.Equal(t, CodeUnsupportedVersion, w.Error)
	c.CloseNow()
}

func TestAuthBeforeAnythingElse(t *testing.T) {
	hub := newHub(t, fastCfg())
	// Bad token and bad version together: the peer learns only "unauthorized".
	w, c := rawHandshake(t, hub, Hello{ProtocolVersion: 0, ServerToken: "wrong"})
	assert.Equal(t, CodeUnauthorized, w.Error)
	c.CloseNow()

	_, err := Dial(context.Background(), hub.URL(), "wrong", ClientOptions{Handler: &LocalHandler{BaseURL: "http://127.0.0.1:1", AllowPath: allowAll}, AllowInsecure: true})
	assert.ErrorIs(t, err, ErrUnauthorized)

	// Run stops instead of hammering the hub with a revoked token.
	err = Run(context.Background(), hub.URL(), "wrong", ClientOptions{Handler: &LocalHandler{BaseURL: "http://127.0.0.1:1", AllowPath: allowAll}, AllowInsecure: true})
	assert.ErrorIs(t, err, ErrUnauthorized)

	select {
	case <-hub.Sessions:
		t.Fatal("unauthenticated peer got a session")
	default:
	}
}

func TestHandshakeAbuse(t *testing.T) {
	hub := newHub(t, fastCfg())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Oversized first message is cut off by the read limit.
	c, _, err := websocket.Dial(ctx, hub.URL(), nil)
	require.NoError(t, err)
	_ = c.Write(ctx, websocket.MessageBinary, []byte(strings.Repeat("x", 64<<10)))
	_, _, err = c.Read(ctx)
	assert.Error(t, err)

	// Not JSON.
	c, _, err = websocket.Dial(ctx, hub.URL(), nil)
	require.NoError(t, err)
	_ = c.Write(ctx, websocket.MessageBinary, []byte("garbage"))
	_, msg, err := c.Read(ctx)
	require.NoError(t, err)
	assert.Contains(t, string(msg), CodeBadHandshake)

	// Browsers (Origin header) cannot open a tunnel.
	_, resp, err := websocket.Dial(ctx, hub.URL(), &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://evil.example"}}})
	assert.Error(t, err)
	if resp != nil {
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	}
	select {
	case <-hub.Sessions:
		t.Fatal("session created")
	default:
	}
}

func TestDialRejectsInsecureAndOddSchemes(t *testing.T) {
	h := &LocalHandler{BaseURL: "http://127.0.0.1:1", AllowPath: allowAll}
	for _, u := range []string{"ws://hub.example/tunnel", "http://hub.example/tunnel", "file:///etc/passwd", "ftp://x", "://bad", "wss:///nohost"} {
		_, err := Dial(context.Background(), u, "t", ClientOptions{Handler: h})
		assert.Error(t, err, u)
	}
}

func TestLocalHandlerGuards(t *testing.T) {
	hub := newHub(t, fastCfg())
	var hits atomic.Int32
	sess, _ := connect(t, hub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/api/redir" {
			http.Redirect(w, r, "http://evil.example/steal", http.StatusFound)
			return
		}
		if r.URL.Path == "/api/redir-local" {
			http.Redirect(w, r, "http://127.0.0.1:9/api/x?y=1", http.StatusFound)
			return
		}
		w.Write([]byte("ok"))
	}), func(l *LocalHandler) {
		l.AllowPath = func(_, p string) bool { return !strings.HasPrefix(p, "/api/admin") }
	})

	bad := []string{
		"/", "/etc/passwd", "/admin", "/api", "/apix/x", "/api/../etc", "/api/%2e%2e/x", "/api/%2E%2E/x",
		"/api/a%2fb", "/api/a%2Fb", "/api/%252e%252e/x", "/api/a\\b", "/api//x", "/api/./x", "/api/x;y",
		"/api/x%00", "/api/x%0d%0aHost:evil", "/api/%c0%ae%c0%ae/x", "//evil.example/api/x",
		"/api/http://evil.example/", "/api/admin/secret", "/ws/x", // /ws/ is not an http path
	}
	for _, p := range bad {
		req, _ := http.NewRequest("GET", "http://x/", nil)
		req.URL.Path, req.URL.RawPath = p, p
		resp, err := sess.RoundTrip(req)
		if err == nil {
			resp.Body.Close()
		}
		var se *StreamError
		if assert.ErrorAs(t, err, &se, p) {
			assert.Contains(t, []string{CodePathNotAllowed, CodeBadRequest}, se.Code, p)
		}
	}
	assert.Zero(t, hits.Load(), "no rejected request reached the local API")

	// Method allowlist.
	req, _ := http.NewRequest("TRACE", "http://x/api/ok", nil)
	_, err := sess.RoundTrip(req)
	assert.Error(t, err)
	assert.Zero(t, hits.Load())

	// Redirects are relayed, never followed; off-host Location is dropped,
	// loopback Location is made relative.
	r := do(t, sess, "GET", "/api/redir", "", nil, nil)
	assert.Equal(t, http.StatusFound, r.StatusCode)
	assert.Empty(t, r.Header.Get("Location"))
	r.Body.Close()
	r = do(t, sess, "GET", "/api/redir-local", "", nil, nil)
	assert.Equal(t, "/api/x?y=1", r.Header.Get("Location"))
	r.Body.Close()
	assert.Equal(t, int32(2), hits.Load())

	// WebSocket paths get the same treatment.
	_, err = sess.DialWS(context.Background(), "/api/admin/ws", "", nil)
	assert.Error(t, err)
	_, err = sess.DialWS(context.Background(), "/ws/%2e%2e/x", "", nil)
	assert.Error(t, err)
}

func TestValidatePath(t *testing.T) {
	ok := []string{"/api/", "/api/agents", "/api/agents/", "/api/a/b/c", "/api/files/a%20b.txt", "/api/x-y_z.json", "/ws/terminal/abc", "/api/caf%C3%A9"}
	for _, p := range ok {
		assert.NoError(t, ValidatePath(p), p)
	}
	bad := []string{
		"", "api/x", "/", "/apis", "/API/x", "http://h/api/x", "//h/api/x", "/api/../x", "/api/..", "/api/.", "/api/%2e", "/api/%2e%2e",
		"/api/%2E%2e/x", "/api/.%2e/x", "/api/a%2fb", "/api/a%5cb", "/api/a\\b", "/api/%252e", "/api/%25", "/api//x", "/api/x//",
		"/api/x;a=b", "/api/x%3Bb", "/api/x\x00", "/api/x%00", "/api/x\r\ny", "/api/x%0D%0A", "/api/x%0a", "/api/x?y", "/api/x#y",
		"/api/x y", "/api/x\ty", "/api/%c0%af", "/api/%e0%80%af", "/api/\xff", "/api/é", "/api/%zz", "/api/%", "/api/@evil", "/api/" + strings.Repeat("a", 5000),
	}
	for _, p := range bad {
		if p == "/api/@evil" {
			continue // '@' is an ordinary path character, not a host part
		}
		assert.Error(t, ValidatePath(p), "%q", p)
	}
}

func TestLoopbackClient(t *testing.T) {
	for _, u := range []string{"http://example.com", "http://10.0.0.5:8420", "http://192.168.1.2", "ftp://127.0.0.1", "http://[2001:db8::1]:80", "127.0.0.1:8420", ""} {
		_, _, err := NewLoopbackClient(u)
		assert.Error(t, err, u)
	}
	for _, u := range []string{"http://127.0.0.1:8420", "http://localhost:8420", "http://[::1]:8420"} {
		_, _, err := NewLoopbackClient(u)
		assert.NoError(t, err, u)
	}
	// Requests addressed elsewhere still land on the pinned loopback address.
	var gotHost string
	ls := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { gotHost = r.Host }))
	defer ls.Close()
	cl, _, err := NewLoopbackClient(ls.URL)
	require.NoError(t, err)
	resp, err := cl.Get("http://evil.example/anything")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, "evil.example", gotHost, "the request reached the local server, not evil.example")
}

func TestBackoffDelay(t *testing.T) {
	b := Backoff{}
	for attempt, base := range []time.Duration{1, 2, 4, 8, 16, 32, 60, 60, 60} {
		base *= time.Second
		for i := 0; i < 50; i++ {
			d := b.delay(attempt)
			assert.GreaterOrEqual(t, d, time.Duration(float64(base)*0.75), attempt)
			assert.LessOrEqual(t, d, time.Duration(float64(base)*1.25), attempt)
		}
	}
	seen := map[time.Duration]bool{}
	for i := 0; i < 20; i++ {
		seen[b.delay(3)] = true
	}
	assert.Greater(t, len(seen), 1, "jitter")
}

func TestChunkFraming(t *testing.T) {
	var buf strings.Builder
	cw := &chunkWriter{w: &buf}
	big := strings.Repeat("z", maxChunk*2+5)
	cw.Write([]byte(big))
	cw.Close()
	b, err := io.ReadAll(&chunkReader{r: strings.NewReader(buf.String())})
	require.NoError(t, err)
	assert.Equal(t, big, string(b))

	// Missing terminator is an error, not EOF.
	trunc := buf.String()[:buf.Len()-4]
	_, err = io.ReadAll(&chunkReader{r: strings.NewReader(trunc)})
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	// Cut inside a chunk.
	_, err = io.ReadAll(&chunkReader{r: strings.NewReader(buf.String()[:100])})
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	// Size cap.
	_, err = io.ReadAll(&chunkReader{r: strings.NewReader(buf.String()), limit: 1000})
	assert.ErrorIs(t, err, errBodyTooLarge)
}

func TestHeaderLimits(t *testing.T) {
	good := `{"kind":"http","method":"GET","path":"/api/x","headers":{"A":["b"]}}`
	_, err := DecodeHeader([]byte(good))
	assert.NoError(t, err)
	bad := []string{
		``, `null`, `[]`, `{`, `{"kind":"tcp","path":"/api/x"}`, `{"kind":"http","path":"/api/x"}`,
		`{"kind":"http","method":"GET\r\n","path":"/api/x"}`, `{"kind":"http","method":"GET","path":"/api/x\r\nHost: evil"}`,
		`{"kind":"http","method":"GET","path":"/api/x","headers":{"A\r\nB":["c"]}}`,
		`{"kind":"http","method":"GET","path":"/api/x","headers":{"A":["b\nc"]}}`,
		`{"kind":"http","method":"GET","path":"/api/x","content_length":-5}`,
		`{"kind":"ws","method":"POST","path":"/ws/x"}`,
		`{"kind":"http","method":"GET","path":"` + strings.Repeat("a", 5000) + `"}`,
		`{"kind":"http","method":"GET","path":"/api/x","headers":{"A":["` + strings.Repeat("a", 9000) + `"]}}`,
		strings.Repeat(" ", 70<<10) + good,
	}
	for _, b := range bad {
		_, err := DecodeHeader([]byte(b))
		assert.Error(t, err, b[:min(len(b), 60)])
	}
	many := map[string][]string{}
	for i := 0; i < 200; i++ {
		many[fmt.Sprintf("H%d", i)] = []string{"v"}
	}
	b, _ := json.Marshal(Header{Kind: KindHTTP, Method: "GET", Path: "/api/x", Headers: many})
	_, err = DecodeHeader(b)
	assert.Error(t, err, "too many headers")
}

func FuzzDecodeHeader(f *testing.F) {
	f.Add([]byte(`{"kind":"http","method":"GET","path":"/api/x","query":"a=b","headers":{"A":["b"]}}`))
	f.Add([]byte(`{"kind":"ws","path":"/ws/term","protocols":["coral"]}`))
	f.Add([]byte(`{"kind":"http","method":"POST","path":"/api/x\r\n","content_length":9999999999}`))
	f.Add([]byte(`{`))
	f.Add([]byte{0xff, 0xfe, 0x00})
	f.Fuzz(func(t *testing.T, b []byte) {
		h, err := DecodeHeader(b)
		if err != nil {
			return
		}
		// Whatever the decoder accepts must satisfy its own invariants.
		assert.True(t, h.Kind == KindHTTP || h.Kind == KindWS)
		assert.NotContains(t, h.Path, "\n")
		assert.NotContains(t, h.Path, "\r")
		assert.LessOrEqual(t, len(h.Headers), 128)
		// And must survive a re-encode round trip.
		out, merr := json.Marshal(h)
		require.NoError(t, merr)
		_, err = DecodeHeader(out)
		assert.NoError(t, err)
	})
}

func FuzzValidatePath(f *testing.F) {
	for _, s := range []string{"/api/x", "/api/%2e%2e/x", "/ws/a", "//x", "/api/\x00"} {
		f.Add(s)
	}
	for _, s := range []string{"/api/%ef%bc%8e%ef%bc%8e/x", "/api/%e2%80%a5/x", "/api/\uff0e\uff0e/x", "/api/%ef%bc%8f"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, p string) {
		if ValidatePath(p) != nil {
			return
		}
		assert.True(t, strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/ws/"))
		for _, bad := range []string{"//", "\\", ";", "\x00", "\r", "\n", "?", "#", " "} {
			assert.NotContains(t, p, bad)
		}
		for _, seg := range strings.Split(p, "/") {
			assert.NotContains(t, []string{".", ".."}, seg)
		}
		assert.NotContains(t, strings.ToLower(p), "%2e")
		assert.NotContains(t, strings.ToLower(p), "%2f")
		assert.NotContains(t, strings.ToLower(p), "%5c")
		// A router that normalises Unicode (NFKC) must not be able to turn an accepted
		// path into a traversal: characters that fold to "." "/" or "\\" are refused.
		if dec, err := url.PathUnescape(p); err == nil {
			assert.False(t, strings.ContainsAny(dec, compatDotSlash), p)
		}
	})
}

func TestNoGoroutineLeakAfterManyStreams(t *testing.T) {
	cfg := fastCfg()
	cfg.MaxStreams = 64
	hub := newHub(t, cfg)
	sess, _ := connect(t, hub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("x")) }), nil)
	for i := 0; i < 20; i++ { // warm up
		do(t, sess, "GET", "/api/x", "", nil, nil).Body.Close()
	}
	time.Sleep(200 * time.Millisecond)
	before := goroutines()
	for i := 0; i < 2000; i++ {
		resp := do(t, sess, "GET", "/api/x", "", nil, nil)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	require.Eventually(t, func() bool { return goroutines() <= before+10 }, 5*time.Second, 50*time.Millisecond,
		"goroutines before=%d after=%d", before, goroutines())
	assert.Equal(t, 0, sess.NumStreams())
}

func TestRetryLaterIsNotPermanent(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		AcceptHTTP(w, r, func(context.Context, string) (any, error) {
			if calls.Add(1) < 3 {
				return nil, ErrRetryLater
			}
			return "id", nil
		}, fastCfg())
	}))
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	_, err := Dial(context.Background(), url, "t", ClientOptions{Handler: &LocalHandler{BaseURL: "http://127.0.0.1:1", AllowPath: allowAll}, AllowInsecure: true})
	assert.ErrorIs(t, err, ErrRetryLater)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	connected := make(chan struct{}, 1)
	go Run(ctx, url, "t", ClientOptions{
		Handler: &LocalHandler{BaseURL: "http://127.0.0.1:1", AllowPath: allowAll}, AllowInsecure: true, SessionConfig: fastCfg(),
		Backoff: Backoff{Min: 10 * time.Millisecond, Max: 50 * time.Millisecond},
		OnState: func(c bool, _ error) {
			if c {
				select {
				case connected <- struct{}{}:
				default:
				}
			}
		},
	})
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("Run gave up on a retry_later refusal")
	}
}

func TestConnectorMarksRequestsAndHubCannotForgeOrRemoveTheMark(t *testing.T) {
	hub := newHub(t, fastCfg())
	var got string
	sess, _ := connect(t, hub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(ViaHeader)
	}), func(l *LocalHandler) { l.AllowPath = func(string, string) bool { return true } })
	req, _ := http.NewRequest("GET", "http://x/api/ping", nil)
	req.Header.Set(ViaHeader, "") // a hub that tries to clear it
	resp, err := sess.RoundTrip(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, "1", got)
}

// A hub must not be able to make the connector's request look like a same-origin browser
// fetch to Coral's file endpoints.
func TestHubCannotForgeBrowserEvidence(t *testing.T) {
	hub := newHub(t, fastCfg())
	var got http.Header
	sess, _ := connect(t, hub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
	}), func(l *LocalHandler) { l.AllowPath = func(string, string) bool { return true } })
	req, _ := http.NewRequest("GET", "http://x/api/ping", nil)
	for k, v := range map[string]string{
		"Sec-Fetch-Site": "same-origin", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty",
		"Origin": "http://127.0.0.1", "Referer": "http://127.0.0.1/", "Host": "localhost", "Authorization": "Bearer x",
	} {
		req.Header.Set(k, v)
	}
	resp, err := sess.RoundTrip(req)
	require.NoError(t, err)
	resp.Body.Close()
	for _, h := range []string{"Sec-Fetch-Site", "Sec-Fetch-Mode", "Sec-Fetch-Dest", "Origin", "Referer", "Authorization"} {
		assert.Empty(t, got.Get(h), h)
	}
	assert.Equal(t, "1", got.Get(ViaHeader))
}
