package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/pkg/secretbox"
	"github.com/cdknorow/coral/pkg/tunnel"
)

const fakeToken = "coralhub_srv_abcd1234_" + "x0123456789012345678901234567890123456789012"

// fakeHub implements the hub endpoints the connector uses.
type fakeHub struct {
	srv       *httptest.Server
	hits      atomic.Int64 // every request, any path
	tunnels   atomic.Int64 // /tunnel attempts
	approveAt atomic.Int64 // poll calls before success (pending until reached)
	polls     atomic.Int64
	revoked   atomic.Bool
	sessions  chan *tunnel.Session
}

func newFakeHub(t *testing.T) *fakeHub {
	t.Helper()
	h := &fakeHub{sessions: make(chan *tunnel.Session, 16)}
	h.approveAt.Store(2)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/enroll/start", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"device_code": strings.Repeat("d", 43), "user_code": "ABCD-EFGH",
			"verification_url": h.srv.URL + "/enroll?code=ABCD-EFGH", "interval": 2, "expires_in": 600,
		})
	})
	mux.HandleFunc("POST /api/enroll/poll", func(w http.ResponseWriter, r *http.Request) {
		if h.polls.Add(1) < h.approveAt.Load() {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"authorization_pending"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"server_token": fakeToken, "server_id": "srv_1", "label": "My Laptop"})
	})
	mux.HandleFunc("GET /tunnel", func(w http.ResponseWriter, r *http.Request) {
		h.tunnels.Add(1)
		s, err := tunnel.AcceptHTTP(w, r, func(_ context.Context, tok string) (any, error) {
			if h.revoked.Load() || tok != fakeToken {
				return nil, tunnel.ErrUnauthorized
			}
			return "srv_1", nil
		}, tunnel.SessionConfig{})
		if err == nil {
			h.sessions <- s
		}
	})
	// TLS, like a real hub: release builds refuse a cleartext hub, so these tests run in both build modes.
	h.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.hits.Add(1)
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *fakeHub) session(t *testing.T) *tunnel.Session {
	t.Helper()
	select {
	case s := <-h.sessions:
		t.Cleanup(func() { s.Close() })
		return s
	case <-time.After(10 * time.Second):
		t.Fatal("connector did not connect")
		return nil
	}
}

// localAPI records what reaches the "local Coral API".
type localAPI struct {
	srv   *httptest.Server
	mu    sync.Mutex
	seen  []http.Header
	paths []string
}

func newLocalAPI(t *testing.T) *localAPI {
	l := &localAPI{}
	l.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		l.seen, l.paths = append(l.seen, r.Header.Clone()), append(l.paths, r.Method+" "+r.URL.Path)
		l.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(l.srv.Close)
	return l
}

func (l *localAPI) count() int { l.mu.Lock(); defer l.mu.Unlock(); return len(l.paths) }

type env struct {
	db    *store.DB
	cs    *store.CloudStore
	hub   *fakeHub
	local *localAPI
	logs  *bytes.Buffer
	mode  atomic.Int32 // the local maximum access mode
}

func newEnv(t *testing.T) *env {
	t.Helper()
	t.Setenv(secretbox.EnvKey, "")
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	rs := store.NewRemoteServerStore(db, dir)
	e := &env{db: db, cs: store.NewCloudStore(db, rs), hub: newFakeHub(t), local: newLocalAPI(t), logs: &bytes.Buffer{}}
	e.mode.Store(int32(tunnel.TierView))
	return e
}

func (e *env) manager(hubURL string) *Manager {
	return New(Options{
		HubURL: hubURL, HTTPClient: e.hub.srv.Client(), Store: e.cs, LocalBaseURL: e.local.srv.URL, MaxMode: func() tunnel.Mode { return tunnel.Mode(e.mode.Load()) },
		CoralVersion: "test", PollInterval: 20 * time.Millisecond,
		Backoff: tunnel.Backoff{Min: 20 * time.Millisecond, Max: 100 * time.Millisecond},
		Logger:  slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelInfo})),
	})
}

func start(t *testing.T, m *Manager) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.root != nil || m.o.HubURL == "" }, 2*time.Second, 5*time.Millisecond)
	return cancel
}

func waitState(t *testing.T, m *Manager, want string) Status {
	t.Helper()
	var st Status
	require.Eventually(t, func() bool { st = m.Status(); return st.State == want }, 10*time.Second, 10*time.Millisecond,
		"want state %s, last %+v", want, st)
	return st
}

func viaTunnel(t *testing.T, s *tunnel.Session, method, path string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, "http://hub.invalid"+path, nil)
	resp, err := s.RoundTrip(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// ── tests ───────────────────────────────────────────────────────────

func TestOffMakesNoOutboundConnection(t *testing.T) {
	e := newEnv(t)
	m := e.manager("") // no --cloud-url: connector off
	start(t, m)
	require.Equal(t, StateOff, m.Status().State)
	require.ErrorIs(t, m.Connect(context.Background()), ErrNotConfigured)
	require.ErrorIs(t, m.Disconnect(context.Background()), ErrNotConfigured)
	time.Sleep(150 * time.Millisecond)
	require.Zero(t, e.hub.hits.Load(), "an off connector must never dial")
}

func TestEnrollConnectAndServeAllowedRoutes(t *testing.T) {
	e := newEnv(t)
	m := e.manager(e.hub.srv.URL)
	start(t, m)
	require.Equal(t, StateIdle, m.Status().State)
	require.Zero(t, e.hub.hits.Load(), "configured but not enrolled: still no dial until the user connects")

	require.NoError(t, m.Connect(context.Background()))
	st := m.Status()
	require.Equal(t, StateEnrolling, st.State)
	require.Equal(t, "ABCD-EFGH", st.UserCode)
	require.Equal(t, e.hub.srv.URL+"/enroll?code=ABCD-EFGH", st.VerificationURL)
	require.NotContains(t, mustJSON(st), fakeToken)

	waitState(t, m, StateConnected)
	st = m.Status()
	require.Equal(t, "My Laptop", st.Label)
	require.Equal(t, "view", st.MaxMode)

	// The token is stored sealed, never as plaintext.
	var raw string
	require.NoError(t, e.db.Get(&raw, "SELECT token FROM cloud_connection WHERE id = 1"))
	require.True(t, strings.HasPrefix(raw, "v1:"), raw)
	require.NotContains(t, raw, fakeToken)

	s := e.hub.session(t)
	// Allowed read.
	code, body := viaTunnel(t, s, "GET", "/api/health")
	require.Equal(t, 200, code, body)
	require.Equal(t, 1, e.local.count())
	e.local.mu.Lock()
	h := e.local.seen[0]
	e.local.mu.Unlock()
	require.Equal(t, "1", h.Get(tunnel.ViaHeader), "forwarded requests carry the tunnel stamp")
	require.Empty(t, h.Get("Authorization"))

	// Never-routes and routes above the mode never reach the local API (default mode: view).
	for _, p := range [][2]string{{"GET", "/api/settings"}, {"PUT", "/api/settings"}, {"GET", "/api/system/api-key"},
		{"POST", "/api/sessions/live/a/send"}, {"POST", "/api/sessions/launch"}, {"GET", "/api/filesystem/list"},
		{"GET", "/api/cloud/status"}, {"GET", "/api/sessions/live/a/file-content?filepath=/etc/passwd"}} {
		code, _ := viaTunnel(t, s, p[0], p[1])
		require.NotEqual(t, 200, code, "%s %s", p[0], p[1])
	}
	require.Equal(t, 1, e.local.count(), "denied requests must not reach the local server")

	// Raising the local mode takes effect on the next request; lowering it takes effect just as fast.
	e.mode.Store(int32(tunnel.TierInteract))
	code, _ = viaTunnel(t, s, "POST", "/api/sessions/live/a/send")
	require.Equal(t, 200, code)
	code, _ = viaTunnel(t, s, "POST", "/api/sessions/launch")
	require.NotEqual(t, 200, code, "interact does not include launch")
	e.mode.Store(int32(tunnel.TierLaunch))
	code, _ = viaTunnel(t, s, "GET", "/api/filesystem/list")
	require.Equal(t, 200, code)
	e.mode.Store(int32(tunnel.TierView))
	code, _ = viaTunnel(t, s, "GET", "/api/filesystem/list")
	require.NotEqual(t, 200, code)
	code, _ = viaTunnel(t, s, "POST", "/api/sessions/live/a/send")
	require.NotEqual(t, 200, code)

	// The hub being reachable and quiet produces no log noise at info level.
	require.NotContains(t, e.logs.String(), fakeToken)
}

func TestReconnectsAfterADrop(t *testing.T) {
	e := newEnv(t)
	m := e.manager(e.hub.srv.URL)
	start(t, m)
	require.NoError(t, m.Connect(context.Background()))
	waitState(t, m, StateConnected)
	first := e.hub.session(t)

	first.Close() // the hub (or the network) drops the tunnel
	waitState(t, m, StateRetrying)
	second := e.hub.session(t)
	waitState(t, m, StateConnected)
	code, _ := viaTunnel(t, second, "GET", "/api/health")
	require.Equal(t, 200, code)
}

func TestRevokedTokenStopsRetriesAndIsRemembered(t *testing.T) {
	e := newEnv(t)
	m := e.manager(e.hub.srv.URL)
	start(t, m)
	require.NoError(t, m.Connect(context.Background()))
	waitState(t, m, StateConnected)
	s := e.hub.session(t)

	e.hub.revoked.Store(true) // revoke on the hub, then the tunnel drops
	s.Close()
	st := waitState(t, m, StateRevoked)
	require.Contains(t, st.LastError, "revoked")

	n := e.hub.tunnels.Load()
	time.Sleep(400 * time.Millisecond) // many backoff periods
	require.Equal(t, n, e.hub.tunnels.Load(), "a revoked connector must stop retrying")

	conn, err := e.cs.Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, store.CloudStateRevoked, conn.State)

	// A restart does not dial a revoked enrollment either.
	m2 := e.manager(e.hub.srv.URL)
	start(t, m2)
	require.Equal(t, StateRevoked, m2.Status().State)
	time.Sleep(150 * time.Millisecond)
	require.Equal(t, n, e.hub.tunnels.Load())
}

func TestDisconnectForgetsTheTokenAndStops(t *testing.T) {
	e := newEnv(t)
	m := e.manager(e.hub.srv.URL)
	start(t, m)
	require.NoError(t, m.Connect(context.Background()))
	waitState(t, m, StateConnected)
	s := e.hub.session(t)

	require.NoError(t, m.Disconnect(context.Background()))
	require.Equal(t, StateIdle, m.Status().State)
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("tunnel still up after Disconnect")
	}
	_, err := e.cs.Get(context.Background())
	require.ErrorIs(t, err, store.ErrNoCloudConnection)
	n := e.hub.tunnels.Load()
	time.Sleep(300 * time.Millisecond)
	require.Equal(t, n, e.hub.tunnels.Load(), "no reconnect after Disconnect")
}

func TestAlreadyEnrolledConnectsOnStartup(t *testing.T) {
	e := newEnv(t)
	require.NoError(t, e.cs.Save(context.Background(), e.hub.srv.URL, "srv_1", "My Laptop", fakeToken))
	m := e.manager(e.hub.srv.URL)
	start(t, m)
	waitState(t, m, StateConnected)
}

func TestUnreachableHubNeverBreaksLocalUseAndStaysQuiet(t *testing.T) {
	e := newEnv(t)
	dead := httptest.NewTLSServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close() // nothing listens there
	require.NoError(t, e.cs.Save(context.Background(), deadURL, "srv_1", "x", fakeToken))
	m := e.manager(deadURL)
	start(t, m)
	waitState(t, m, StateRetrying)
	// Local API is untouched and the manager never blocked the caller.
	resp, err := http.Get(e.local.srv.URL + "/api/health")
	require.NoError(t, err)
	resp.Body.Close()
	require.NotContains(t, e.logs.String(), "level=ERROR")
	require.NotContains(t, e.logs.String(), "level=WARN")
}

func TestEnrollmentMismatchedHubIsNotDialed(t *testing.T) {
	e := newEnv(t)
	other := newFakeHub(t)
	require.NoError(t, e.cs.Save(context.Background(), other.srv.URL, "srv_1", "x", fakeToken))
	m := e.manager(e.hub.srv.URL) // flag points somewhere else
	start(t, m)
	require.Equal(t, StateIdle, m.Status().State)
	require.Contains(t, m.Status().LastError, "different hub")
	time.Sleep(150 * time.Millisecond)
	require.Zero(t, other.hits.Load())
	require.Zero(t, e.hub.hits.Load())
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestHubLearnsTheLocalModeAndSeesItChange(t *testing.T) {
	e := newEnv(t)
	m := e.manager(e.hub.srv.URL)
	start(t, m)
	require.NoError(t, m.Connect(context.Background()))
	waitState(t, m, StateConnected)
	first := e.hub.session(t)
	require.Equal(t, tunnel.TierView, first.LocalMode())

	e.mode.Store(int32(tunnel.TierInteract))
	m.ModeChanged(context.Background()) // the settings route calls this
	second := e.hub.session(t)          // the connector reconnected to report it
	require.Equal(t, tunnel.TierInteract, second.LocalMode())
	waitState(t, m, StateConnected)
}
