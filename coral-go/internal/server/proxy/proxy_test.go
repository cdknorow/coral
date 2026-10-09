package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
)

const remoteKey = "remote-secret-key"

type fakeResolver struct {
	mu      sync.Mutex
	targets map[string]Target
	errs    map[string]error
	marks   []string // "id:status"
}

func (f *fakeResolver) Resolve(_ context.Context, id string) (Target, error) {
	if err, ok := f.errs[id]; ok {
		return Target{}, err
	}
	t, ok := f.targets[id]
	if !ok {
		return Target{}, ErrUnknownServer
	}
	return t, nil
}

func (f *fakeResolver) MarkStatus(_ context.Context, id, status, _ string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marks = append(f.marks, id+":"+status)
}

func (f *fakeResolver) hasMark(m string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.marks {
		if x == m {
			return true
		}
	}
	return false
}

func newHub(res Resolver) http.Handler {
	r := chi.NewRouter()
	r.Handle("/api/remote/{server}/*", New(res))
	return r
}

// keyed wraps a remote handler so it demands the API key from every caller,
// including loopback ones: the proxy cannot succeed through a localhost bypass.
func keyed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+remoteKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// setup starts a key-requiring remote and a hub with server "a" pointing at it.
func setup(t *testing.T, remote http.Handler, mod func(*Target)) (*fakeResolver, *httptest.Server, *httptest.Server) {
	t.Helper()
	rem := httptest.NewServer(keyed(remote))
	t.Cleanup(rem.Close)
	tg := Target{URL: rem.URL, APIKey: remoteKey, AllowPrivate: true}
	if mod != nil {
		mod(&tg)
	}
	res := &fakeResolver{targets: map[string]Target{"a": tg}, errs: map[string]error{}}
	hub := httptest.NewServer(newHub(res))
	t.Cleanup(hub.Close)
	return res, hub, rem
}

func TestKeyInjectionAndHeaderStripping(t *testing.T) {
	var seen http.Header
	var seenQuery, seenPath string
	_, hub, _ := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, seenQuery, seenPath = r.Header.Clone(), r.URL.RawQuery, r.URL.Path
		w.Header().Set("Set-Cookie", "evil=1")
		w.Header().Set("X-Custom", "yes")
		w.WriteHeader(http.StatusTeapot)
		io.WriteString(w, `{"ok":true}`)
	}), nil)

	req, _ := http.NewRequest("POST", hub.URL+"/api/remote/a/api/sessions/launch?api_key=browserkey&x=1", strings.NewReader("body"))
	req.Header.Set("Authorization", "Bearer browser-token")
	req.Header.Set("Cookie", "session=abc")
	req.Header.Set("Origin", "http://hub")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)

	assert.Equal(t, http.StatusTeapot, resp.StatusCode)
	assert.Equal(t, `{"ok":true}`, string(b))
	assert.Equal(t, "yes", resp.Header.Get("X-Custom"))
	assert.Empty(t, resp.Header.Get("Set-Cookie"))
	assert.Equal(t, "Bearer "+remoteKey, seen.Get("Authorization"))
	assert.Empty(t, seen.Get("Cookie"))
	assert.Empty(t, seen.Get("Origin"))
	assert.Equal(t, "x=1", seenQuery)
	assert.Equal(t, "/api/sessions/launch", seenPath)
	assert.NotContains(t, string(b), remoteKey)
}

// The remote rejects loopback callers without the key, so a working proxy proves
// the key (not a localhost auth bypass) is what authenticates the request.
func TestProxyWorksBecauseOfKeyNotLocalhostBypass(t *testing.T) {
	var mu sync.Mutex
	var seenAuth []string
	_, hub, rem := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenAuth = append(seenAuth, r.Header.Get("Authorization"))
		mu.Unlock()
		io.WriteString(w, "ok")
	}), nil)

	resp, err := http.Get(rem.URL + "/api/health") // loopback, no key
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	resp, err = http.Get(hub.URL + "/api/remote/a/api/health")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"Bearer " + remoteKey}, seenAuth)
}

func TestStreamingSSE(t *testing.T) {
	release := make(chan struct{})
	_, hub, _ := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		io.WriteString(w, "data: one\n\n")
		fl.Flush()
		<-release
		io.WriteString(w, "data: two\n\n")
		fl.Flush()
	}), nil)

	req, _ := http.NewRequest("GET", hub.URL+"/api/remote/a/api/events", nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	br := bufio.NewReader(resp.Body)
	line, err := br.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "data: one\n", line) // arrived while the remote is still blocked
	close(release)
	br.ReadString('\n')
	line, err = br.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "data: two\n", line)
}

func TestPathSafety(t *testing.T) {
	var mu sync.Mutex
	called := false
	_, hub, _ := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		called = true
		mu.Unlock()
	}), nil)
	cases := []struct {
		path string
		want int
	}{
		{"/api/remote/a/api/../etc/passwd", 400},
		{"/api/remote/a/api/%2e%2e/secret", 400},
		{"/api/remote/a/api/%2E%2E/secret", 400},
		{"/api/remote/a/api/foo%2f..%2fbar", 400},
		{"/api/remote/a/api/foo%5c..%5cbar", 400},
		{"/api/remote/a/api/%252e%252e/x", 400},
		{"/api/remote/a/api//x", 400},
		{"/api/remote/a/api/./x", 400},
		{"/api/remote/a/static/app.js", 403},
		{"/api/remote/a/ws/terminal/x", 403}, // ws path without an upgrade
		{"/api/remote/a/apix/foo", 403},
	}
	for _, c := range cases {
		conn, err := net.Dial("tcp", strings.TrimPrefix(hub.URL, "http://"))
		require.NoError(t, err)
		io.WriteString(conn, "GET "+c.path+" HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		require.NoError(t, err, c.path)
		resp.Body.Close()
		conn.Close()
		assert.Equal(t, c.want, resp.StatusCode, c.path)
	}
	mu.Lock()
	defer mu.Unlock()
	assert.False(t, called, "remote must never be reached")
}

func TestSanitizePath(t *testing.T) {
	_, _, err := SanitizePath("/api/sessions/live/my%20agent/chat")
	assert.NoError(t, err)
	_, _, err = SanitizePath("/api/a/../b")
	assert.Error(t, err)
	_, _, err = SanitizePath("api/a")
	assert.Error(t, err)
}

func TestUnknownServer(t *testing.T) {
	_, hub, _ := setup(t, http.NotFoundHandler(), nil)
	resp, err := http.Get(hub.URL + "/api/remote/nope/api/health")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, 404, resp.StatusCode)
}

func TestUnreachable(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close() // nothing listening
	res := &fakeResolver{targets: map[string]Target{"dead": {URL: "http://" + addr, APIKey: "k", AllowPrivate: true}}}
	hub := httptest.NewServer(newHub(res))
	defer hub.Close()

	resp, err := http.Get(hub.URL + "/api/remote/dead/api/health")
	require.NoError(t, err)
	defer resp.Body.Close()
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	assert.Equal(t, 502, resp.StatusCode)
	assert.Equal(t, map[string]string{"error": "remote unreachable", "server": "dead"}, body)
	assert.True(t, res.hasMark("dead:unreachable"))
}

func TestUpstream401MapsTo502(t *testing.T) {
	res, hub, _ := setup(t, http.NotFoundHandler(), func(tg *Target) { tg.APIKey = "wrong" })
	resp, err := http.Get(hub.URL + "/api/remote/a/api/health")
	require.NoError(t, err)
	defer resp.Body.Close()
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	assert.Equal(t, 502, resp.StatusCode)
	assert.Equal(t, map[string]string{"error": "remote rejected API key", "server": "a"}, body)
	assert.True(t, res.hasMark("a:unauthorized"))
}

func TestKeyUnreadable(t *testing.T) {
	res := &fakeResolver{errs: map[string]error{"k": ErrKeyUnreadable}}
	hub := httptest.NewServer(newHub(res))
	defer hub.Close()
	resp, err := http.Get(hub.URL + "/api/remote/k/api/health")
	require.NoError(t, err)
	defer resp.Body.Close()
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	assert.Equal(t, 502, resp.StatusCode)
	assert.Equal(t, "remote key unreadable", body["error"])
	assert.True(t, res.hasMark("k:key_unreadable"))
}

func TestSSRFBlocksPrivateUnlessAllowed(t *testing.T) {
	var mu sync.Mutex
	hit := false
	_, hub, _ := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hit = true
		mu.Unlock()
		io.WriteString(w, "ok")
	}), func(tg *Target) { tg.AllowPrivate = false })
	resp, err := http.Get(hub.URL + "/api/remote/a/api/health")
	require.NoError(t, err)
	defer resp.Body.Close()
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	assert.Equal(t, 502, resp.StatusCode)
	assert.Equal(t, "remote address not allowed", body["error"])
	mu.Lock()
	assert.False(t, hit)
	mu.Unlock()

	// allow_private succeeds against the same kind of loopback remote.
	_, hub2, _ := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }), nil)
	resp2, err := http.Get(hub2.URL + "/api/remote/a/api/health")
	require.NoError(t, err)
	resp2.Body.Close()
	assert.Equal(t, 200, resp2.StatusCode)
}

// Dial-time guard: even when pre-validation is bypassed (as with DNS rebinding,
// where the name resolved to a public IP earlier), the dialer refuses private IPs.
func TestDialTimeGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL, nil)
	_, err := transport(false).RoundTrip(req)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrBlocked) || strings.Contains(err.Error(), ErrBlocked.Error()), err.Error())
	resp, err := transport(true).RoundTrip(req)
	require.NoError(t, err)
	resp.Body.Close()
}

func TestGetHelper(t *testing.T) {
	rem := httptest.NewServer(keyed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "live") })))
	defer rem.Close()
	ctx := context.Background()
	ok := Target{URL: rem.URL, APIKey: remoteKey, AllowPrivate: true}

	resp, err := Get(ctx, ok, "/api/sessions/live", "api_key=zzz")
	require.NoError(t, err)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, "live", string(b))

	_, err = Get(ctx, Target{URL: rem.URL, APIKey: "bad", AllowPrivate: true}, "/api/x", "")
	assert.ErrorIs(t, err, ErrRemoteRejected)
	_, err = Get(ctx, Target{URL: rem.URL, APIKey: remoteKey}, "/api/x", "")
	assert.ErrorIs(t, err, ErrBlocked)
	_, err = Get(ctx, ok, "/static/x", "")
	assert.ErrorIs(t, err, ErrPathNotAllowed)
	_, err = Get(ctx, ok, "/api/../x", "")
	assert.ErrorIs(t, err, ErrInvalidPath)
}

// ── WebSocket ───────────────────────────────────────────────────────

func closeReasonOf(err error) string {
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return ce.Reason
	}
	return ""
}

func wsHub(t *testing.T, handler func(c *websocket.Conn, r *http.Request), key string) (*httptest.Server, *fakeResolver) {
	t.Helper()
	rem := httptest.NewServer(keyed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		handler(c, r)
	})))
	t.Cleanup(rem.Close)
	res := &fakeResolver{targets: map[string]Target{"a": {URL: rem.URL, APIKey: key, AllowPrivate: true}}}
	hub := httptest.NewServer(newHub(res))
	t.Cleanup(hub.Close)
	return hub, res
}

func wsDial(t *testing.T, hub *httptest.Server, path string) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return websocket.Dial(ctx, "ws"+strings.TrimPrefix(hub.URL, "http")+path, nil)
}

func TestWebSocketRoundTripAndClientClose(t *testing.T) {
	type closed struct {
		code   websocket.StatusCode
		reason string
	}
	got := make(chan closed, 1)
	queryCh := make(chan string, 1)
	hub, _ := wsHub(t, func(c *websocket.Conn, r *http.Request) {
		queryCh <- r.URL.RawQuery
		ctx := context.Background()
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				got <- closed{websocket.CloseStatus(err), closeReasonOf(err)}
				return
			}
			c.Write(ctx, typ, append([]byte("echo:"), data...))
		}
	}, remoteKey)

	c, _, err := wsDial(t, hub, "/api/remote/a/ws/terminal/agent1?api_key=browser&rows=24")
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, c.Write(ctx, websocket.MessageText, []byte("hi")))
	typ, data, err := c.Read(ctx)
	require.NoError(t, err)
	assert.Equal(t, websocket.MessageText, typ)
	assert.Equal(t, "echo:hi", string(data))
	require.NoError(t, c.Write(ctx, websocket.MessageBinary, []byte{1, 2}))
	typ, data, _ = c.Read(ctx)
	assert.Equal(t, websocket.MessageBinary, typ)
	assert.Equal(t, []byte("echo:\x01\x02"), data)

	c.Close(4001, "bye")
	select {
	case m := <-got:
		assert.Equal(t, websocket.StatusCode(4001), m.code)
		assert.Equal(t, "bye", m.reason)
	case <-time.After(5 * time.Second):
		t.Fatal("remote did not observe close")
	}
	assert.Equal(t, "rows=24", <-queryCh)
}

func TestWebSocketRemoteCloseCodePropagates(t *testing.T) {
	hub, _ := wsHub(t, func(c *websocket.Conn, r *http.Request) {
		c.Write(context.Background(), websocket.MessageText, []byte("first"))
		c.Close(4002, "session ended")
	}, remoteKey)
	c, _, err := wsDial(t, hub, "/api/remote/a/ws/x")
	require.NoError(t, err)
	_, data, err := c.Read(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "first", string(data))
	_, _, err = c.Read(context.Background())
	require.Error(t, err)
	assert.Equal(t, websocket.StatusCode(4002), websocket.CloseStatus(err))
	assert.Equal(t, "session ended", closeReasonOf(err))
}

func TestWebSocketRemoteDropsMidStream(t *testing.T) {
	hub, _ := wsHub(t, func(c *websocket.Conn, r *http.Request) {
		c.Write(context.Background(), websocket.MessageText, []byte("before drop"))
		time.Sleep(50 * time.Millisecond)
		c.CloseNow() // abrupt: no close frame
	}, remoteKey)

	c, _, err := wsDial(t, hub, "/api/remote/a/ws/x")
	require.NoError(t, err)
	_, data, err := c.Read(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "before drop", string(data))
	_, _, err = c.Read(context.Background())
	require.Error(t, err)
	assert.Equal(t, websocket.StatusInternalError, websocket.CloseStatus(err))
}

func TestWebSocketBrowserDropClosesUpstream(t *testing.T) {
	done := make(chan struct{})
	hub, _ := wsHub(t, func(c *websocket.Conn, r *http.Request) {
		if _, _, err := c.Read(context.Background()); err != nil {
			close(done)
		}
	}, remoteKey)
	c, _, err := wsDial(t, hub, "/api/remote/a/ws/x")
	require.NoError(t, err)
	c.CloseNow()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream not closed after browser dropped")
	}
}

func TestWebSocketErrors(t *testing.T) {
	// Wrong key -> 502 at handshake, marked unauthorized.
	hub, res := wsHub(t, func(c *websocket.Conn, r *http.Request) {}, "wrong")
	_, resp, err := wsDial(t, hub, "/api/remote/a/ws/x")
	require.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, 502, resp.StatusCode)
	assert.True(t, res.hasMark("a:unauthorized"))

	_, resp, err = wsDial(t, hub, "/api/remote/zzz/ws/x")
	require.Error(t, err)
	assert.Equal(t, 404, resp.StatusCode)

	_, resp, err = wsDial(t, hub, "/api/remote/a/ws/../etc")
	require.Error(t, err)
	assert.Equal(t, 400, resp.StatusCode)
	_, resp, err = wsDial(t, hub, "/api/remote/a/static/x")
	require.Error(t, err)
	assert.Equal(t, 403, resp.StatusCode)
}

func TestWebSocketSSRFBlocked(t *testing.T) {
	rem := httptest.NewServer(keyed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("remote must not be reached")
	})))
	defer rem.Close()
	res := &fakeResolver{targets: map[string]Target{"a": {URL: rem.URL, APIKey: remoteKey}}}
	hub := httptest.NewServer(newHub(res))
	defer hub.Close()
	_, resp, err := wsDial(t, hub, "/api/remote/a/ws/x")
	require.Error(t, err)
	assert.Equal(t, 502, resp.StatusCode)

	_, err = DialWebSocket(context.Background(), Target{URL: rem.URL, APIKey: remoteKey}, "/ws/x", "", nil)
	assert.ErrorIs(t, err, ErrBlocked)
}

func TestDialWebSocketHelper(t *testing.T) {
	rem := httptest.NewServer(keyed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		c.Write(context.Background(), websocket.MessageText, []byte("feed"))
		c.Close(websocket.StatusNormalClosure, "")
	})))
	defer rem.Close()
	c, err := DialWebSocket(context.Background(), Target{URL: rem.URL, APIKey: remoteKey, AllowPrivate: true}, "/ws/coral", "", nil)
	require.NoError(t, err)
	_, data, err := c.Read(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "feed", string(data))
	_, err = DialWebSocket(context.Background(), Target{URL: rem.URL, APIKey: "bad", AllowPrivate: true}, "/ws/coral", "", nil)
	assert.ErrorIs(t, err, ErrRemoteRejected)
}
