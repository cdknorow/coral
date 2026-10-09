package server

// End-to-end tests for the multi-server hub (docs/spec-multi-server-hub.md,
// WP7). One hub (cfg.HubMode) and two real remote servers are stood up
// in-process, each with its own data dir, SQLite DBs, API key store and PTY
// backend. Everything the tests do to a remote goes through the hub's HTTP API
// (/api/servers, /api/remote/{server}/..., /api/sessions/live, /ws/coral).
//
// How the remotes genuinely require their API key: auth.Middleware lets a
// loopback RemoteAddr through without a key. Every remote is therefore wrapped
// in a handler that rewrites r.RemoteAddr to a non-loopback address
// (203.0.113.x, TEST-NET-3) before the real router sees the request, exactly as
// if the hub reached it over a LAN. The remote's real auth middleware and real
// key store are used unchanged, so a request without the right key gets 401.
// TestHubE2E_RemoteRequiresKey proves the bypass is not what makes the proxy
// work: a keyless request to the remote fails, and the requests the hub sends
// carry the remote's key.
//
// Terminals are launched through the proxy with agent_type "terminal", so no
// agent CLI (claude, ...) is needed. When the environment lets the process
// spawn /bin/sh (CORAL_SHELL=/bin/sh) they are real PTY sessions; in a sandbox
// that forbids fork/exec they are served by an in-memory fake backend (below)
// that emulates just enough of a shell ("echo ..."). The same assertions run in
// both modes; the mode in use is logged by each test.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"

	"github.com/cdknorow/coral/internal/background"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/store"
)

// ── shared plumbing ─────────────────────────────────────────────────

// e2eEnv isolates process-global state for one test: shell, temp dir (PTY log
// files), transcripts dir, fast hub polling, and log capture.
type e2eEnv struct {
	logs *lockedBuf

	mu     sync.Mutex
	bodies [][]byte // every response body the test saw from the hub API
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func newE2EEnv(t *testing.T) *e2eEnv {
	t.Helper()
	t.Setenv("CORAL_SHELL", "/bin/sh")
	t.Logf("terminal backend: real PTY=%v (false means in-memory fake; sandbox forbids fork/exec)", realPTYUsable())
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("CLAUDE_PROJECTS_DIR", filepath.Join(t.TempDir(), "projects"))
	t.Setenv("CORAL_SECRET_KEY", "") // never inherit an override from the environment

	prev := remoteAgentHubConfig
	remoteAgentHubConfig = background.RemoteAgentHubConfig{
		PollInterval:    150 * time.Millisecond,
		RequestTimeout:  2 * time.Second,
		RegistryRefresh: 150 * time.Millisecond,
		MinBackoff:      50 * time.Millisecond,
		MaxBackoff:      200 * time.Millisecond,
	}
	t.Cleanup(func() { remoteAgentHubConfig = prev })

	env := &e2eEnv{logs: &lockedBuf{}}
	prevW, prevFlags := log.Writer(), log.Flags()
	prevSlog := slog.Default()
	log.SetOutput(env.logs)
	slog.SetDefault(slog.New(slog.NewTextHandler(env.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		log.SetOutput(prevW)
		log.SetFlags(prevFlags)
		slog.SetDefault(prevSlog)
	})
	return env
}

func newE2EConfig(t *testing.T, dir string) *config.Config {
	t.Helper()
	cfg := config.Load(dir) // explicit dir: never ~/.coral
	cfg.DBPath = filepath.Join(dir, "sessions.db")
	cfg.Host = "127.0.0.1"
	cfg.Port = 0
	cfg.LogDir = dir
	cfg.CoralRoot = dir
	cfg.WSPollIntervalS = 1
	return cfg
}

// ── remote server ───────────────────────────────────────────────────

type seenReq struct {
	Method, Path, RawQuery, Auth, Cookie, RemoteAddr string
}

// e2eRemote is a real Coral server whose API key is genuinely enforced.
type e2eRemote struct {
	t       *testing.T
	id      string
	dir     string
	work    string // a directory that exists only for this remote's tests
	cfg     *config.Config
	db      *store.DB
	backend ptymanager.TerminalBackend
	fake    *fakeBackend // nil when a real PTY backend is in use
	srv     *Server

	mu       sync.Mutex
	reqs     []seenReq
	hs       *http.Server
	addr     string
	hijacked map[net.Conn]struct{}
}

const fakeClientAddr = "203.0.113.7:4242"

func newE2ERemote(t *testing.T, id string, mutate func(*config.Config)) *e2eRemote {
	t.Helper()
	dir := t.TempDir()
	cfg := newE2EConfig(t, dir)
	if mutate != nil {
		mutate(cfg)
	}
	db, err := store.Open(cfg.DBPath)
	require.NoError(t, err)
	stack := newTermStack(t)
	backend := stack.backend
	r := &e2eRemote{
		t: t, id: id, dir: dir, cfg: cfg, db: db, backend: backend, fake: stack.fake,
		work:     filepath.Join(dir, "work", "proj"), // same base name on every remote
		srv:      New(cfg, db, backend, stack.terminal),
		hijacked: map[net.Conn]struct{}{},
	}
	require.NoError(t, os.MkdirAll(r.work, 0o755))
	r.start("127.0.0.1:0")
	t.Cleanup(func() {
		r.stop()
		for _, s := range backend.ListSessions() {
			_ = backend.Kill(s.AgentName)
		}
		db.Close()
	})
	return r
}

func (r *e2eRemote) key() string { return r.srv.keyStore.Key() }
func (r *e2eRemote) url() string { return "http://" + r.addr }

func (r *e2eRemote) handler() http.Handler {
	inner := r.srv.Router()
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		req.RemoteAddr = fakeClientAddr // non-loopback: auth is enforced
		r.mu.Lock()
		r.reqs = append(r.reqs, seenReq{
			Method: req.Method, Path: req.URL.Path, RawQuery: req.URL.RawQuery,
			Auth: req.Header.Get("Authorization"), Cookie: req.Header.Get("Cookie"), RemoteAddr: req.RemoteAddr,
		})
		r.mu.Unlock()
		inner.ServeHTTP(w, req)
	})
}

func (r *e2eRemote) start(addr string) {
	r.t.Helper()
	var ln net.Listener
	var err error
	for i := 0; i < 50; i++ { // the port may linger briefly after stop()
		if ln, err = net.Listen("tcp", addr); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.NoError(r.t, err)
	r.addr = ln.Addr().String()
	hs := &http.Server{
		Handler: r.handler(),
		ConnState: func(c net.Conn, s http.ConnState) {
			if s == http.StateHijacked {
				r.mu.Lock()
				r.hijacked[c] = struct{}{}
				r.mu.Unlock()
			}
		},
	}
	r.mu.Lock()
	r.hs = hs
	r.mu.Unlock()
	go hs.Serve(ln)
}

// stop takes the remote offline: the listener, open requests and hijacked
// (websocket) connections all go away. State (DB, sessions) is kept.
func (r *e2eRemote) stop() {
	r.mu.Lock()
	hs := r.hs
	r.hs = nil
	conns := r.hijacked
	r.hijacked = map[net.Conn]struct{}{}
	r.mu.Unlock()
	if hs == nil {
		return
	}
	hs.Close()
	for c := range conns {
		c.Close()
	}
}

func (r *e2eRemote) restart() { r.start(r.addr) }

func (r *e2eRemote) seen() []seenReq {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]seenReq(nil), r.reqs...)
}

func (r *e2eRemote) count(method, path string) int {
	n := 0
	for _, q := range r.seen() {
		if q.Method == method && q.Path == path {
			n++
		}
	}
	return n
}

// direct sends a request straight to the remote (as a non-loopback client).
func (r *e2eRemote) direct(method, path, bearer string) int {
	r.t.Helper()
	req, err := http.NewRequest(method, r.url()+path, nil)
	require.NoError(r.t, err)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(r.t, err)
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func (r *e2eRemote) seedSleeping(name, sid string) {
	r.t.Helper()
	require.NoError(r.t, store.NewSessionStore(r.db).RegisterLiveSession(context.Background(), &store.LiveSession{
		SessionID: sid, AgentType: "claude", AgentName: name, WorkingDir: r.work, IsSleeping: 1,
		CreatedAt: "2026-01-01T00:00:00Z",
	}))
}

func (r *e2eRemote) liveCount() int {
	all, err := store.NewSessionStore(r.db).GetAllLiveSessions(context.Background())
	require.NoError(r.t, err)
	return len(all)
}

// ── hub ─────────────────────────────────────────────────────────────

type e2eHub struct {
	t       *testing.T
	env     *e2eEnv
	dir     string
	cfg     *config.Config
	db      *store.DB
	backend ptymanager.TerminalBackend
	srv     *Server
	ts      *httptest.Server
	cancel  context.CancelFunc
	done    chan struct{}
}

func newE2EHub(t *testing.T, env *e2eEnv, dir string, hubMode bool) *e2eHub {
	t.Helper()
	if dir == "" {
		dir = t.TempDir()
	}
	cfg := newE2EConfig(t, dir)
	cfg.HubMode = hubMode
	db, err := store.Open(cfg.DBPath)
	require.NoError(t, err)
	stack := newTermStack(t)
	backend := stack.backend
	h := &e2eHub{t: t, env: env, dir: dir, cfg: cfg, db: db, backend: backend, done: make(chan struct{})}
	h.srv = New(cfg, db, backend, stack.terminal)
	h.ts = httptest.NewServer(h.srv.Router())
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { defer close(h.done); h.srv.RunRemoteAgents(ctx) }()
	t.Cleanup(h.close)
	return h
}

func (h *e2eHub) close() {
	if h.cancel == nil {
		return
	}
	h.cancel()
	<-h.done
	h.ts.CloseClientConnections()
	h.ts.Close()
	for _, s := range h.backend.ListSessions() {
		_ = h.backend.Kill(s.AgentName)
	}
	h.db.Close()
	h.cancel = nil
}

// do performs a request against the hub and records the body for the
// plaintext-key scan.
func (h *e2eHub) do(method, path string, body any) (int, []byte) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(h.t, err)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.ts.URL+path, rd)
	require.NoError(h.t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return h.send(req)
}

func (h *e2eHub) send(req *http.Request) (int, []byte) {
	h.t.Helper()
	resp, err := http.DefaultClient.Do(req)
	require.NoError(h.t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(h.t, err)
	h.env.mu.Lock()
	h.env.bodies = append(h.env.bodies, b)
	h.env.mu.Unlock()
	return resp.StatusCode, b
}

func (h *e2eHub) getJSON(path string, out any) int {
	h.t.Helper()
	code, b := h.do("GET", path, nil)
	if out != nil && code == 200 {
		require.NoError(h.t, json.Unmarshal(b, out), string(b))
	}
	return code
}

type regResult struct {
	Code int
	Body map[string]any
}

func (h *e2eHub) register(id, url, key string, allowPrivate bool) regResult {
	h.t.Helper()
	code, b := h.do("POST", "/api/servers", map[string]any{
		"id": id, "label": strings.ToUpper(id), "url": url, "api_key": key, "allow_private": allowPrivate,
	})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return regResult{code, m}
}

func (h *e2eHub) serverStatus(id string) string {
	h.t.Helper()
	var list []map[string]any
	require.Equal(h.t, 200, h.getJSON("/api/servers", &list))
	for _, s := range list {
		if s["id"] == id {
			st, _ := s["status"].(string)
			return st
		}
	}
	return "(absent)"
}

// liveEntry is the subset of a live-list row the tests inspect.
type liveEntry struct {
	Name      string `json:"name"`
	SessionID string `json:"session_id"`
	Server    string `json:"server"`
	Stale     bool   `json:"stale"`
	AgentType string `json:"agent_type"`
}

type liveView struct {
	Sessions []liveEntry
	Servers  []background.RemoteServerStatus
	Plain    bool // true when the hub returned a plain array (no remotes registered)
}

func (h *e2eHub) live() liveView {
	h.t.Helper()
	code, b := h.do("GET", "/api/sessions/live", nil)
	require.Equal(h.t, 200, code, string(b))
	var v liveView
	if err := json.Unmarshal(b, &v.Sessions); err == nil {
		v.Plain = true
		return v
	}
	var obj struct {
		Sessions []liveEntry                     `json:"sessions"`
		Servers  []background.RemoteServerStatus `json:"servers"`
	}
	require.NoError(h.t, json.Unmarshal(b, &obj), string(b))
	v.Sessions, v.Servers = obj.Sessions, obj.Servers
	return v
}

func (v liveView) find(server, name string) (liveEntry, bool) {
	for _, s := range v.Sessions {
		if s.Server == server && s.Name == name {
			return s, true
		}
	}
	return liveEntry{}, false
}

func (v liveView) status(server string) string {
	for _, s := range v.Servers {
		if s.ID == server {
			return s.Status
		}
	}
	return ""
}

func (h *e2eHub) eventually(what string, cond func() bool) {
	h.t.Helper()
	require.Eventually(h.t, cond, 10*time.Second, 50*time.Millisecond, what)
}

// launchTerminal starts a real PTY terminal on server via the hub proxy (or on
// the hub itself when server == "local") and returns the launch response.
func (h *e2eHub) launchTerminal(server, workDir, display string) map[string]any {
	h.t.Helper()
	path := "/api/sessions/launch"
	if server != "local" {
		path = "/api/remote/" + server + path
	}
	code, b := h.do("POST", path, map[string]any{"working_dir": workDir, "agent_type": "terminal", "display_name": display})
	require.Equal(h.t, 200, code, string(b))
	var m map[string]any
	require.NoError(h.t, json.Unmarshal(b, &m))
	require.Equal(h.t, true, m["ok"], string(b))
	return m
}

func wsURLFor(ts *httptest.Server, path string) string {
	return "ws" + strings.TrimPrefix(ts.URL, "http") + path
}

// assertNoPlaintext fails if any key appears in a hub response, the captured
// logs, or the raw bytes of the hub's database files.
func (e *e2eEnv) assertNoPlaintext(t *testing.T, hubDir string, keys ...string) {
	t.Helper()
	e.mu.Lock()
	bodies := append([][]byte(nil), e.bodies...)
	e.mu.Unlock()
	logs := e.logs.String()
	for _, k := range keys {
		require.NotEmpty(t, k)
		for _, b := range bodies {
			require.NotContains(t, string(b), k, "plaintext API key in a hub API response")
		}
		require.NotContains(t, logs, k, "plaintext API key in captured logs")
		for _, f := range []string{"sessions.db", "sessions.db-wal", "sessions.db-shm"} {
			raw, err := os.ReadFile(filepath.Join(hubDir, f))
			if err != nil {
				continue
			}
			require.False(t, bytes.Contains(raw, []byte(k)), "plaintext API key in %s", f)
		}
	}
}

// ── 1. hub off vs on ────────────────────────────────────────────────

func TestHubE2E_OffVersusOn(t *testing.T) {
	env := newE2EEnv(t)
	off := newE2EHub(t, env, "", false)
	remote := newE2ERemote(t, "a", nil)

	var health map[string]any
	require.Equal(t, 200, off.getJSON("/api/health", &health))
	assert.Equal(t, false, health["hub"])
	for _, p := range []string{"/api/servers", "/api/remote/a/api/health", "/api/servers/a/test"} {
		code, _ := off.do("GET", p, nil)
		assert.Contains(t, []int{404, 405}, code, "hub off: %s", p)
	}
	code, _ := off.do("POST", "/api/servers", map[string]any{"id": "a", "url": remote.url(), "api_key": remote.key()})
	assert.Contains(t, []int{404, 405}, code, "hub off must not register servers")
	assert.True(t, off.live().Plain, "hub off: live list is the unchanged plain array")
	assert.Zero(t, remote.count("GET", "/api/health"), "hub off must never contact a remote")

	on := newE2EHub(t, env, "", true)
	require.Equal(t, 200, on.getJSON("/api/health", &health))
	assert.Equal(t, true, health["hub"])
	var list []map[string]any
	require.Equal(t, 200, on.getJSON("/api/servers", &list))
	require.Len(t, list, 1, "hub on, no remotes: only the implicit local entry")
	assert.Equal(t, "local", list[0]["id"])
	assert.True(t, on.live().Plain, "hub on with zero remotes: live list unchanged")
	code, _ = on.do("GET", "/api/remote/missing/api/health", nil)
	assert.Equal(t, 404, code)
	// Existing registry rows are ignored while hub mode is off and used again
	// when it is turned back on (same data dir).
	reg := on.register("a", remote.url(), remote.key(), true)
	require.Equal(t, 201, reg.Code, reg.Body)
	dir := on.dir
	on.close()
	offAgain := newE2EHub(t, env, dir, false)
	code, _ = offAgain.do("GET", "/api/servers", nil)
	assert.Equal(t, 404, code)
	offAgain.close()
	onAgain := newE2EHub(t, env, dir, true)
	assert.Equal(t, "online", onAgain.serverStatus("a"), "row survives a hub-off period")
	env.assertNoPlaintext(t, dir, remote.key())
}

// ── 2. remote requires its key; registration outcomes ───────────────

func TestHubE2E_RemoteRequiresKey(t *testing.T) {
	env := newE2EEnv(t)
	hub := newE2EHub(t, env, "", true)
	a := newE2ERemote(t, "a", nil)

	// The remote really enforces its key for a non-loopback client.
	assert.Equal(t, 401, a.direct("GET", "/api/sessions/live", ""), "no key")
	assert.Equal(t, 401, a.direct("GET", "/api/sessions/live", "not-the-key"), "wrong key")
	assert.Equal(t, 200, a.direct("GET", "/api/sessions/live", a.key()), "right key")
	// ... and /api/health carries no auth exemption either (hub depends on it).
	assert.Equal(t, 401, a.direct("GET", "/api/health", ""))

	reg := hub.register("a", a.url(), a.key(), true)
	require.Equal(t, 201, reg.Code, reg.Body)
	assert.Equal(t, "online", reg.Body["status"])

	// Every request the hub made to the remote came from a non-loopback
	// address and carried the remote's key as a bearer token.
	hubReqs := 0
	for _, q := range a.seen() {
		if q.Auth == "Bearer "+a.key() {
			hubReqs++
			assert.Equal(t, fakeClientAddr, q.RemoteAddr)
		}
	}
	require.Positive(t, hubReqs)

	// Through the proxy the remote answers; with the key regenerated on the
	// remote, the same call fails: the key (not the loopback bypass) is what
	// lets the proxy work.
	code, _ := hub.do("GET", "/api/remote/a/api/sessions/live", nil)
	require.Equal(t, 200, code)
	// Sustained legitimate use must never be throttled by the remote's brute
	// force limiter (it counts failed guesses only). The hub's own poller adds
	// to this load.
	for i := 0; i < 40; i++ {
		code, body := hub.do("GET", "/api/remote/a/api/sessions/live", nil)
		require.Equal(t, 200, code, "request %d: %s", i, body)
	}
	_, err := a.srv.keyStore.RegenerateKey()
	require.NoError(t, err)
	code, b := hub.do("GET", "/api/remote/a/api/sessions/live", nil)
	assert.Equal(t, 502, code)
	assert.Contains(t, string(b), "remote rejected API key")
	hub.eventually("status unauthorized after remote key rotated", func() bool { return hub.serverStatus("a") == "unauthorized" })
}

func TestHubE2E_RegisterOutcomesAndKeyHygiene(t *testing.T) {
	env := newE2EEnv(t)
	hub := newE2EHub(t, env, "", true)
	a := newE2ERemote(t, "a", nil)
	b := newE2ERemote(t, "b", nil)

	good := hub.register("a", a.url(), a.key(), true)
	require.Equal(t, 201, good.Code, good.Body)
	assert.Equal(t, "online", good.Body["status"])

	// Wrong key -> unauthorized, and nothing is saved.
	bad := hub.register("b", b.url(), "definitely-the-wrong-key-0123456789abcdef", true)
	assert.Equal(t, 502, bad.Code)
	assert.Equal(t, "unauthorized", bad.Body["status"])
	assert.Equal(t, "(absent)", hub.serverStatus("b"))

	// Dead URL -> unreachable. Grab a port and close it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	dead := "http://" + ln.Addr().String()
	ln.Close()
	un := hub.register("dead", dead, "k", true)
	assert.Equal(t, 502, un.Code)
	assert.Equal(t, "unreachable", un.Body["status"])

	// Loopback is refused unless allow_private is set (SSRF guard at registration).
	priv := hub.register("b", b.url(), b.key(), false)
	assert.Equal(t, 400, priv.Code, priv.Body)

	// Now a real second remote.
	require.Equal(t, 201, hub.register("b", b.url(), b.key(), true).Code)

	var list []map[string]any
	require.Equal(t, 200, hub.getJSON("/api/servers", &list))
	require.Len(t, list, 3) // local + a + b
	for _, s := range list {
		_, hasKey := s["api_key"]
		assert.False(t, hasKey, "api_key must never be returned")
	}
	// Test endpoint and PATCH (label) also keep the key out.
	code, _ := hub.do("POST", "/api/servers/a/test", nil)
	assert.Equal(t, 200, code)
	code, _ = hub.do("PATCH", "/api/servers/a", map[string]any{"label": "Workstation"})
	assert.Equal(t, 200, code)

	// Run the poller and a proxied call so keys have flowed through the hub.
	hub.eventually("both online", func() bool { return hub.live().status("a") == "online" && hub.live().status("b") == "online" })
	code, _ = hub.do("GET", "/api/remote/a/api/sessions/live", nil)
	require.Equal(t, 200, code)

	// Stored column is ciphertext only, in a v1 envelope; raw DB bytes, API
	// responses and logs contain no plaintext key.
	rows, err := hub.db.Queryx("SELECT id, api_key FROM remote_servers ORDER BY id")
	require.NoError(t, err)
	n := 0
	for rows.Next() {
		var id, enc string
		require.NoError(t, rows.Scan(&id, &enc))
		assert.True(t, strings.HasPrefix(enc, "v1:"), "%s: %q", id, enc)
		assert.NotContains(t, enc, a.key())
		assert.NotContains(t, enc, b.key())
		n++
	}
	rows.Close()
	require.Equal(t, 2, n)
	_, err = hub.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	require.NoError(t, err)
	env.assertNoPlaintext(t, hub.dir, a.key(), b.key())
	keyFile, err := os.Stat(filepath.Join(hub.dir, ".remote_secret_key"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), keyFile.Mode().Perm())
}

// ── 3. merged list, same-named agents, live updates ─────────────────

func TestHubE2E_MergedListSameNames(t *testing.T) {
	env := newE2EEnv(t)
	hub := newE2EHub(t, env, "", true)
	a := newE2ERemote(t, "a", nil)
	b := newE2ERemote(t, "b", nil)
	require.Equal(t, 201, hub.register("a", a.url(), a.key(), true).Code)
	require.Equal(t, 201, hub.register("b", b.url(), b.key(), true).Code)

	// Same-named agents everywhere: a seeded sleeping agent "dup" on the hub
	// and on both remotes, plus real terminals launched via the proxy whose
	// names derive from the identical folder name "proj".
	hubWork := filepath.Join(hub.dir, "work", "proj")
	require.NoError(t, os.MkdirAll(hubWork, 0o755))
	require.NoError(t, store.NewSessionStore(hub.db).RegisterLiveSession(context.Background(), &store.LiveSession{
		SessionID: "hub-dup-1", AgentType: "claude", AgentName: "dup", WorkingDir: hubWork, IsSleeping: 1, CreatedAt: "2026-01-01T00:00:00Z"}))
	a.seedSleeping("dup", "a-dup-1")
	b.seedSleeping("dup", "b-dup-1")
	la := hub.launchTerminal("a", a.work, "")
	lb := hub.launchTerminal("b", b.work, "")
	ll := hub.launchTerminal("local", hubWork, "")
	nameA, nameB, nameL := "proj", "proj", "proj"
	_ = la
	_ = lb
	_ = ll

	hub.eventually("everything merged", func() bool {
		v := hub.live()
		for _, srv := range []string{"local", "a", "b"} {
			if _, ok := v.find(srv, "dup"); !ok {
				return false
			}
		}
		_, ka := v.find("a", nameA)
		_, kb := v.find("b", nameB)
		_, kl := v.find("local", nameL)
		return ka && kb && kl
	})
	v := hub.live()
	require.False(t, v.Plain, "with remotes registered the list carries servers")
	ids := map[string]string{}
	for _, srv := range []string{"local", "a", "b"} {
		e, _ := v.find(srv, "dup")
		assert.False(t, e.Stale)
		ids[srv] = e.SessionID
	}
	assert.Equal(t, map[string]string{"local": "hub-dup-1", "a": "a-dup-1", "b": "b-dup-1"}, ids,
		"same name, three distinct identities")
	ea, _ := v.find("a", "proj")
	eb, _ := v.find("b", "proj")
	assert.NotEqual(t, ea.SessionID, eb.SessionID)
	assert.Equal(t, la["session_id"], ea.SessionID)
	assert.Equal(t, lb["session_id"], eb.SessionID)
	assert.Equal(t, "online", v.status("a"))
	assert.Equal(t, "online", v.status("b"))

	// Live update: an agent created on a remote *directly* (not via the hub)
	// shows up in the hub list within a poll interval, and its removal too.
	direct := a.launchDirectTerminal(t, a.work)
	hub.eventually("direct launch on a visible in hub", func() bool {
		for _, s := range hub.live().Sessions {
			if s.Server == "a" && s.SessionID == direct {
				return true
			}
		}
		return false
	})
	require.NoError(t, store.NewSessionStore(a.db).UnregisterLiveSession(context.Background(), direct))
	_ = a.backend.Kill(a.sessionNameFor(direct))
	hub.eventually("removal reflected", func() bool {
		for _, s := range hub.live().Sessions {
			if s.SessionID == direct {
				return false
			}
		}
		return true
	})

	// The hub's own dashboard feed tags remote sessions with their server.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, wsURLFor(hub.ts, "/ws/coral"), nil)
	require.NoError(t, err)
	defer c.CloseNow()
	c.SetReadLimit(8 << 20)
	var msg struct {
		Type     string           `json:"type"`
		Sessions []map[string]any `json:"sessions"`
	}
	require.NoError(t, wsjson.Read(ctx, c, &msg))
	require.Equal(t, "coral_update", msg.Type)
	seen := map[string]bool{}
	for _, s := range msg.Sessions {
		sv, _ := s["server"].(string)
		if s["name"] == "dup" {
			seen[sv] = true
		}
	}
	assert.Equal(t, map[string]bool{"local": true, "a": true, "b": true}, seen, "feed carries all three dup agents, tagged")
	c.Close(websocket.StatusNormalClosure, "")
}

// launchDirectTerminal creates a terminal on the remote bypassing the hub.
func (r *e2eRemote) launchDirectTerminal(t *testing.T, workDir string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"working_dir": workDir, "agent_type": "terminal"})
	req, err := http.NewRequest("POST", r.url()+"/api/sessions/launch", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+r.key())
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	require.Equal(t, 200, resp.StatusCode, string(b))
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m["session_id"].(string)
}

func (r *e2eRemote) sessionNameFor(sessionID string) string {
	for _, s := range r.backend.ListSessions() {
		if strings.Contains(s.AgentName, sessionID) {
			return s.AgentName
		}
	}
	return ""
}

// ── 5. chat history and terminal through the proxy ──────────────────

func TestHubE2E_ChatAndTerminalViaProxy(t *testing.T) {
	env := newE2EEnv(t)
	hub := newE2EHub(t, env, "", true)
	a := newE2ERemote(t, "a", nil)
	b := newE2ERemote(t, "b", nil)
	require.Equal(t, 201, hub.register("a", a.url(), a.key(), true).Code)
	require.Equal(t, 201, hub.register("b", b.url(), b.key(), true).Code)

	// Chat history: a Claude transcript for the same agent name on each remote.
	a.seedSleeping("dup", "aaaaaaaa-0000-0000-0000-00000000000a")
	b.seedSleeping("dup", "bbbbbbbb-0000-0000-0000-00000000000b")
	proj := filepath.Join(os.Getenv("CLAUDE_PROJECTS_DIR"), "p")
	require.NoError(t, os.MkdirAll(proj, 0o755))
	writeTranscript := func(sid, text string) {
		line, _ := json.Marshal(map[string]any{
			"type": "user", "timestamp": "2026-01-01T00:00:00Z", "sessionId": sid,
			"message": map[string]any{"role": "user", "content": text},
		})
		require.NoError(t, os.WriteFile(filepath.Join(proj, sid+".jsonl"), append(line, '\n'), 0o644))
	}
	writeTranscript("aaaaaaaa-0000-0000-0000-00000000000a", "hello from agent A history")
	writeTranscript("bbbbbbbb-0000-0000-0000-00000000000b", "hello from agent B history")

	chat := func(srv, sid string) string {
		q := url.Values{"agent_type": {"claude"}, "session_id": {sid}, "after": {"0"}}
		code, body := hub.do("GET", "/api/remote/"+srv+"/api/sessions/live/dup/chat?"+q.Encode(), nil)
		require.Equal(t, 200, code, string(body))
		return string(body)
	}
	ca := chat("a", "aaaaaaaa-0000-0000-0000-00000000000a")
	cb := chat("b", "bbbbbbbb-0000-0000-0000-00000000000b")
	assert.Contains(t, ca, "hello from agent A history")
	assert.NotContains(t, ca, "agent B")
	assert.Contains(t, cb, "hello from agent B history")
	assert.NotContains(t, cb, "agent A")

	// Terminal: launch a real shell on A through the proxy, then drive it over
	// a websocket that also goes through /api/remote/a/ws/terminal/...
	l := hub.launchTerminal("a", a.work, "")
	sname := l["session_name"].(string)
	sid := l["session_id"].(string)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	wsPath := "/api/remote/a/ws/terminal/" + sname + "?agent_type=terminal&session_id=" + sid
	c, _, err := websocket.Dial(ctx, wsURLFor(hub.ts, wsPath), nil)
	require.NoError(t, err)
	defer c.CloseNow()
	require.NoError(t, wsjson.Write(ctx, c, map[string]any{"type": "terminal_input", "data": "echo hub-e2e-$((20+22))\n"}))
	var got strings.Builder
	for !strings.Contains(strings.ReplaceAll(got.String(), "\r", ""), "hub-e2e-42\n") {
		typ, data, err := c.Read(ctx)
		require.NoError(t, err, "terminal output so far: %q", got.String())
		if typ == websocket.MessageBinary {
			got.Write(data)
		}
	}
	// The keystrokes ran on remote A's PTY only.
	assert.NotEmpty(t, a.backend.ListSessions())
	assert.Empty(t, b.backend.ListSessions())
	assert.Empty(t, hub.backend.ListSessions())
	// The hub dialled the remote's terminal socket with the remote's key.
	var upgraded bool
	for _, q := range a.seen() {
		if strings.HasPrefix(q.Path, "/ws/terminal/") {
			upgraded = true
			assert.Equal(t, "Bearer "+a.key(), q.Auth)
			assert.Empty(t, q.Cookie)
		}
	}
	assert.True(t, upgraded)

	// Closing the browser side closes the upstream (the PTY unsubscribes) ...
	require.NoError(t, c.Close(websocket.StatusNormalClosure, "bye"))
	if a.fake != nil {
		hub.eventually("remote terminal subscriber released after browser close", func() bool { return a.fake.subscribers(sname) == 0 })
	}

	// ... and a remote that drops mid-stream ends the browser socket instead
	// of hanging it.
	c2, _, err := websocket.Dial(ctx, wsURLFor(hub.ts, wsPath), nil)
	require.NoError(t, err)
	defer c2.CloseNow()
	require.NoError(t, wsjson.Write(ctx, c2, map[string]any{"type": "terminal_input", "data": "echo alive\n"}))
	_, _, err = c2.Read(ctx)
	require.NoError(t, err)
	a.stop()
	for {
		if _, _, err = c2.Read(ctx); err != nil {
			break
		}
	}
	require.NoError(t, ctx.Err(), "browser socket must close promptly when the remote drops, not hang")
	assert.NotEqual(t, -1, int(websocket.CloseStatus(err)), "a close status is propagated, got %v", err)
}

// ── 7. launch through the proxy reaches exactly one server ──────────

func TestHubE2E_LaunchOnRemoteOnly(t *testing.T) {
	env := newE2EEnv(t)
	hub := newE2EHub(t, env, "", true)
	a := newE2ERemote(t, "a", nil)
	b := newE2ERemote(t, "b", nil)
	require.Equal(t, 201, hub.register("a", a.url(), a.key(), true).Code)
	require.Equal(t, 201, hub.register("b", b.url(), b.key(), true).Code)

	// Browse the remote's filesystem through the proxy for the working dir.
	var fs struct {
		Path    string   `json:"path"`
		Entries []string `json:"entries"`
	}
	parent := filepath.Dir(b.work)
	require.Equal(t, 200, hub.getJSON("/api/remote/b/api/filesystem/list?path="+url.QueryEscape(parent), &fs))
	assert.Contains(t, fs.Entries, "proj")
	var git map[string]any
	require.Equal(t, 200, hub.getJSON("/api/remote/b/api/filesystem/is-git?path="+url.QueryEscape(b.work), &git))

	aBefore := len(a.seen())
	l := hub.launchTerminal("b", b.work, "term-b")
	assert.Equal(t, b.work, l["working_dir"])

	assert.Equal(t, 1, b.count("POST", "/api/sessions/launch"))
	assert.Equal(t, 1, b.liveCount(), "agent registered on b")
	assert.NotEmpty(t, b.backend.ListSessions(), "process runs on b")
	assert.Equal(t, 0, a.count("POST", "/api/sessions/launch"), "a never saw the launch")
	assert.Equal(t, 0, a.liveCount())
	assert.Empty(t, a.backend.ListSessions())
	for _, q := range a.seen()[aBefore:] {
		assert.NotEqual(t, "POST", q.Method, "a saw a write: %+v", q)
	}
	all, err := store.NewSessionStore(hub.db).GetAllLiveSessions(context.Background())
	require.NoError(t, err)
	assert.Empty(t, all, "hub registered nothing locally")
	assert.Empty(t, hub.backend.ListSessions())

	hub.eventually("launched agent appears under server b", func() bool {
		for _, s := range hub.live().Sessions {
			if s.Server == "b" && s.SessionID == l["session_id"] {
				return true
			}
		}
		return false
	})

	// A team launched through the proxy lives wholly on that remote: its
	// agents run there and register there; its board is that remote's board.
	code, body := hub.do("POST", "/api/remote/b/api/sessions/launch-team", map[string]any{
		"board_name": "e2e-team", "working_dir": b.work, "agent_type": "terminal",
		"agents": []map[string]any{{"name": "Lead", "prompt": "x"}, {"name": "Dev", "prompt": "y"}},
	})
	require.Equal(t, 200, code, string(body))
	assert.Equal(t, 1, b.count("POST", "/api/sessions/launch-team"))
	assert.Equal(t, 0, a.count("POST", "/api/sessions/launch-team"))
	assert.Equal(t, 3, b.liveCount(), "terminal + 2 team members on b")
	assert.Equal(t, 0, a.liveCount())
	assert.Len(t, b.backend.ListSessions(), 3)
	assert.Empty(t, a.backend.ListSessions())
	assert.Empty(t, hub.backend.ListSessions())
	var bBoards, aBoards, hBoards []map[string]any
	require.Equal(t, 200, hub.getJSON("/api/remote/b/api/board/projects", &bBoards))
	require.Equal(t, 200, hub.getJSON("/api/remote/a/api/board/projects", &aBoards))
	require.Equal(t, 200, hub.getJSON("/api/board/projects", &hBoards))
	has := func(l []map[string]any) bool {
		for _, p := range l {
			if p["project"] == "e2e-team" || p["name"] == "e2e-team" {
				return true
			}
		}
		return false
	}
	assert.True(t, has(bBoards), "team board exists on b: %v", bBoards)
	assert.False(t, has(aBoards))
	assert.False(t, has(hBoards))

	// Failure never falls back to local: a launch toward an unreachable or
	// unknown server creates nothing anywhere.
	code, _ = hub.do("POST", "/api/remote/nope/api/sessions/launch", map[string]any{"working_dir": hub.dir, "agent_type": "terminal"})
	assert.Equal(t, 404, code)
	b.stop()
	code, body = hub.do("POST", "/api/remote/b/api/sessions/launch", map[string]any{"working_dir": hub.dir, "agent_type": "terminal"})
	assert.Equal(t, 502, code, string(body))
	assert.Contains(t, string(body), "remote unreachable")
	all, _ = store.NewSessionStore(hub.db).GetAllLiveSessions(context.Background())
	assert.Empty(t, all, "no local fallback")
	assert.Empty(t, hub.backend.ListSessions())
}

// A remote's own 403 (here: demo limit reached, and a directory that does not
// exist) is an ordinary answer. It must reach the caller unchanged and must not
// mark the server "unauthorized" (regression for the proxy mapping every 403 to
// "remote rejected API key").
func TestHubE2E_Remote403IsNotAKeyFailure(t *testing.T) {
	env := newE2EEnv(t)
	hub := newE2EHub(t, env, "", true)
	a := newE2ERemote(t, "a", func(c *config.Config) { c.MaxLiveAgents = 1 })
	require.Equal(t, 201, hub.register("a", a.url(), a.key(), true).Code)

	hub.launchTerminal("a", a.work, "first")
	code, body := hub.do("POST", "/api/remote/a/api/sessions/launch", map[string]any{"working_dir": a.work, "agent_type": "terminal"})
	assert.Equal(t, 403, code, string(body))
	assert.Contains(t, string(body), "Demo limit")
	assert.NotContains(t, string(body), "rejected API key")

	code, body = hub.do("GET", "/api/remote/a/api/filesystem/list?path="+url.QueryEscape(filepath.Join(a.dir, "does-not-exist")), nil)
	assert.Equal(t, 403, code, string(body))
	assert.NotContains(t, string(body), "rejected API key")

	assert.Equal(t, "online", hub.serverStatus("a"))
	assert.Equal(t, 1, a.liveCount())
}

// ── 8. offline / online ─────────────────────────────────────────────

func TestHubE2E_OfflineMarksStaleAndRecovers(t *testing.T) {
	env := newE2EEnv(t)
	hub := newE2EHub(t, env, "", true)
	a := newE2ERemote(t, "a", nil)
	b := newE2ERemote(t, "b", nil)
	require.Equal(t, 201, hub.register("a", a.url(), a.key(), true).Code)
	require.Equal(t, 201, hub.register("b", b.url(), b.key(), true).Code)

	hubWork := filepath.Join(hub.dir, "w")
	require.NoError(t, os.MkdirAll(hubWork, 0o755))
	hub.launchTerminal("local", hubWork, "")
	a.seedSleeping("alpha", "a-alpha-1")
	b.seedSleeping("beta", "b-beta-1")
	hub.eventually("both remotes' agents visible and fresh", func() bool {
		v := hub.live()
		x, okA := v.find("a", "alpha")
		y, okB := v.find("b", "beta")
		return okA && okB && !x.Stale && !y.Stale
	})

	a.stop()
	hub.eventually("a offline: unreachable and its agent stale", func() bool {
		v := hub.live()
		x, ok := v.find("a", "alpha")
		return v.status("a") == "unreachable" && ok && x.Stale
	})
	v := hub.live()
	y, ok := v.find("b", "beta")
	assert.True(t, ok)
	assert.False(t, y.Stale, "other remote unaffected")
	assert.Equal(t, "online", v.status("b"))
	locals := 0
	for _, s := range v.Sessions {
		if s.Server == "local" {
			locals++
			assert.False(t, s.Stale)
		}
	}
	assert.Equal(t, 1, locals, "local agents unaffected")
	assert.Equal(t, "unreachable", hub.serverStatus("a"))
	code, body := hub.do("GET", "/api/remote/a/api/sessions/live", nil)
	assert.Equal(t, 502, code)
	assert.Contains(t, string(body), "remote unreachable")
	// Local API still fully works.
	assert.Equal(t, 200, hub.getJSON("/api/health", nil))

	// The same hub process recovers by itself: no reload, no re-registration.
	a.restart()
	hub.eventually("a recovered", func() bool {
		v := hub.live()
		x, ok := v.find("a", "alpha")
		return v.status("a") == "online" && ok && !x.Stale && hub.serverStatus("a") == "online"
	})
	code, _ = hub.do("GET", "/api/remote/a/api/sessions/live", nil)
	assert.Equal(t, 200, code)
}

// ── 9. proxy refusals ───────────────────────────────────────────────

func TestHubE2E_ProxyRefusals(t *testing.T) {
	env := newE2EEnv(t)
	hub := newE2EHub(t, env, "", true)
	a := newE2ERemote(t, "a", nil)
	require.Equal(t, 201, hub.register("a", a.url(), a.key(), true).Code)

	// Unregistered server ids, including attempts to smuggle a URL.
	for _, p := range []string{
		"/api/remote/unknown/api/health",
		"/api/remote/" + url.PathEscape(a.url()) + "/api/health",
		"/api/remote/" + url.PathEscape("a@127.0.0.1:1") + "/api/health",
	} {
		code, _ := hub.do("GET", p, nil)
		assert.Contains(t, []int{400, 404}, code, p)
	}

	// Path traversal and anything outside /api (or /ws for sockets).
	before := len(a.seen())
	for _, p := range []string{
		"/api/remote/a/api/../etc/passwd",
		"/api/remote/a/api/%2e%2e/health",
		"/api/remote/a/api/%2E%2E/%2e%2e/etc",
		"/api/remote/a/api/..%2fhealth",
		"/api/remote/a/api/%252e%252e/health",
		"/api/remote/a/api//health",
		"/api/remote/a/api/foo%5c..%5chealth",
	} {
		code, _ := hubRaw(t, hub, "GET", p)
		assert.Contains(t, []int{400, 404}, code, p)
	}
	for _, p := range []string{"/api/remote/a/static/app.js", "/api/remote/a/auth", "/api/remote/a/", "/api/remote/a/apix/health"} {
		code, _ := hubRaw(t, hub, "GET", p)
		assert.Contains(t, []int{403, 404, 400}, code, p)
	}
	assert.Len(t, a.seen(), before, "refused requests never reached the remote")

	// The browser's credentials never reach the remote; the remote key never
	// reaches the browser.
	req, _ := http.NewRequest("GET", hub.ts.URL+"/api/remote/a/api/sessions/live?api_key=browser-secret&x=1", nil)
	req.Header.Set("Authorization", "Bearer browser-secret")
	req.Header.Set("Cookie", "coral_session=browser-secret")
	code, _ := hub.send(req)
	require.Equal(t, 200, code)
	last := a.seen()[len(a.seen())-1]
	assert.Equal(t, "Bearer "+a.key(), last.Auth)
	assert.Empty(t, last.Cookie)
	assert.NotContains(t, last.RawQuery, "browser-secret")
	assert.Contains(t, last.RawQuery, "x=1")

	// Private/reserved addresses are refused at dial time unless allow_private
	// is set on that server. (Registration refuses them too, so insert the row
	// the way an older/edited DB could have it.)
	rs := store.NewRemoteServerStore(hub.db, hub.dir)
	_, err := rs.Add(context.Background(), "lan", "LAN", a.url(), a.key(), false)
	require.NoError(t, err)
	before = len(a.seen())
	code, body := hub.do("GET", "/api/remote/lan/api/health", nil)
	assert.Equal(t, 502, code, string(body))
	assert.Contains(t, string(body), "not allowed")
	assert.Len(t, a.seen(), before, "no connection was made to the private address")
	dcode, _ := hub.do("POST", "/api/servers/lan/test", nil)
	assert.Equal(t, 200, dcode)
	assert.Equal(t, "unreachable", hub.serverStatus("lan"), "private target is never contacted")
	assert.Len(t, a.seen(), before)
	// Editing allow_private through the registry API (the only way) enables it.
	pc, pb := hub.do("PATCH", "/api/servers/lan", map[string]any{"allow_private": true})
	require.Equal(t, 200, pc, string(pb))
	code, _ = hub.do("GET", "/api/remote/lan/api/health", nil)
	assert.Equal(t, 200, code)
	// Unauthenticated-as-hub-user header tricks do not enable it either.
	req, _ = http.NewRequest("POST", hub.ts.URL+"/api/servers", strings.NewReader(
		fmt.Sprintf(`{"id":"x","url":%q,"api_key":"k"}`, a.url())))
	req.Header.Set("X-Allow-Private", "1")
	code, _ = hub.send(req)
	assert.Equal(t, 400, code)
}

// hubRaw sends a request without normalising the path (Go's client keeps
// escapes and dot segments in req.URL.Opaque-free form via RawPath).
func hubRaw(t *testing.T, h *e2eHub, method, path string) (int, []byte) {
	t.Helper()
	u, err := url.Parse(h.ts.URL + path)
	require.NoError(t, err)
	req := &http.Request{Method: method, URL: u, Header: http.Header{}, Host: u.Host}
	return h.send(req)
}

// ── 10. key_unreadable ──────────────────────────────────────────────

func TestHubE2E_KeyUnreadableDoesNotBreakHub(t *testing.T) {
	env := newE2EEnv(t)
	hub := newE2EHub(t, env, "", true)
	a := newE2ERemote(t, "a", nil)
	b := newE2ERemote(t, "b", nil)
	require.Equal(t, 201, hub.register("a", a.url(), a.key(), true).Code)
	require.Equal(t, 201, hub.register("b", b.url(), b.key(), true).Code)
	a.seedSleeping("alpha", "a-alpha-1")
	b.seedSleeping("beta", "b-beta-1")
	hub.eventually("both online", func() bool {
		_, okA := hub.live().find("a", "alpha")
		_, okB := hub.live().find("b", "beta")
		return okA && okB
	})

	// (1) Ciphertext moved between rows (AAD binding): a's row now holds b's
	// ciphertext. Only affected servers become key_unreadable; others work.
	var encB string
	require.NoError(t, hub.db.Get(&encB, "SELECT api_key FROM remote_servers WHERE id='b'"))
	_, err := hub.db.Exec("UPDATE remote_servers SET api_key=? WHERE id='a'", encB)
	require.NoError(t, err)
	code, body := hub.do("GET", "/api/remote/a/api/sessions/live", nil)
	assert.Equal(t, 502, code, string(body))
	assert.Contains(t, string(body), "remote key unreadable")
	assert.Equal(t, "key_unreadable", hub.serverStatus("a"))
	code, _ = hub.do("GET", "/api/remote/b/api/sessions/live", nil)
	assert.Equal(t, 200, code, "b is unaffected")
	hub.eventually("poller marks a key_unreadable, b stays online", func() bool {
		v := hub.live()
		x, ok := v.find("a", "alpha")
		return v.status("a") == "key_unreadable" && ok && x.Stale && v.status("b") == "online"
	})
	code, _ = hub.do("POST", "/api/servers/a/test", nil)
	assert.Equal(t, 200, code)
	assert.Equal(t, "key_unreadable", hub.serverStatus("a"))
	assert.Equal(t, 200, hub.getJSON("/api/health", nil), "hub still serves")

	// (2) Re-entering the key re-encrypts it and recovers, no restart.
	code, body = hub.do("PATCH", "/api/servers/a", map[string]any{"api_key": a.key()})
	require.Equal(t, 200, code, string(body))
	hub.eventually("a recovered after key re-entry", func() bool { return hub.live().status("a") == "online" })

	// (3) Missing master key file with existing encrypted rows: after a hub
	// restart the keys are unreadable (no silent new key), hub stays up.
	dir := hub.dir
	hub.close()
	require.NoError(t, os.Remove(filepath.Join(dir, ".remote_secret_key")))
	hub2 := newE2EHub(t, env, dir, true)
	var list []map[string]any
	require.Equal(t, 200, hub2.getJSON("/api/servers", &list))
	code, body = hub2.do("POST", "/api/servers/a/test", nil)
	assert.Equal(t, 200, code, string(body))
	code, _ = hub2.do("POST", "/api/servers/b/test", nil)
	assert.Equal(t, 200, code)
	assert.Equal(t, "key_unreadable", hub2.serverStatus("a"))
	assert.Equal(t, "key_unreadable", hub2.serverStatus("b"))
	_, statErr := os.Stat(filepath.Join(dir, ".remote_secret_key"))
	assert.True(t, os.IsNotExist(statErr), "no key silently regenerated while encrypted rows exist")
	code, _ = hub2.do("GET", "/api/remote/b/api/sessions/live", nil)
	assert.Equal(t, 502, code)
	hub2.eventually("poller reports key_unreadable without crashing", func() bool {
		v := hub2.live()
		return v.status("a") == "key_unreadable" && v.status("b") == "key_unreadable"
	})
	assert.Equal(t, 200, hub2.getJSON("/api/health", nil))
	hub2.close()

	// (4) A wrong master key (CORAL_SECRET_KEY override) behaves the same.
	t.Setenv("CORAL_SECRET_KEY", strings.Repeat("ab", 32))
	hub3 := newE2EHub(t, env, dir, true)
	code, _ = hub3.do("POST", "/api/servers/a/test", nil)
	assert.Equal(t, 200, code)
	assert.Equal(t, "key_unreadable", hub3.serverStatus("a"))
	assert.Equal(t, 200, hub3.getJSON("/api/health", nil))

	env.assertNoPlaintext(t, dir, a.key(), b.key())
}

// ── terminal stack: real PTY when spawning is permitted, else a fake ─

type termStack struct {
	backend  ptymanager.TerminalBackend
	terminal ptymanager.SessionTerminal
	fake     *fakeBackend
}

var (
	ptyProbeOnce sync.Once
	ptyUsable    bool
)

func realPTYUsable() bool {
	ptyProbeOnce.Do(func() {
		// Spawn exactly what a launch spawns (a shell on a new PTY with its own
		// session); plain fork/exec being allowed is not enough.
		b := ptymanager.NewPTYBackend()
		if b.Spawn("probe", "terminal", os.TempDir(), "probe-"+fmt.Sprint(time.Now().UnixNano()), "", 80, 24) == nil {
			ptyUsable = true
			_ = b.Kill("probe")
		}
	})
	return ptyUsable
}

func newTermStack(t *testing.T) termStack {
	t.Helper()
	if realPTYUsable() {
		b := ptymanager.NewPTYBackend()
		return termStack{backend: b, terminal: ptymanager.NewPTYSessionTerminal(b)}
	}
	f := &fakeBackend{sessions: map[string]*fakeSession{}, logDir: t.TempDir()}
	return termStack{backend: f, terminal: &fakeTerminal{f: f}, fake: f}
}

type fakeSession struct {
	agentType, sessionID, dir string
	subs                      map[string]chan []byte
	input                     strings.Builder
	pending                   string
}

// fakeBackend is an in-memory ptymanager.TerminalBackend. Its "shell" answers
// each complete input line of the form `echo TEXT` with TEXT (expanding
// $((A+B))) and echoes nothing else.
type fakeBackend struct {
	mu       sync.Mutex
	sessions map[string]*fakeSession
	logDir   string
}

var arithRe = regexp.MustCompile(`\$\(\((\d+)\+(\d+)\)\)`)

func (f *fakeBackend) Spawn(name, agentType, workDir, sessionID, command string, cols, rows uint16) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.sessions[name]; ok {
		return fmt.Errorf("session %q already exists", name)
	}
	f.sessions[name] = &fakeSession{agentType: agentType, sessionID: sessionID, dir: workDir, subs: map[string]chan []byte{}}
	return nil
}

func (f *fakeBackend) Kill(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[name]
	if !ok {
		return fmt.Errorf("session %q not found", name)
	}
	for _, ch := range s.subs {
		close(ch)
	}
	delete(f.sessions, name)
	return nil
}

func (f *fakeBackend) Restart(name, command string) error { return nil }

func (f *fakeBackend) SendInput(name string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[name]
	if !ok {
		return fmt.Errorf("session %q not found", name)
	}
	s.input.Write(data)
	s.pending += string(data)
	for {
		i := strings.IndexByte(s.pending, '\n')
		if i < 0 {
			return nil
		}
		line := strings.TrimSpace(s.pending[:i])
		s.pending = s.pending[i+1:]
		if rest, ok := strings.CutPrefix(line, "echo "); ok {
			rest = arithRe.ReplaceAllStringFunc(rest, func(m string) string {
				g := arithRe.FindStringSubmatch(m)
				var x, y int
				fmt.Sscan(g[1], &x)
				fmt.Sscan(g[2], &y)
				return fmt.Sprint(x + y)
			})
			for _, ch := range s.subs {
				select {
				case ch <- []byte(rest + "\r\n"):
				default:
				}
			}
		}
	}
}

func (f *fakeBackend) Resize(name string, cols, rows uint16) error { return nil }

func (f *fakeBackend) Attach(name, subscriberID string) (<-chan []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[name]
	if !ok {
		return nil, fmt.Errorf("session %q not found", name)
	}
	ch := make(chan []byte, 64)
	s.subs[subscriberID] = ch
	return ch, nil
}

func (f *fakeBackend) Unsubscribe(name, subscriberID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sessions[name]; ok {
		delete(s.subs, subscriberID)
	}
}

func (f *fakeBackend) Replay(name string) ([]byte, error) { return nil, nil }

func (f *fakeBackend) ListSessions() []ptymanager.SessionInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ptymanager.SessionInfo
	for n, s := range f.sessions {
		out = append(out, ptymanager.SessionInfo{AgentName: n, AgentType: s.agentType, SessionID: s.sessionID, WorkingDir: s.dir, Running: true})
	}
	return out
}

func (f *fakeBackend) IsRunning(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.sessions[name]
	return ok
}

func (f *fakeBackend) LogPath(name string) string { return filepath.Join(f.logDir, name+".log") }

func (f *fakeBackend) Close() error { return nil }

func (f *fakeBackend) subscribers(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sessions[name]; ok {
		return len(s.subs)
	}
	return 0
}

// fakeTerminal implements the parts of ptymanager.SessionTerminal the hub
// flows touch; anything else panics (caught as a 500 by the Recoverer) so a
// test never silently depends on an unimplemented operation.
type fakeTerminal struct {
	ptymanager.SessionTerminal
	f *fakeBackend
}

func (t *fakeTerminal) ListSessions(context.Context) ([]ptymanager.PaneInfo, error) {
	var out []ptymanager.PaneInfo
	for _, s := range t.f.ListSessions() {
		out = append(out, ptymanager.PaneInfo{PaneTitle: s.AgentName, SessionName: s.AgentName, Target: s.SessionID, CurrentPath: s.WorkingDir})
	}
	return out, nil
}

func (t *fakeTerminal) HasSession(_ context.Context, name string) bool { return t.f.IsRunning(name) }

func (t *fakeTerminal) FindSession(ctx context.Context, name, agentType, sessionID string) (*ptymanager.PaneInfo, error) {
	panes, _ := t.ListSessions(ctx)
	for i := range panes {
		if panes[i].SessionName == name || panes[i].Target == sessionID {
			return &panes[i], nil
		}
	}
	return nil, nil
}
