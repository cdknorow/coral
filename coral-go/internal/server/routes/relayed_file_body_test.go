package routes

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/pkg/tunnel"
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

// The real handler behind the real guards (#4453, the Orchestrator's conditions): a file just over 4 MiB and
// one of the full 8 MiB are stored and answered 200; a declared length over the frame limit is 413 (the
// server-wide cap, before a byte is read); and the guards that were relaxed for the size still leave the hub
// stamp, the authenticity secret and the Hub-user refusal to the handler, at full size too.
func TestRelayedFileThroughTheRealGuards(t *testing.T) {
	e := newArtifactEnv(t)
	e.open()
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	defer db.Close()
	o := NewIdentityObserver(db, e.h.bs, func(*http.Request) bool { return false })
	r := chi.NewRouter()
	r.Use(BodyLimit, CanonicalIdentityKeys, o.Middleware)
	r.Post("/api/board/{project}/external-artifacts", e.h.PostExternalArtifact)

	send := func(body []byte, src, via, hubUser string) (int, map[string]any) {
		req := httptest.NewRequest("POST", "/api/board/beta/external-artifacts", bytes.NewReader(body))
		if src != "" {
			req.Header.Set(tunnel.HubServerHeader, src)
		}
		switch via {
		case "auth":
			req.Header.Set(tunnel.ViaHeader, cloud.ViaSecret())
		case "forged":
			req.Header.Set(tunnel.ViaHeader, "1")
		}
		if hubUser != "" {
			req.Header.Set(tunnel.HubUserHeader, hubUser)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	file := func(n int, tag string) []byte { // n bytes of text, unlike any other file
		return bytes.Repeat([]byte(tag+" line of a large text file\n"), n/(len(tag)+27)+1)[:n]
	}

	over4 := file(4<<20+4096, "over4")
	full := file(maxReceivedArtifact, "full")
	for name, data := range map[string][]byte{"over 4 MiB": over4, "the full 8 MiB": full} {
		status, out := send(frame(meta(data, name+".txt"), data), relaySrc, "auth", "")
		require.Equal(t, 200, status, "%s: %v", name, out)
		got, err := os.ReadFile(filepath.Join(e.dir, "artifacts", "objects", out["artifact_id"].(string)))
		require.NoError(t, err)
		assert.Equal(t, data, got, "%s: stored byte for byte", name)
	}

	// Over the frame limit: refused by its declared length, before a byte is read, and nothing is stored.
	stored := len(e.stored())
	status, out := send(bytes.Repeat([]byte("x"), 4+maxReceivedMeta+maxReceivedArtifact+1), relaySrc, "auth", "")
	assert.Equal(t, http.StatusRequestEntityTooLarge, status, out)
	assert.Equal(t, "body_too_large", out["code"])
	assert.Len(t, e.stored(), stored)
	// A file one byte over its own cap is the handler's refusal (the frame is within the limit).
	tooBig := file(maxReceivedArtifact+1, "toobig")
	status, out = send(frame(meta(tooBig, "big.txt"), tooBig), relaySrc, "auth", "")
	assert.Equal(t, http.StatusBadRequest, status, out)
	assert.Len(t, e.stored(), stored)

	// The relaxed guards leave the stamp checks to the handler, at the full size too.
	fullFrame := frame(meta(full, "again.txt"), full)
	status, out = send(fullFrame, "", "", "")
	assert.Equal(t, http.StatusForbidden, status, "no stamp: %v", out)
	assert.Equal(t, "hub_server_untrusted", out["error"])
	status, out = send(fullFrame, relaySrc, "", "")
	assert.Equal(t, http.StatusForbidden, status, "a stamp without the connector's secret: %v", out)
	status, out = send(fullFrame, relaySrc, "forged", "")
	assert.Equal(t, http.StatusUnauthorized, status, "a forged secret is refused by the identity guard: %v", out)
	status, out = send(fullFrame, relaySrc, "auth", "hub-user-1234abcd")
	assert.Equal(t, http.StatusBadRequest, status, "a Hub user and a server: %v", out)
	assert.Equal(t, "ambiguous_origin", out["error"])
	assert.Len(t, e.stored(), stored, "none of them stored anything")
}

// countingBody counts what is read from it.
type countingBody struct {
	left int
	read int
}

func (c *countingBody) Read(p []byte) (int, error) {
	if c.left == 0 {
		return 0, io.EOF
	}
	n := min(len(p), c.left)
	for i := 0; i < n; i++ {
		p[i] = 0xAB
	}
	c.left -= n
	c.read += n
	return n, nil
}
func (c *countingBody) Close() error { return nil }

// An agent's token on a relayed file request: the identity check does not read the frame looking for claims
// (checkBoard and attributionClaims), as it does for any other board request; the board and subscription checks
// still run (Security's #4453 review, with the Orchestrator).
func TestIdentityCheckDoesNotReadARelayedFilesBody(t *testing.T) {
	_, bh := setupBoardTestServer(t)
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	defer db.Close()
	ctx := t.Context()
	_, err = bh.bs.Subscribe(ctx, "p", "Dev", "Developer", "claude-dev", nil, "")
	require.NoError(t, err)
	token, err := db.MintSessionToken(ctx, "sid-dev", "claude-dev")
	require.NoError(t, err)
	o := NewIdentityObserver(db, bh.bs, func(*http.Request) bool { return false })

	run := func(path string) (read, code int) {
		h := o.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		body := &countingBody{left: 3 << 20}
		req := httptest.NewRequest("POST", path, body)
		req.ContentLength = int64(body.left)
		req.Header.Set(store.SessionTokenHeader, token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return body.read, rec.Code
	}
	read, _ := run("/api/board/p/external-artifacts")
	assert.Zero(t, read, "the identity check read none of the frame")
	read, _ = run("/api/board/p/messages")
	assert.Positive(t, read, "another board route's body is still read for its claims")
	// The board check still runs: a token not subscribed on the board is refused as before, without a read.
	read, code := run("/api/board/other-board/external-artifacts")
	assert.Zero(t, read)
	assert.Equal(t, http.StatusForbidden, code, "an agent on another board is refused as for any board route")
}
