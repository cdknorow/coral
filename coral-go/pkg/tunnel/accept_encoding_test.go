package tunnel

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// concretePath fills a route pattern's parameters with values its matcher accepts.
func concretePath(pattern string) string {
	segs := strings.Split(pattern[1:], "/")
	for i, s := range segs {
		switch {
		case s == "{hex64}":
			segs[i] = strings.Repeat("ab", 32)
		case s == "{uiid}":
			segs[i] = "panel-1"
		case strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}"):
			segs[i] = "p1"
		}
	}
	return "/" + strings.Join(segs, "/")
}

// The connector never forwards Accept-Encoding, on any route the tunnel allows,
// HTTP or WebSocket: a compressed body that mixes a secret with
// attacker-influenced text would leak through its length (BREACH). The machine
// therefore answers every tunnelled request uncompressed, whatever the hub asks.
func TestAcceptEncodingIsDroppedOnEveryRoute(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{} // "METHOD path" -> Accept-Encoding the local API received
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Method+" "+r.URL.Path] = r.Header.Get("Accept-Encoding")
		mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/ws/") {
			if c, err := websocket.Accept(w, r, nil); err == nil {
				c.Close(websocket.StatusNormalClosure, "")
			}
			return
		}
		w.Write([]byte("ok"))
	})
	hub := newHub(t, fastCfg())
	sess, _ := connect(t, hub, local, func(l *LocalHandler) {
		l.AllowRequest = AllowRequest(func() Mode { return TierLaunch })
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	gzipOnly := http.Header{"Accept-Encoding": {"gzip, deflate, br"}}
	checked := 0
	for _, r := range routeTable {
		if r.tier == TierNever {
			continue
		}
		path := concretePath(r.pattern)
		key := r.method + " " + path
		if strings.HasPrefix(path, "/ws/") {
			hs, err := sess.OpenStream(ctx, Header{Kind: KindWS, Method: http.MethodGet, Path: path, Headers: gzipOnly.Clone()})
			require.NoError(t, err, key)
			_, err = sess.readResponse(hs)
			require.NoError(t, err, key)
			hs.Close()
		} else {
			var body *strings.Reader
			if r.method != http.MethodGet {
				body = strings.NewReader("{}")
			} else {
				body = strings.NewReader("")
			}
			resp := do(t, sess, r.method, path, "", body, gzipOnly.Clone())
			resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode, key)
		}
		mu.Lock()
		ae, reached := seen[key]
		mu.Unlock()
		require.True(t, reached, "%s never reached the local API, so the check would be vacuous", key)
		assert.Empty(t, ae, "%s forwarded Accept-Encoding", key)
		checked++
	}
	assert.Greater(t, checked, 50, "the route table was iterated")
}

// Older chat history is paged with before=<absolute index> (live_chat.js sends
// it with offset for servers that predate it). The tunnel must pass it through
// to the machine; a parameter the handler does not read is still refused.
func TestChatBeforeCursorPassesTheTunnel(t *testing.T) {
	const older = "session_id=abc&agent_type=claude&limit=200&before=900&offset=105"
	assert.Equal(t, TierView, Classify(http.MethodGet, "/api/sessions/live/a/chat", older))
	assert.Equal(t, TierNever, Classify(http.MethodGet, "/api/sessions/live/a/chat", older+"&before_id=3"))

	var mu sync.Mutex
	var got string
	hub := newHub(t, fastCfg())
	sess, _ := connect(t, hub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = r.URL.RawQuery
		mu.Unlock()
		w.Write([]byte(`{"messages":[],"start":700}`))
	}), func(l *LocalHandler) {
		l.AllowRequest = AllowRequest(func() Mode { return TierView })
	})
	resp := do(t, sess, http.MethodGet, "/api/sessions/live/a/chat", older, strings.NewReader(""), nil)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, older, got, "the query reaches the machine unchanged")
}

// A Content-Encoding set by the local app is never relayed: gzip is decoded by
// the connector (and no length is claimed for the decoded body, whose size is
// not known in advance), identity passes, a bodyless response just loses the
// header, and any other or stacked encoding is answered 502 with a JSON error
// rather than relayed opaque or failed as a stream.
func TestLocalContentEncodingIsNeverRelayed(t *testing.T) {
	const page = `{"messages":["one","two"]}`
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(page))
	require.NoError(t, zw.Close())

	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/gzip", "/api/head-gzip":
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Length", strconv.Itoa(gz.Len()))
			if r.Method != http.MethodHead {
				w.Write(gz.Bytes())
			}
		case "/api/x-gzip":
			w.Header().Set("Content-Encoding", "X-GZIP")
			w.Write(gz.Bytes())
		case "/api/identity":
			w.Header().Set("Content-Encoding", "identity")
			w.Write([]byte(page))
		case "/api/br":
			w.Header().Set("Content-Encoding", "br")
			w.Write([]byte("not really brotli"))
		case "/api/stacked":
			w.Header().Add("Content-Encoding", "gzip")
			w.Header().Add("Content-Encoding", "gzip")
			w.Write(gz.Bytes())
		case "/api/corrupt":
			w.Header().Set("Content-Encoding", "gzip")
			w.Write([]byte("this is not gzip"))
		case "/api/no-content":
			w.Header().Set("Content-Encoding", "gzip")
			w.WriteHeader(http.StatusNoContent)
		}
	})
	hub := newHub(t, fastCfg())
	sess, _ := connect(t, hub, local, nil)

	get := func(method, path string) (*http.Response, string) {
		t.Helper()
		resp := do(t, sess, method, path, "", strings.NewReader(""), nil)
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err, path)
		return resp, string(b)
	}

	for _, path := range []string{"/api/gzip", "/api/x-gzip", "/api/identity"} {
		resp, body := get(http.MethodGet, path)
		assert.Equal(t, http.StatusOK, resp.StatusCode, path)
		assert.Empty(t, resp.Header.Values("Content-Encoding"), path)
		assert.Equal(t, page, body, path)
	}
	resp, _ := get(http.MethodGet, "/api/gzip")
	assert.Empty(t, resp.Header.Get(BodyLengthHeader), "the compressed length is not claimed for the decoded body")
	resp, _ = get(http.MethodGet, "/api/identity")
	assert.Equal(t, strconv.Itoa(len(page)), resp.Header.Get(BodyLengthHeader), "a plain body still declares its length")

	for _, path := range []string{"/api/br", "/api/stacked", "/api/corrupt"} {
		resp, body := get(http.MethodGet, path)
		assert.Equal(t, http.StatusBadGateway, resp.StatusCode, path)
		assert.Empty(t, resp.Header.Values("Content-Encoding"), path)
		assert.JSONEq(t, `{"error":"unsupported content encoding"}`, body, path)
	}

	resp, body := get(http.MethodHead, "/api/head-gzip")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, resp.Header.Values("Content-Encoding"))
	assert.Empty(t, body)
	resp, _ = get(http.MethodGet, "/api/no-content")
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Empty(t, resp.Header.Values("Content-Encoding"))
}
