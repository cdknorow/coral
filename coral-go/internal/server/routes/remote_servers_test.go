package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/secretbox"
	"github.com/cdknorow/coral/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const routeSecret = "ROUTE-SECRET-API-KEY-abcdef"

type rsEnv struct {
	h      *RemoteServersHandler
	router chi.Router
	dir    string
	db     *store.DB
}

func newRSEnv(t *testing.T) *rsEnv {
	t.Helper()
	t.Setenv(secretbox.EnvKey, "")
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h := NewRemoteServersHandler(db, config.Load(dir))
	h.healthTimeout = 3 * time.Second
	r := chi.NewRouter()
	r.Use(DebugRequestLogger)
	r.Get("/api/servers", h.List)
	r.Post("/api/servers", h.Add)
	r.Patch("/api/servers/{id}", h.Update)
	r.Delete("/api/servers/{id}", h.Delete)
	r.Post("/api/servers/{id}/test", h.Test)
	return &rsEnv{h: h, router: r, dir: dir, db: db}
}

func (e *rsEnv) do(method, path string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

// fakeRemote requires a Bearer key even though it is on loopback, so a pass
// proves the hub sent the key rather than relying on any localhost bypass.
var rejectKeys atomic.Bool

func fakeRemote(t *testing.T, key string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rejectKeys.Load() || r.URL.Path != "/api/health" || r.Header.Get("Authorization") != "Bearer "+key {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"status":"ok","version":"9.9.9"}`))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func decode(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), v))
}

func TestServers_ListIncludesLocal(t *testing.T) {
	e := newRSEnv(t)
	w := e.do("GET", "/api/servers", nil)
	require.Equal(t, 200, w.Code)
	var out []map[string]any
	decode(t, w, &out)
	require.Len(t, out, 1)
	assert.Equal(t, "local", out[0]["id"])
}

func TestServers_AddTestPatchDelete(t *testing.T) {
	e := newRSEnv(t)
	rejectKeys.Store(false)
	t.Cleanup(func() { rejectKeys.Store(false) })
	remote := fakeRemote(t, routeSecret)

	w := e.do("POST", "/api/servers", map[string]any{
		"id": "ws", "label": "Workstation", "url": remote.URL, "api_key": routeSecret, "allow_private": true})
	require.Equal(t, 201, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), routeSecret)
	assert.NotContains(t, w.Body.String(), "api_key")
	assert.NotContains(t, w.Body.String(), "v1:")
	var srv store.RemoteServer
	decode(t, w, &srv)
	assert.Equal(t, store.RemoteStatusOnline, srv.Status)
	assert.NotNil(t, srv.LastSeen)

	w = e.do("GET", "/api/servers", nil)
	assert.NotContains(t, w.Body.String(), routeSecret)
	assert.NotContains(t, w.Body.String(), "api_key")
	var list []map[string]any
	decode(t, w, &list)
	require.Len(t, list, 2)
	assert.Equal(t, "local", list[0]["id"])
	assert.Equal(t, "ws", list[1]["id"])

	// Stored value is ciphertext only.
	var stored string
	require.NoError(t, e.db.Get(&stored, "SELECT api_key FROM remote_servers WHERE id='ws'"))
	assert.True(t, strings.HasPrefix(stored, "v1:"))
	assert.NotContains(t, stored, routeSecret)

	// Test re-runs the check.
	w = e.do("POST", "/api/servers/ws/test", nil)
	require.Equal(t, 200, w.Code)
	decode(t, w, &srv)
	assert.Equal(t, store.RemoteStatusOnline, srv.Status)
	assert.NotContains(t, w.Body.String(), routeSecret)

	// Rotating the remote key out from under the hub shows unauthorized.
	rejectKeys.Store(true)
	defer rejectKeys.Store(false)
	w = e.do("POST", "/api/servers/ws/test", nil)
	decode(t, w, &srv)
	assert.Equal(t, store.RemoteStatusUnauthorized, srv.Status)

	// PATCH label only: no remote call needed.
	w = e.do("PATCH", "/api/servers/ws", map[string]any{"label": "Renamed"})
	require.Equal(t, 200, w.Code, w.Body.String())
	decode(t, w, &srv)
	assert.Equal(t, "Renamed", srv.Label)

	// PATCH key against a remote that rejects it is refused, nothing changes.
	w = e.do("PATCH", "/api/servers/ws", map[string]any{"api_key": "new-key"})
	assert.Equal(t, 502, w.Code)
	assert.NotContains(t, w.Body.String(), "new-key")
	k, err := e.h.Store().DecryptedKey(context.Background(), "ws")
	require.NoError(t, err)
	assert.Equal(t, routeSecret, k)

	// PATCH key against an accepting remote works (write-only).
	rejectKeys.Store(false)
	good := fakeRemote(t, "new-key")
	w = e.do("PATCH", "/api/servers/ws", map[string]any{"api_key": "new-key", "url": good.URL})
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), "new-key")
	k, _ = e.h.Store().DecryptedKey(context.Background(), "ws")
	assert.Equal(t, "new-key", k)

	assert.Equal(t, 404, e.do("PATCH", "/api/servers/nope", map[string]any{"label": "x"}).Code)
	assert.Equal(t, 200, e.do("DELETE", "/api/servers/ws", nil).Code)
	assert.Equal(t, 404, e.do("DELETE", "/api/servers/ws", nil).Code)
	assert.Equal(t, 404, e.do("POST", "/api/servers/ws/test", nil).Code)
}

func TestServers_AddValidation(t *testing.T) {
	e := newRSEnv(t)
	remote := fakeRemote(t, routeSecret)
	body := func(m map[string]any) map[string]any {
		base := map[string]any{"id": "ok", "label": "L", "url": remote.URL, "api_key": routeSecret, "allow_private": true}
		for k, v := range m {
			base[k] = v
		}
		return base
	}
	for name, tc := range map[string]map[string]any{
		"local id":    body(map[string]any{"id": "local"}),
		"bad slug":    body(map[string]any{"id": "Bad_ID"}),
		"long slug":   body(map[string]any{"id": strings.Repeat("a", 33)}),
		"no key":      body(map[string]any{"api_key": ""}),
		"bad scheme":  body(map[string]any{"url": "ftp://example.com"}),
		"url path":    body(map[string]any{"url": remote.URL + "/x"}),
		"url creds":   body(map[string]any{"url": "http://u:p@example.com"}),
		"private url": body(map[string]any{"allow_private": false}), // loopback blocked by SSRF
	} {
		w := e.do("POST", "/api/servers", tc)
		assert.Equal(t, 400, w.Code, "%s: %s", name, w.Body.String())
		assert.NotContains(t, w.Body.String(), routeSecret, name)
	}
	assert.Equal(t, 400, e.do("POST", "/api/servers", map[string]any{"id": "ok"}).Code)

	// Nothing was saved.
	var n int
	require.NoError(t, e.db.Get(&n, "SELECT COUNT(*) FROM remote_servers"))
	assert.Zero(t, n)
	_, statErr := os.Stat(filepath.Join(e.dir, ".remote_secret_key"))
	assert.True(t, os.IsNotExist(statErr), "no master key created for rejected adds")

	// Wrong key -> not saved, status in error.
	w := e.do("POST", "/api/servers", body(map[string]any{"api_key": "wrong-key"}))
	assert.Equal(t, 502, w.Code)
	assert.Contains(t, w.Body.String(), store.RemoteStatusUnauthorized)
	assert.NotContains(t, w.Body.String(), "wrong-key")

	// Unreachable.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	w = e.do("POST", "/api/servers", body(map[string]any{"url": deadURL}))
	assert.Equal(t, 502, w.Code)
	assert.Contains(t, w.Body.String(), store.RemoteStatusUnreachable)

	// Not a Coral server (200 but wrong body).
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hi")) }))
	defer other.Close()
	w = e.do("POST", "/api/servers", body(map[string]any{"url": other.URL}))
	assert.Equal(t, 502, w.Code)

	// Duplicate.
	require.Equal(t, 201, e.do("POST", "/api/servers", body(nil)).Code)
	assert.Equal(t, 409, e.do("POST", "/api/servers", body(nil)).Code)

	// Turning allow_private off on a loopback server is refused.
	w = e.do("PATCH", "/api/servers/ok", map[string]any{"allow_private": false})
	assert.Equal(t, 400, w.Code)
}

func TestServers_RedirectNotFollowed(t *testing.T) {
	e := newRSEnv(t)
	var hit bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/api/health", http.StatusFound)
	}))
	defer redir.Close()
	w := e.do("POST", "/api/servers", map[string]any{"id": "r", "url": redir.URL, "api_key": routeSecret, "allow_private": true})
	assert.Equal(t, 502, w.Code)
	assert.False(t, hit, "key must not be forwarded across redirects")
}

func TestServers_KeyUnreadableDoesNotBreakOthers(t *testing.T) {
	e := newRSEnv(t)
	a := fakeRemote(t, "key-a")
	b := fakeRemote(t, "key-b")
	for id, r := range map[string]struct {
		url, key string
	}{"a": {a.URL, "key-a"}, "b": {b.URL, "key-b"}} {
		require.Equal(t, 201, e.do("POST", "/api/servers", map[string]any{"id": id, "url": r.url, "api_key": r.key, "allow_private": true}).Code)
	}
	// Swap ciphertext of a onto b.
	_, err := e.db.Exec("UPDATE remote_servers SET api_key=(SELECT api_key FROM remote_servers WHERE id='a') WHERE id='b'")
	require.NoError(t, err)

	var list []map[string]any
	w := e.do("GET", "/api/servers", nil)
	require.Equal(t, 200, w.Code)
	decode(t, w, &list)
	st := map[string]any{}
	for _, s := range list {
		st[s["id"].(string)] = s["status"]
	}
	assert.Equal(t, "key_unreadable", st["b"])
	assert.NotEqual(t, "key_unreadable", st["a"])

	w = e.do("POST", "/api/servers/b/test", nil)
	require.Equal(t, 200, w.Code)
	assert.Contains(t, w.Body.String(), "key_unreadable")
	w = e.do("POST", "/api/servers/a/test", nil)
	assert.Contains(t, w.Body.String(), `"online"`)

	// Re-entering the key recovers b.
	w = e.do("PATCH", "/api/servers/b", map[string]any{"api_key": "key-b"})
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"online"`)
}

func TestServers_NoPlaintextInLogsOrDB(t *testing.T) {
	e := newRSEnv(t)
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)
	old := slowRequestThreshold
	slowRequestThreshold = 0
	defer func() { slowRequestThreshold = old }()
	t.Setenv("CORAL_DEBUG", "1")

	remote := fakeRemote(t, routeSecret)
	w := e.do("POST", "/api/servers", map[string]any{"id": "ws", "url": remote.URL, "api_key": routeSecret, "allow_private": true})
	require.Equal(t, 201, w.Code)
	// Even if a client puts the key in the query string it must not be logged.
	req := httptest.NewRequest("GET", "/api/servers?api_key="+routeSecret, nil)
	e.router.ServeHTTP(httptest.NewRecorder(), req)
	e.do("POST", "/api/servers/ws/test", nil)

	assert.NotContains(t, logs.String(), routeSecret)
	assert.Contains(t, logs.String(), "slow request")
	assert.Contains(t, logs.String(), "REDACTED")

	e.db.Exec("PRAGMA wal_checkpoint(FULL)")
	for _, f := range []string{"sessions.db", "sessions.db-wal"} {
		if b, err := os.ReadFile(filepath.Join(e.dir, f)); err == nil {
			assert.False(t, bytes.Contains(b, []byte(routeSecret)), f)
		}
	}
}

func TestRedactQuery(t *testing.T) {
	assert.Equal(t, "", RedactQuery(""))
	assert.Equal(t, "a=1", RedactQuery("a=1"))
	got := RedactQuery("a=1&api_key=SECRET&Token=T2")
	assert.NotContains(t, got, "SECRET")
	assert.NotContains(t, got, "T2")
	assert.Contains(t, got, "a=1")
}
