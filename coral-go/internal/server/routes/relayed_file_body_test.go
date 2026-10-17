package routes

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store/storetest"
)

// A file another Coral sends is a binary frame of up to 8 MiB: the board's 4 MiB cap on a request body (the
// identity guards read board bodies to find the identities they name) must not apply to it, and the
// server-wide cap must allow a full frame and refuse one byte more (#4453: an end-to-end run found a
// 7.5 MiB file refused with 413 body_too_large, which the handler-level tests could not see).
func TestRelayedFileRouteIsNotCappedAsABoardRequest(t *testing.T) {
	path := "/api/board/p/external-artifacts"
	full := 4 + maxReceivedMeta + maxReceivedArtifact

	assert.True(t, isRelayedFileRoute(httptest.NewRequest("POST", path, nil)))
	for _, p := range []string{"/api/board/p/external-messages", "/api/board/p/q/external-artifacts", "/api/board//external-artifacts", "/api/agent/external-artifacts"} {
		assert.False(t, isRelayedFileRoute(httptest.NewRequest("POST", p, nil)), p)
	}
	assert.False(t, isRelayedFileRoute(httptest.NewRequest("GET", path, nil)), "only a POST is a relayed file")

	// The whole chain a server runs, down to a handler that reads the body.
	_, bh := setupBoardTestServer(t)
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	defer db.Close()
	o := NewIdentityObserver(db, bh.bs, func(*http.Request) bool { return false })
	var got int
	r := chi.NewRouter()
	r.Use(BodyLimit, CanonicalIdentityKeys, o.Middleware)
	r.Post("/api/board/{project}/external-artifacts", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		got = len(b)
		w.WriteHeader(http.StatusOK)
	})
	post := func(n int, headers map[string]string) int {
		body := bytes.Repeat([]byte{0, 1, 2, 3, '{', '"'}, n/6+1)[:n] // binary, starting like no JSON object
		req := httptest.NewRequest("POST", path, bytes.NewReader(body))
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		got = 0
		r.ServeHTTP(rec, req)
		return rec.Code
	}
	for _, n := range []int{1 << 10, maxBoardBody + 1, 7<<20 + 512<<10, full} {
		assert.Equal(t, http.StatusOK, post(n, nil), "a frame of %d bytes", n)
		assert.Equal(t, n, got, "the handler reads all %d bytes, unchanged", n)
	}
	assert.Equal(t, http.StatusRequestEntityTooLarge, post(full+1, nil), "one byte over a full frame is refused by the server-wide cap")

	// Another board route keeps the 4 MiB cap.
	req := httptest.NewRequest("POST", "/api/board/p/messages", bytes.NewReader(bytes.Repeat([]byte("x"), maxBoardBody+1)))
	rec := httptest.NewRecorder()
	h := o.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}
