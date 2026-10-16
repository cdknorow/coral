package httputil

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3724: /api/ JSON replies of 1 KB or more are gzipped for a client that accepts gzip; nothing
// else changes.

func jsonBody(n int) string {
	return `{"messages":"` + strings.Repeat("board message text ", n/19+1)[:n] + `"}`
}

type gzipCase struct {
	path, accept, method, ctype, enc string
	status                           int
	body                             string
	writes                           int // the body in this many writes
	reqHeader                        map[string]string
}

func serveGzip(t *testing.T, c gzipCase) *httptest.ResponseRecorder {
	t.Helper()
	h := GzipAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c.ctype != "" {
			w.Header().Set("Content-Type", c.ctype)
		}
		if c.enc != "" {
			w.Header().Set("Content-Encoding", c.enc)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(c.body)))
		if c.status != 0 {
			w.WriteHeader(c.status)
		}
		n := max(c.writes, 1)
		step := len(c.body)/n + 1
		for b := c.body; len(b) > 0; {
			k := min(step, len(b))
			_, _ = w.Write([]byte(b[:k]))
			b = b[k:]
		}
	}))
	method := c.method
	if method == "" {
		method = http.MethodGet
	}
	r := httptest.NewRequest(method, c.path, nil)
	if c.accept != "" {
		r.Header.Set("Accept-Encoding", c.accept)
	}
	for k, v := range c.reqHeader {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	require.NoError(t, err)
	out, err := io.ReadAll(zr)
	require.NoError(t, err)
	return string(out)
}

func TestGzipAPICompressesJSONFrom1KB(t *testing.T) {
	for _, c := range []gzipCase{
		{path: "/api/board/x/messages/all", accept: "gzip, deflate", ctype: "application/json", body: jsonBody(16 << 10)},
		{path: "/api/x", accept: "gzip", ctype: "application/json; charset=utf-8", body: jsonBody(GzipAPIMinBytes), writes: 40},
		{path: "/api/x", accept: "*", ctype: "application/problem+json", status: 404, body: jsonBody(4 << 10)},
	} {
		rec := serveGzip(t, c)
		assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"), c.path)
		assert.Contains(t, rec.Header().Values("Vary"), "Accept-Encoding")
		assert.Empty(t, rec.Header().Get("Content-Length"), "the plain length is dropped")
		assert.Equal(t, max(c.status, 200), rec.Code)
		assert.Equal(t, c.body, gunzip(t, rec.Body.Bytes()))
		assert.Less(t, rec.Body.Len(), len(c.body))
	}
}

func TestGzipAPILeavesTheRestAlone(t *testing.T) {
	big := jsonBody(16 << 10)
	for name, c := range map[string]gzipCase{
		"under 1 KB":          {path: "/api/x", accept: "gzip", ctype: "application/json", body: jsonBody(GzipAPIMinBytes - 40)},
		"no Accept-Encoding":  {path: "/api/x", ctype: "application/json", body: big},
		"gzip refused":        {path: "/api/x", accept: "gzip;q=0, *", ctype: "application/json", body: big},
		"not /api/":           {path: "/static/app.js", accept: "gzip", ctype: "application/json", body: big},
		"not JSON":            {path: "/api/x", accept: "gzip", ctype: "text/plain", body: big},
		"an event stream":     {path: "/api/events", accept: "gzip", ctype: "text/event-stream", body: big},
		"an image":            {path: "/api/artifacts/a", accept: "gzip", ctype: "image/png", body: big},
		"its own encoding":    {path: "/api/x", accept: "gzip", ctype: "application/json", enc: "br", body: big},
		"HEAD":                {path: "/api/x", accept: "gzip", method: http.MethodHead, ctype: "application/json", body: big},
		"a Range request":     {path: "/api/x", accept: "gzip", ctype: "application/json", body: big, reqHeader: map[string]string{"Range": "bytes=0-9"}},
		"a WebSocket upgrade": {path: "/api/x", accept: "gzip", ctype: "application/json", body: big, reqHeader: map[string]string{"Upgrade": "websocket"}},
		"a 304, no body":      {path: "/api/x", accept: "gzip", ctype: "application/json", status: 304},
		"an empty 200":        {path: "/api/x", accept: "gzip", ctype: "application/json"},
	} {
		rec := serveGzip(t, c)
		assert.Equal(t, c.enc, rec.Header().Get("Content-Encoding"), name)
		assert.Equal(t, c.body, rec.Body.String(), name)
		assert.Equal(t, max(c.status, 200), rec.Code, name)
		if c.body != "" && c.enc == "" {
			assert.Equal(t, strconv.Itoa(len(c.body)), rec.Header().Get("Content-Length"), "%s: the handler's length stands", name)
		}
	}
}

// A handler that gzips its own reply (WriteJSONCompressed) is passed through: compressed once.
func TestGzipAPIAndWriteJSONCompressed(t *testing.T) {
	v := map[string]string{"messages": strings.Repeat("x", 20<<10)}
	h := GzipAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { WriteJSONCompressed(w, r, 200, v) }))
	r := httptest.NewRequest(http.MethodGet, "/api/sessions/live/a/chat", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
	assert.Contains(t, gunzip(t, rec.Body.Bytes()), `"messages"`)
}

// A reply flushed before it reaches the threshold is a stream: sent plain, each flush reaching the
// client. One that reached it first is compressed, and a flush pushes the compressed bytes out.
func TestGzipAPIFlush(t *testing.T) {
	srv := httptest.NewServer(GzipAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"a":1}`))
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte(strings.Repeat(" ", 4<<10) + `}`))
	})))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/stream", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := (&http.Transport{DisableCompression: true}).RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Empty(t, resp.Header.Get("Content-Encoding"), "flushed under the threshold: plain")
	assert.True(t, strings.HasPrefix(string(body), `{"a":1}`))

	rec := httptest.NewRecorder()
	GzipAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(jsonBody(2 << 10)))
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte(" "))
	})).ServeHTTP(rec, func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/x", nil)
		r.Header.Set("Accept-Encoding", "gzip")
		return r
	}())
	assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
	assert.True(t, rec.Flushed)
	assert.Equal(t, jsonBody(2<<10)+" ", gunzip(t, rec.Body.Bytes()))
}

// The WebSocket upgrade and anything off /api/ get the server's own writer, so Hijack works.
func TestGzipAPIPassesTheWriterThrough(t *testing.T) {
	for _, r := range []*http.Request{
		func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/api/ws", nil)
			r.Header.Set("Upgrade", "websocket")
			r.Header.Set("Accept-Encoding", "gzip")
			return r
		}(),
		func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/ws/terminal/a", nil)
			r.Header.Set("Accept-Encoding", "gzip")
			return r
		}(),
	} {
		var wrapped bool
		GzipAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, wrapped = w.(*gzipAPIWriter)
		})).ServeHTTP(httptest.NewRecorder(), r)
		assert.False(t, wrapped, r.URL.Path)
	}
}
