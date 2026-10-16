package server

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/server/routes"
	"github.com/cdknorow/coral/internal/store/storetest"
)

func bodyLimitServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	cfg := newE2EConfig(t, t.TempDir())
	db, err := storetest.Open(t, cfg.DBPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	stack := newTermStack(t)
	srv := newTestServer(t, cfg, db, stack.backend, stack.terminal)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	return srv, ts
}

// onlyReader hides a body's length, so it is sent chunked.
type onlyReader struct{ io.Reader }

// #3998 (Security's #3994 finding 1): a 64 MiB body is refused with 413, declared or streamed,
// without the server taking that memory; the upload route still takes a 19 MiB file.
func TestOversizedBodiesAreRefused(t *testing.T) {
	srv, ts := bodyLimitServer(t)
	key := srv.keyStore.Key()
	huge := bytes.Repeat([]byte("x"), 64<<20)
	post := func(path string, body io.Reader, size int64) *http.Response {
		t.Helper()
		req, err := http.NewRequest("POST", ts.URL+path, body)
		require.NoError(t, err)
		req.ContentLength = size
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for _, p := range []string{"/api/board/team/messages", "/api/sessions/live/x/events", "/api/tags"} {
		// Declared: refused before a byte is read.
		body := append([]byte(`{"content":"`), huge...)
		assert.Equal(t, http.StatusRequestEntityTooLarge, post(p, bytes.NewReader(body), int64(len(body))).StatusCode, p)
	}
	// Streamed (no Content-Length): cut at the limit; the handler's decode error is sent as 413.
	streamed := io.MultiReader(strings.NewReader(`{"name":"`), bytes.NewReader(huge), strings.NewReader(`"}`))
	assert.Equal(t, http.StatusRequestEntityTooLarge, post("/api/tags", onlyReader{streamed}, -1).StatusCode)
	runtime.GC()
	runtime.ReadMemStats(&after)
	grew := int64(after.HeapInuse) - int64(before.HeapInuse)
	assert.Less(t, grew, int64(32<<20), "the server didn't take the bodies' memory (the test holds one 64 MiB buffer itself)")

	// A normal body still works; the upload route takes 19 MiB.
	assert.NotEqual(t, http.StatusRequestEntityTooLarge, post("/api/tags", strings.NewReader(`{"name":"ok","color":"#ffffff"}`), -1).StatusCode)
	var mp bytes.Buffer
	mw := multipart.NewWriter(&mp)
	fw, err := mw.CreateFormFile("file", "big.png")
	require.NoError(t, err)
	_, _ = fw.Write(bytes.Repeat([]byte{0}, 19<<20))
	require.NoError(t, mw.Close())
	req, _ := http.NewRequest("POST", ts.URL+"/api/upload", &mp)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.NotEqual(t, http.StatusRequestEntityTooLarge, resp.StatusCode, "the upload route keeps its 20 MB")
}

// Every route with a raised limit is a route the server serves (so the table can't go stale).
func TestBodyLimitTableNamesServedRoutes(t *testing.T) {
	srv, _ := bodyLimitServer(t)
	served := map[string]bool{}
	require.NoError(t, chi.Walk(srv.Router(), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		served[method+" "+route] = true
		return nil
	}))
	for _, k := range routes.BodyLimitKeys() {
		assert.True(t, served[k], "bodyLimits names %q, which isn't served", k)
	}
}
