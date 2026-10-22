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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/pkg/secretbox"
	"github.com/subgentic/coral/pkg/tunnel"
)

const fakeToken = "coralhub_srv_abcd1234_" + "x0123456789012345678901234567890123456789012"

// fakeHub implements the hub endpoints the connector uses.
type fakeHub struct {
	srv         *httptest.Server
	hits        atomic.Int64 // every request, any path
	tunnels     atomic.Int64 // /tunnel attempts
	approveAt   atomic.Int64 // poll calls before success (pending until reached)
	polls       atomic.Int64
	revoked     atomic.Bool
	deny        atomic.Bool  // poll answers access_denied
	expireAfter atomic.Int64 // >0: polls from this one on answer expired_token
	busy        atomic.Int64 // >0: start answers 429 rate_limited with this Retry-After
	starts      atomic.Int64 // POST /api/enroll/start requests
	down        atomic.Bool  // start answers 503
	sessions    chan *tunnel.Session

	routeKey  atomic.Value // string: the route key Welcome carries (#3683); unset: none
	account   atomic.Value // string: the account the enrollment reply names (#3966); unset: none
	ownerUser atomic.Value // string: the owner's hub-user label the enrollment reply names (#5280); unset: none
	signin    atomic.Value // signinFunc: answers Hub sign-in (#3966); unset: not advertised
	onMode    atomic.Value // func(tunnel.Mode): takes the local mode message (#5918); unset: local_mode/1 is not advertised
	routeMu   sync.Mutex
	routeSeen []string // the X-Coral-Route header of each /tunnel dial
}

type signinFunc func(tunnel.SigninRequest) tunnel.SigninResponse

func (h *fakeHub) routeHeaders() []string {
	h.routeMu.Lock()
	defer h.routeMu.Unlock()
	return append([]string(nil), h.routeSeen...)
}

func newFakeHub(t *testing.T) *fakeHub {
	t.Helper()
	h := &fakeHub{sessions: make(chan *tunnel.Session, 16)}
	h.approveAt.Store(2)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/enroll/start", func(w http.ResponseWriter, r *http.Request) {
		h.starts.Add(1)
		if h.down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if n := h.busy.Load(); n > 0 {
			w.Header().Set("Retry-After", strconv.FormatInt(n, 10))
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"rate_limited"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"device_code": strings.Repeat("d", 43), "user_code": "ABCD-EFGH",
			"verification_url": h.srv.URL + "/enroll?code=ABCD-EFGH", "interval": 2, "expires_in": 600,
		})
	})
	mux.HandleFunc("POST /api/enroll/poll", func(w http.ResponseWriter, r *http.Request) {
		if n := h.expireAfter.Load(); n > 0 && h.polls.Load()+1 >= n {
			h.polls.Add(1)
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"expired_token"}`))
			return
		}
		if h.deny.Load() {
			h.polls.Add(1)
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"access_denied"}`))
			return
		}
		if h.polls.Add(1) < h.approveAt.Load() {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"authorization_pending"}`))
			return
		}
		reply := map[string]any{"server_token": fakeToken, "server_id": "srv_1", "label": "My Laptop"}
		if a, ok := h.account.Load().(string); ok {
			reply["account"] = a
		}
		if u, ok := h.ownerUser.Load().(string); ok {
			reply["owner_user"] = u
		}
		json.NewEncoder(w).Encode(reply)
	})
	mux.HandleFunc("GET /tunnel", func(w http.ResponseWriter, r *http.Request) {
		h.tunnels.Add(1)
		h.routeMu.Lock()
		h.routeSeen = append(h.routeSeen, r.Header.Get(tunnel.RouteHeader))
		h.routeMu.Unlock()
		s, err := tunnel.AcceptHTTP(w, r, func(_ context.Context, tok string) (any, error) {
			if h.revoked.Load() || tok != fakeToken {
				return nil, tunnel.ErrUnauthorized
			}
			return "srv_1", nil
		}, tunnel.SessionConfig{
			RouteKey: func(any) string { k, _ := h.routeKey.Load().(string); return k },
			Capabilities: func(any) []string {
				var caps []string
				if _, ok := h.signin.Load().(signinFunc); ok {
					caps = append(caps, tunnel.CapabilitySignin)
				}
				if _, ok := h.onMode.Load().(func(tunnel.Mode)); ok {
					caps = append(caps, tunnel.CapabilityLocalMode)
				}
				return caps
			},
			MinLocalModeGap: 100 * time.Millisecond, // the real hub's is a second; a test holds its own messages to 300 ms
			OnLocalMode: func(_ context.Context, _ *tunnel.Session, mode tunnel.Mode) {
				if f, ok := h.onMode.Load().(func(tunnel.Mode)); ok {
					f(mode)
				}
			},
			OnSignin: func(ctx context.Context, s *tunnel.Session, req tunnel.SigninRequest) tunnel.SigninResponse {
				return h.signin.Load().(signinFunc)(req)
			},
		})
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
	db, err := storetest.Open(t, filepath.Join(dir, "sessions.db"))
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
	if m.o.HubURL == "" {
		return cancel // Run returns at once: nothing to wait for
	}
	// Wait for Run to have read the stored enrollment (m.loaded), not for a moment: the tests
	// read the state it sets right after (#3778: m.root is set before that, and testify 1.12's
	// Eventually no longer waits a tick before its first check, which had hidden the gap).
	select {
	case <-m.loaded:
	case <-time.After(5 * time.Second):
		t.Fatal("the manager never read its stored enrollment")
	}
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

// Release builds configure https://app.coralhub.dev by default (#3221). A configured hub is
// only an address: a server that has never been connected sends nothing to it, however
// long it runs, until the user clicks Connect.
func TestConfiguredButNeverConnectedNeverDials(t *testing.T) {
	e := newEnv(t)
	m := e.manager(e.hub.srv.URL)
	start(t, m)
	require.Equal(t, StateIdle, m.Status().State)
	time.Sleep(500 * time.Millisecond)
	require.Zero(t, e.hub.hits.Load(), "no request to the hub before Connect")
	require.NoError(t, m.Connect(context.Background()))
	require.Eventually(t, func() bool { return e.hub.hits.Load() > 0 }, 5*time.Second, 20*time.Millisecond, "Connect reaches the hub")
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
	require.Equal(t, ViaSecret(), h.Get(tunnel.ViaHeader), "forwarded requests carry the per-process tunnel stamp")
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
	// It says which host it was enrolled with and how to move (#3546: the hub moved hosts).
	require.Contains(t, m.Status().LastError, other.srv.URL)
	require.Contains(t, m.Status().LastError, "coral cloud connect --force")
	require.Equal(t, ErrCodeHubMoved, m.Status().LastErrorCode, "a code the dashboard can show (#3546)")
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

// Whatever the hub turned on ends when the link does (#3182): OnLinkLost runs
// when the user disconnects and when the hub revokes this server.
func TestLinkLostRunsOnDisconnectAndRevoke(t *testing.T) {
	for _, how := range []string{"disconnect", "revoke"} {
		t.Run(how, func(t *testing.T) {
			e := newEnv(t)
			m := e.manager(e.hub.srv.URL)
			var lost atomic.Int32
			m.o.OnLinkLost = func(context.Context) { lost.Add(1) }
			start(t, m)
			require.NoError(t, m.Connect(context.Background()))
			waitState(t, m, StateConnected)
			s := e.hub.session(t)
			require.Zero(t, lost.Load())
			if how == "disconnect" {
				require.NoError(t, m.Disconnect(context.Background()))
			} else {
				e.hub.revoked.Store(true)
				s.Close()
				waitState(t, m, StateRevoked)
			}
			require.Eventually(t, func() bool { return lost.Load() == 1 }, 5*time.Second, 10*time.Millisecond)
		})
	}
}

// #3285: right after startup an enrolled server still looks idle until Run has
// read its enrollment. Status says so (loading), and Connect waits instead of
// starting a new enrollment on top of the stored one.
func TestConnectWaitsForTheStoredEnrollment(t *testing.T) {
	e := newEnv(t)
	m := e.manager(e.hub.srv.URL)
	require.True(t, m.Status().Loading)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, m.Connect(ctx), context.DeadlineExceeded, "no enrollment before the connector has loaded")
	require.Zero(t, e.hub.hits.Load(), "and no dial")
	start(t, m)
	require.Eventually(t, func() bool { return !m.Status().Loading }, 2*time.Second, 5*time.Millisecond)
	require.False(t, New(Options{}).Status().Loading, "no hub: nothing to load")
}

// #3285: Ctrl-C in 'coral cloud connect' cancels an enrollment in progress (the
// code is dropped, nothing is linked) and never undoes a connection.
func TestCancelEnrollmentOnlyCancelsAnEnrollment(t *testing.T) {
	e := newEnv(t)
	e.hub.approveAt.Store(1 << 30) // stays pending
	m := e.manager(e.hub.srv.URL)
	start(t, m)
	require.NoError(t, m.Connect(context.Background()))
	require.Equal(t, StateEnrolling, m.Status().State)
	m.CancelEnrollment(context.Background())
	st := m.Status()
	require.Equal(t, StateIdle, st.State)
	require.Empty(t, st.UserCode, "the code is dropped")
	require.Equal(t, "enrollment cancelled", st.LastError)

	e.hub.approveAt.Store(2)
	require.NoError(t, m.Connect(context.Background()))
	waitState(t, m, StateConnected)
	m.CancelEnrollment(context.Background())
	require.Equal(t, StateConnected, m.Status().State, "a connection is never undone")
}

// coral-hub's contract (v1.4.16): a denied request ends the enrollment at once with a
// clear reason, instead of being retried as a hub error.
func TestEnrollmentAccessDeniedStopsAtOnce(t *testing.T) {
	e := newEnv(t)
	e.hub.deny.Store(true)
	m := e.manager(e.hub.srv.URL)
	start(t, m)
	require.NoError(t, m.Connect(context.Background()))
	waitState(t, m, StateIdle)
	require.Equal(t, "the request was denied in Coral Hub", m.Status().LastError)
	require.Equal(t, ErrCodeAccessDenied, m.Status().LastErrorCode)
	require.EqualValues(t, 1, e.hub.polls.Load(), "no more polls after the denial")
}

// A rate-limited start says how long to wait, from the hub's Retry-After.
func TestEnrollmentStartRateLimited(t *testing.T) {
	e := newEnv(t)
	e.hub.busy.Store(90)
	m := e.manager(e.hub.srv.URL)
	start(t, m)
	err := m.Connect(context.Background())
	var rl *RateLimitedError
	require.ErrorAs(t, err, &rl)
	require.Equal(t, 90*time.Second, rl.RetryAfter)
	st := m.Status()
	require.Equal(t, StateIdle, st.State)
	require.Equal(t, "Coral Hub is busy: try again in 1m30s", st.LastError)
	require.Equal(t, ErrCodeRateLimited, st.LastErrorCode)
}

// coral-hub's contract: a user outside the closed beta can't sign in, so the code just
// expires (no distinct answer, by design). The expiry says so, with a code the UI keys on;
// a later state clears the code.
func TestEnrollmentExpiredSaysClosedBeta(t *testing.T) {
	e := newEnv(t)
	e.hub.approveAt.Store(1 << 30) // stays pending
	e.hub.expireAfter.Store(1)
	m := e.manager(e.hub.srv.URL)
	start(t, m)
	require.NoError(t, m.Connect(context.Background()))
	waitState(t, m, StateIdle)
	st := m.Status()
	require.Equal(t, ErrCodeExpired, st.LastErrorCode)
	require.Equal(t, "The code expired before it was approved. If you couldn't sign in at app.coralhub.dev, Coral Hub is in closed beta: request access at https://coralhub.dev/hub.html", st.LastError)

	// Right after an expiry a new code waits for the per-machine gap (#3285 follow-up)…
	var rl *RateLimitedError
	require.ErrorAs(t, m.Connect(context.Background()), &rl)
	require.True(t, rl.Local)
	// …as if the 10 minutes had passed:
	require.NoError(t, e.cs.ClearEnrollment(context.Background()))
	e.hub.expireAfter.Store(0)
	e.hub.approveAt.Store(0)
	require.NoError(t, m.Connect(context.Background()))
	waitState(t, m, StateConnected)
	require.Empty(t, m.Status().LastErrorCode)
}

// #3285 follow-up: a machine in a restart loop reuses its pending code: 20 restarts in a
// row ask the hub for ONE device code.
func TestEnrollmentReusesThePendingCodeAcrossRestarts(t *testing.T) {
	e := newEnv(t)
	e.hub.approveAt.Store(1 << 30) // stays pending
	var codes []string
	for i := 0; i < 20; i++ {
		m := e.manager(e.hub.srv.URL)
		stop := start(t, m)
		require.NoError(t, m.Connect(context.Background()))
		st := m.Status()
		require.Equal(t, StateEnrolling, st.State)
		codes = append(codes, st.UserCode)
		stop()
	}
	assert.EqualValues(t, 1, e.hub.starts.Load(), "one enroll/start for 20 restarts")
	for _, c := range codes {
		assert.Equal(t, codes[0], c, "the same code every time")
	}
}

// A hub that's down: the next start waits a growing, jittered backoff, across restarts,
// without asking the hub meanwhile; the hub's Retry-After is the least wait.
func TestEnrollmentStartBacksOff(t *testing.T) {
	e := newEnv(t)
	e.hub.down.Store(true)
	ctx := context.Background()
	m := e.manager(e.hub.srv.URL)
	start(t, m)
	require.ErrorIs(t, m.Connect(ctx), ErrHubUnavailable)
	require.EqualValues(t, 1, e.hub.starts.Load())
	en, err := e.cs.GetEnrollment(ctx, e.hub.srv.URL)
	require.NoError(t, err)
	wait := time.Until(en.NextStartAt)
	assert.True(t, wait > 25*time.Second && wait <= 37*time.Second, "first backoff ~30 s (+jitter): %s", wait)

	// A restart doesn't reset it, and nothing reaches the hub.
	m2 := e.manager(e.hub.srv.URL)
	start(t, m2)
	var rl *RateLimitedError
	require.ErrorAs(t, m2.Connect(ctx), &rl)
	assert.True(t, rl.Local)
	assert.EqualValues(t, 1, e.hub.starts.Load())

	// The next failure doubles it.
	en.NextStartAt = time.Now().Add(-time.Second)
	require.NoError(t, e.cs.SaveEnrollment(ctx, en))
	require.ErrorIs(t, m2.Connect(ctx), ErrHubUnavailable)
	en, _ = e.cs.GetEnrollment(ctx, e.hub.srv.URL)
	assert.Equal(t, 2, en.Failures)
	wait = time.Until(en.NextStartAt)
	assert.True(t, wait > 55*time.Second && wait <= 73*time.Second, "second backoff ~60 s: %s", wait)

	// A 429 with a longer Retry-After than the backoff: the Retry-After wins.
	e.hub.down.Store(false)
	e.hub.busy.Store(900)
	en.NextStartAt = time.Now().Add(-time.Second)
	require.NoError(t, e.cs.SaveEnrollment(ctx, en))
	require.ErrorAs(t, m2.Connect(ctx), &rl)
	en, _ = e.cs.GetEnrollment(ctx, e.hub.srv.URL)
	assert.True(t, time.Until(en.NextStartAt) >= 899*time.Second, "Retry-After honoured: %s", time.Until(en.NextStartAt))
}

func TestEnrollBackoffIsCapped(t *testing.T) {
	prev := enrollJitter
	enrollJitter = func(time.Duration) time.Duration { return 0 }
	t.Cleanup(func() { enrollJitter = prev })
	for n, want := range map[int]time.Duration{1: 30 * time.Second, 2: time.Minute, 3: 2 * time.Minute, 5: 8 * time.Minute, 6: 15 * time.Minute, 50: 15 * time.Minute} {
		assert.Equal(t, want, enrollBackoff(n), "failure %d", n)
	}
}

// #3546: a server enrolled with the hub's old host keeps HubMoved through a failed attempt to
// enroll with the new one (that attempt's own error and code show), and the old hub is never
// contacted; an enrollment that succeeds clears it.
func TestHubMovedSurvivesAFailedConnect(t *testing.T) {
	e := newEnv(t)
	old := newFakeHub(t)
	require.NoError(t, e.cs.Save(context.Background(), old.srv.URL, "srv_1", "x", fakeToken))
	e.hub.busy.Store(90) // the new hub refuses to start an enrollment
	m := e.manager(e.hub.srv.URL)
	start(t, m)
	require.True(t, m.Status().HubMoved)
	require.Equal(t, ErrCodeHubMoved, m.Status().LastErrorCode)

	var rl *RateLimitedError
	require.ErrorAs(t, m.Connect(context.Background()), &rl)
	st := m.Status()
	require.True(t, st.HubMoved, "still moved after a failed attempt")
	require.Equal(t, StateIdle, st.State)
	require.Equal(t, ErrCodeRateLimited, st.LastErrorCode, "the failure shows its own code")
	require.Equal(t, "Coral Hub is busy: try again in 1m30s", st.LastError)
	require.Zero(t, old.hits.Load(), "the old hub is never contacted")

	// Enrolling with the new hub (here a fresh connector, so no local wait after the failure) clears it.
	e2 := newEnv(t)
	old2 := newFakeHub(t)
	require.NoError(t, e2.cs.Save(context.Background(), old2.srv.URL, "srv_1", "x", fakeToken))
	m2 := e2.manager(e2.hub.srv.URL)
	start(t, m2)
	require.True(t, m2.Status().HubMoved)
	require.NoError(t, m2.Connect(context.Background()))
	require.True(t, m2.Status().HubMoved, "enrolling, not yet enrolled")
	waitState(t, m2, StateConnected)
	require.False(t, m2.Status().HubMoved, "enrolled with the new hub")
	require.Zero(t, old2.hits.Load())
	require.NotContains(t, mustJSON(m2.Status()), "hub_moved")
}
