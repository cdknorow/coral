// Package cloud is the Coral side of the Cloud Hub: enrollment (device code), the
// outbound tunnel, and the fixed allowlist of local routes the hub may reach.
//
// With no hub URL configured the Manager never touches the network. A hub that is
// unreachable, slow or revoked never affects local use: everything here runs in
// background goroutines and retries quietly.
package cloud

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// States shown in the settings page.
const (
	StateOff           = "off"            // no hub URL configured: no outbound connections
	StateIdle          = "idle"           // configured, not enrolled
	StateEnrolling     = "enrolling"      // waiting for the user to confirm the code in the browser
	StateConnecting    = "connecting"     // enrolled, dialing
	StateConnected     = "connected"      // tunnel up
	StateRetrying      = "retrying"       // enrolled, hub unreachable or busy; retrying quietly
	StateRevoked       = "revoked"        // the hub rejected the token; retries stopped
	StateKeyUnreadable = "key_unreadable" // the master key for the stored token is missing or wrong
	StateUpgrade       = "upgrade_required"
)

// ErrNotConfigured is returned by Connect when no hub URL is configured.
var ErrNotConfigured = errors.New("Coral Hub is not configured: start Coral with --cloud-url or CORAL_CLOUD_URL")

// Options configures a Manager.
type Options struct {
	HubURL       string // validated origin from NormalizeHubURL; "" disables everything
	Store        *store.CloudStore
	LocalBaseURL string             // loopback URL of this server, e.g. http://127.0.0.1:8420
	MaxMode      func() tunnel.Mode // this server's own maximum access mode; default view
	CoralVersion string
	HTTPClient   *http.Client // enrollment and tunnel handshake; nil uses NewHubHTTPClient()
	Backoff      tunnel.Backoff
	Logger       *slog.Logger
	Now          func() time.Time
	// PollInterval overrides the hub's suggested enrollment poll interval (tests).
	PollInterval time.Duration
	// OnLinkLost runs when this server stops being linked to the hub: the user
	// disconnects it, or the hub revokes it. Whatever the hub turned on is turned
	// off (auto-accept, #3182).
	OnLinkLost func(ctx context.Context)
	// RecordTerms records the terms version the Hub confirmed when a credential
	// is redeemed (nil: the local acceptance marker; tests replace it).
	RecordTerms func(version string) error
}

// Status is the safe, secret-free view for the UI.
type Status struct {
	Configured      bool      `json:"configured"`
	HubURL          string    `json:"hub_url,omitempty"`
	State           string    `json:"state"`
	UserCode        string    `json:"user_code,omitempty"`
	VerificationURL string    `json:"verification_url,omitempty"`
	ExpiresAt       time.Time `json:"expires_at,omitzero"`
	Label           string    `json:"label,omitempty"`
	ServerID        string    `json:"server_id,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	// LastErrorCode names the reason, for a UI that must not match on LastError's wording:
	// code_expired, access_denied or rate_limited (#3285, coral-hub's contract).
	LastErrorCode string `json:"last_error_code,omitempty"`
	MaxMode       string `json:"max_mode"` // view | interact | launch: set locally, never by the hub
	// Loading is true from startup until the connector has read the stored
	// enrollment (and a startup credential, if any): State is not final yet.
	Loading bool `json:"loading,omitempty"`
}

// Last error codes (Status.LastErrorCode).
const (
	ErrCodeExpired      = "code_expired"
	ErrCodeAccessDenied = "access_denied"
	ErrCodeRateLimited  = "rate_limited"
)

// codeExpiredMessage is coral-hub's required text for a code that expired. A user outside
// Coral Hub's closed beta can't sign in, so their code just expires: the hub gives no
// distinct answer for that, by design (it would leak who is on the allowlist).
const codeExpiredMessage = "The code expired before it was approved. If you couldn't sign in at app.coralai.ai, Coral Hub is in closed beta: request access at https://coralai.ai/hub.html"

// Manager owns the enrollment and the tunnel.
type Manager struct {
	o  Options
	hc *http.Client

	mu       sync.Mutex
	redeemMu sync.Mutex // one redeem at a time
	root     context.Context
	cancel   context.CancelFunc // current enrollment or tunnel goroutine
	st       Status
	gen      int // bumped on every start/stop so stale goroutines cannot write state
	// loaded is closed once Run has read the stored enrollment (at once with no
	// hub): until then an enrolled server still looks idle.
	loaded     chan struct{}
	loadedOnce sync.Once
}

// New creates a Manager. Nothing runs until Run.
func New(o Options) *Manager {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	hc := o.HTTPClient
	if hc == nil {
		hc = NewHubHTTPClient()
	}
	m := &Manager{o: o, hc: hc, loaded: make(chan struct{})}
	m.st = Status{Configured: o.HubURL != "", HubURL: o.HubURL, State: StateOff}
	if o.HubURL != "" {
		m.st.State = StateIdle
	} else {
		m.markLoaded()
	}
	return m
}

func (m *Manager) markLoaded() { m.loadedOnce.Do(func() { close(m.loaded) }) }

func (m *Manager) isLoaded() bool {
	select {
	case <-m.loaded:
		return true
	default:
		return false
	}
}

// Run starts the connector if this server is already enrolled, then waits for ctx.
// With no hub URL it returns immediately: no goroutines, no network.
func (m *Manager) Run(ctx context.Context) {
	if m.o.HubURL == "" {
		return
	}
	m.mu.Lock()
	m.root = ctx
	m.mu.Unlock()

	conn, err := m.o.Store.Get(ctx)
	switch {
	case errors.Is(err, store.ErrNoCloudConnection):
	case err != nil:
		m.setError(StateIdle, "could not read the stored enrollment")
	case conn.HubURL != m.o.HubURL:
		m.setError(StateIdle, "this server is enrolled with a different hub; disconnect before enrolling with this one")
	case conn.State == store.CloudStateRevoked:
		m.mu.Lock()
		m.st.State, m.st.Label, m.st.ServerID = StateRevoked, conn.Label, conn.ServerID
		m.st.LastError = "the hub revoked this server; disconnect and enroll again to reconnect"
		m.mu.Unlock()
	default:
		m.startTunnel(conn)
	}
	m.markLoaded()
	<-ctx.Done()
	m.mu.Lock()
	m.stopLocked()
	m.mu.Unlock()
}

// Bootstrap redeems a one-time credential at startup, before Run (no network
// call before its file is validated).
func (m *Manager) Bootstrap(ctx context.Context, settings *store.SessionStore, hostname string, src CredentialSource) BootstrapResult {
	m.redeemMu.Lock()
	defer m.redeemMu.Unlock()
	return Bootstrap(ctx, BootstrapOptions{HubURL: m.o.HubURL, Store: m.o.Store, Settings: settings, HTTPClient: m.hc, Hostname: hostname, Logger: m.o.Logger, RecordTerms: m.o.RecordTerms}, src)
}

// PreCheck reports whether a bootstrap could proceed (see PreCheck).
func (m *Manager) PreCheck(ctx context.Context) BootstrapResult {
	return PreCheck(ctx, m.o.HubURL, m.o.Store)
}

// Redeem enrolls the running server with a credential handed over by
// `coral cloud connect --ticket-file` (the server stays the only writer of the
// identity), then connects.
func (m *Manager) Redeem(ctx context.Context, settings *store.SessionStore, hostname string, src CredentialSource) BootstrapResult {
	res := m.Bootstrap(ctx, settings, hostname, src)
	if res.Status != BootstrapEnrolled {
		return res
	}
	m.mu.Lock()
	running := m.root != nil
	m.mu.Unlock()
	if conn, err := m.o.Store.Get(ctx); err == nil && running {
		m.startTunnel(conn)
	}
	return res
}

// Status returns the current state.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.st
	s.MaxMode = m.currentMode().String()
	s.Loading = !m.isLoaded()
	return s
}

func (m *Manager) stopLocked() {
	m.gen++
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}

func (m *Manager) setError(state, msg string) {
	m.mu.Lock()
	m.st.State, m.st.LastError, m.st.LastErrorCode = state, msg, ""
	m.mu.Unlock()
}

// Connect starts enrollment: it asks the hub for a code and polls until the user
// confirms it in the browser, the code expires, or Disconnect is called.
func (m *Manager) Connect(ctx context.Context) error {
	if m.o.HubURL == "" {
		return ErrNotConfigured
	}
	// An enrolled server looks idle until Run has read its enrollment: never
	// start a new enrollment before then.
	select {
	case <-m.loaded:
	case <-ctx.Done():
		return ctx.Err()
	}
	m.mu.Lock()
	if m.root == nil {
		m.mu.Unlock()
		return errors.New("cloud connector is not running")
	}
	switch m.st.State {
	case StateEnrolling, StateConnecting, StateConnected, StateRetrying, StateUpgrade:
		m.mu.Unlock()
		return nil // already enrolled or enrolling; Disconnect first to start over
	}
	m.mu.Unlock()

	// A code from an earlier start (this run or a previous one) is reused until it
	// expires; a new one is asked for at most once per enrollMinGap, and after a failed
	// start only after a growing, jittered wait. So a machine in a restart loop asks
	// the hub for one code, not one per start.
	now := m.o.Now()
	pend, _ := m.o.Store.GetEnrollment(ctx, m.o.HubURL)
	if pend != nil && pend.DeviceCode != "" && now.Add(enrollReuseMargin).Before(pend.ExpiresAt) {
		m.startPolling(&DeviceCode{DeviceCode: pend.DeviceCode, UserCode: pend.UserCode, VerificationURL: pend.VerificationURL,
			Interval: pend.Interval, ExpiresAt: pend.ExpiresAt})
		return nil
	}
	if pend != nil && now.Before(pend.NextStartAt) {
		rl := &RateLimitedError{RetryAfter: pend.NextStartAt.Sub(now), Local: true}
		m.mu.Lock()
		m.st.State, m.st.LastError, m.st.LastErrorCode = StateIdle, rl.Error(), ErrCodeRateLimited
		m.mu.Unlock()
		return rl
	}

	host, _ := os.Hostname()
	dc, err := StartEnrollment(ctx, m.hc, m.o.HubURL, now, EnrollInfo{Hostname: host, CoralVersion: m.o.CoralVersion})
	if err != nil {
		failures := 1
		if pend != nil {
			failures = pend.Failures + 1
		}
		wait := enrollBackoff(failures)
		var rl *RateLimitedError
		if errors.As(err, &rl) && rl.RetryAfter > wait {
			wait = rl.RetryAfter // the hub's Retry-After is the least we wait
		}
		_ = m.o.Store.SaveEnrollment(ctx, &store.CloudEnrollment{HubURL: m.o.HubURL, NextStartAt: now.Add(wait), Failures: failures})
		if errors.As(err, &rl) {
			m.mu.Lock()
			m.st.State, m.st.LastError, m.st.LastErrorCode = StateIdle, rl.Error(), ErrCodeRateLimited
			m.mu.Unlock()
		} else {
			m.setError(StateIdle, "could not reach the hub to start enrollment")
		}
		return err
	}
	next := now.Add(enrollMinGap)
	if dc.ExpiresAt.After(next) {
		next = dc.ExpiresAt
	}
	_ = m.o.Store.SaveEnrollment(ctx, &store.CloudEnrollment{HubURL: m.o.HubURL, DeviceCode: dc.DeviceCode, UserCode: dc.UserCode,
		VerificationURL: dc.VerificationURL, Interval: dc.Interval, ExpiresAt: dc.ExpiresAt, NextStartAt: next.Add(enrollJitter(enrollMinGap / 10))})
	m.startPolling(dc)
	return nil
}

// Enrollment start limits (#3285 follow-up).
const (
	enrollReuseMargin = 30 * time.Second // a code this close to expiry is not reused
	enrollMinGap      = 10 * time.Minute // at most one new code per this, per machine
	enrollBackoffBase = 30 * time.Second
	enrollBackoffMax  = 15 * time.Minute
)

// enrollJitter is a random extra wait in [0, max): machines that fail together don't
// retry together.
var enrollJitter = func(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(max)))
}

// enrollBackoff is the wait after the n-th consecutive failed start: 30 s doubling,
// capped at 15 min, plus up to 20% jitter.
func enrollBackoff(n int) time.Duration {
	d := enrollBackoffBase
	for i := 1; i < n && d < enrollBackoffMax; i++ {
		d *= 2
	}
	if d > enrollBackoffMax {
		d = enrollBackoffMax
	}
	return d + enrollJitter(d/5)
}

// dropPendingCode forgets a code that expired or was denied, but keeps the start limit,
// so the next code still waits for it.
func (m *Manager) dropPendingCode(ctx context.Context) {
	if e, err := m.o.Store.GetEnrollment(ctx, m.o.HubURL); err == nil && e != nil {
		e.DeviceCode, e.UserCode, e.VerificationURL, e.ExpiresAt = "", "", "", time.Time{}
		_ = m.o.Store.SaveEnrollment(ctx, e)
	}
}

// startPolling shows the code and polls the hub for the user's approval.
func (m *Manager) startPolling(dc *DeviceCode) {
	m.mu.Lock()
	m.stopLocked()
	m.gen++
	gen := m.gen
	ectx, cancel := context.WithCancel(m.root)
	m.cancel = cancel
	m.st = Status{Configured: true, HubURL: m.o.HubURL, State: StateEnrolling,
		UserCode: dc.UserCode, VerificationURL: dc.VerificationURL, ExpiresAt: dc.ExpiresAt}
	m.mu.Unlock()
	go m.pollEnrollment(ectx, gen, dc)
}

// CancelEnrollment stops an enrollment in progress: the user code is dropped and
// nothing is linked (`coral cloud connect`, Ctrl-C). In any other state it does
// nothing, so it can never undo a connection. If the Hub's approval was saved
// just before the cancel, that enrollment stands and connects.
func (m *Manager) CancelEnrollment(ctx context.Context) {
	m.mu.Lock()
	if m.st.State != StateEnrolling {
		m.mu.Unlock()
		return
	}
	m.stopLocked()
	m.st = Status{Configured: true, HubURL: m.o.HubURL, State: StateIdle, LastError: "enrollment cancelled"}
	running := m.root != nil
	m.mu.Unlock()
	if conn, err := m.o.Store.Get(ctx); err == nil && running && conn.HubURL == m.o.HubURL {
		m.startTunnel(conn)
	}
}

func (m *Manager) stale(gen int) bool { return gen != m.gen }

func (m *Manager) pollEnrollment(ctx context.Context, gen int, dc *DeviceCode) {
	interval, failures := dc.Interval, 0
	if m.o.PollInterval > 0 {
		interval = m.o.PollInterval
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
		if !m.o.Now().Before(dc.ExpiresAt) {
			m.dropPendingCode(ctx)
			m.finishEnrollCode(gen, codeExpiredMessage, ErrCodeExpired)
			return
		}
		en, err := PollEnrollment(ctx, m.hc, m.o.HubURL, dc.DeviceCode)
		switch {
		case err == nil:
			_ = m.o.Store.ClearEnrollment(ctx) // enrolled: no pending code, no rate limit
			if serr := m.o.Store.Save(ctx, m.o.HubURL, en.ServerID, en.Label, en.Token); serr != nil {
				m.finishEnroll(gen, "enrolled, but the token could not be stored securely; check the Coral data directory")
				return
			}
			conn, gerr := m.o.Store.Get(ctx)
			if gerr != nil {
				m.finishEnroll(gen, "enrolled, but the stored enrollment could not be read")
				return
			}
			m.mu.Lock()
			if m.stale(gen) {
				m.mu.Unlock()
				return
			}
			m.mu.Unlock()
			m.startTunnel(conn)
			return
		case errors.Is(err, ErrAuthorizationPending):
			failures = 0
		case errors.Is(err, ErrSlowDown):
			interval += 5 * time.Second
		case errors.Is(err, ErrEnrollmentExpired):
			m.dropPendingCode(ctx)
			m.finishEnrollCode(gen, codeExpiredMessage, ErrCodeExpired)
			return
		case errors.Is(err, ErrAccessDenied):
			m.dropPendingCode(ctx)
			m.finishEnrollCode(gen, "the request was denied in Coral Hub", ErrCodeAccessDenied)
			return
		default:
			if failures++; failures >= 10 {
				m.finishEnroll(gen, "the hub is not answering; enrollment stopped")
				return
			}
		}
	}
}

func (m *Manager) finishEnroll(gen int, msg string) { m.finishEnrollCode(gen, msg, "") }

func (m *Manager) finishEnrollCode(gen int, msg, code string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stale(gen) {
		return
	}
	m.st = Status{Configured: true, HubURL: m.o.HubURL, State: StateIdle, LastError: msg, LastErrorCode: code}
}

// startTunnel runs the tunnel for an enrolled server until revoked or stopped.
func (m *Manager) startTunnel(conn *store.CloudConnection) {
	m.mu.Lock()
	m.stopLocked()
	m.gen++
	gen := m.gen
	tctx, cancel := context.WithCancel(m.root)
	m.cancel = cancel
	m.st = Status{Configured: true, HubURL: m.o.HubURL, State: StateConnecting, Label: conn.Label, ServerID: conn.ServerID}
	m.mu.Unlock()
	go m.runTunnel(tctx, gen)
}

func (m *Manager) runTunnel(ctx context.Context, gen int) {
	set := func(state, msg string) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if !m.stale(gen) {
			m.st.State, m.st.LastError, m.st.LastErrorCode = state, msg, ""
		}
	}
	token, err := m.o.Store.Token(ctx)
	if err != nil {
		set(StateKeyUnreadable, "the stored token cannot be decrypted (master key missing or changed); disconnect and enroll again")
		return
	}
	local := &tunnel.LocalHandler{
		BaseURL:      m.o.LocalBaseURL,
		AllowRequest: tunnel.AllowRequest(m.o.MaxMode), // read on every request: a change applies at once
		MaxMode:      m.currentMode,                    // gates WebSocket input frame by frame
		ViaValue:     ViaSecret(),                      // lets the server tell the connector from a forged stamp
	}
	err = tunnel.Run(ctx, TunnelURL(m.o.HubURL), token, tunnel.ClientOptions{
		Handler:       local,
		CoralVersion:  m.o.CoralVersion,
		LocalMode:     m.currentMode,
		AllowInsecure: config.AllowInsecureCloud,
		HTTPClient:    m.hc,
		Backoff:       m.o.Backoff,
		Logger:        m.o.Logger,
		OnState: func(connected bool, err error) {
			switch {
			case connected:
				set(StateConnected, "")
			case errors.Is(err, tunnel.ErrUnauthorized), errors.Is(err, tunnel.ErrUnsupportedVersion):
				// Run returns right after this; handled below.
			case errors.Is(err, tunnel.ErrRetryLater):
				set(StateRetrying, "the hub is busy; retrying")
			case ctx.Err() == nil:
				set(StateRetrying, "the hub is unreachable; retrying")
			}
		},
	})
	switch {
	case ctx.Err() != nil:
		return
	case errors.Is(err, tunnel.ErrUnauthorized):
		// Revoked (or token invalid): persist, stop retrying, say so.
		_ = m.o.Store.SetState(context.Background(), store.CloudStateRevoked, "revoked by the hub")
		set(StateRevoked, "the hub revoked this server; disconnect and enroll again to reconnect")
		m.linkLost(context.Background())
	case errors.Is(err, tunnel.ErrUnsupportedVersion):
		set(StateUpgrade, "this Coral version is too old for the hub; update Coral")
	}
}

// Disconnect forgets the enrollment locally: it stops the tunnel or enrollment,
// deletes the sealed token and returns to idle. It does not need the hub. (The
// server entry on the hub side is removed from the hub's web page.)
func (m *Manager) Disconnect(ctx context.Context) error {
	if m.o.HubURL == "" {
		return ErrNotConfigured
	}
	m.mu.Lock()
	m.stopLocked()
	m.st = Status{Configured: true, HubURL: m.o.HubURL, State: StateIdle}
	m.mu.Unlock()
	m.linkLost(ctx)
	_ = m.o.Store.ClearEnrollment(ctx)
	return m.o.Store.Delete(ctx)
}

func (m *Manager) linkLost(ctx context.Context) {
	if m.o.OnLinkLost != nil {
		m.o.OnLinkLost(ctx)
	}
}

func (m *Manager) currentMode() tunnel.Mode {
	if m.o.MaxMode == nil {
		return tunnel.TierView
	}
	if md := m.o.MaxMode(); md >= tunnel.TierView && md <= tunnel.TierLaunch {
		return md
	}
	return tunnel.TierView
}

// ModeChanged reconnects an established tunnel so the hub learns the new local maximum
// (shown to the user as "limited by this machine"). Enforcement does not wait for it:
// the connector reads the mode on every request and every WebSocket frame.
func (m *Manager) ModeChanged(ctx context.Context) {
	if m.o.HubURL == "" {
		return
	}
	conn, err := m.o.Store.Get(ctx)
	if err != nil || conn.State != store.CloudStateActive || conn.HubURL != m.o.HubURL {
		return
	}
	m.mu.Lock()
	st := m.st.State
	m.mu.Unlock()
	if st == StateConnected || st == StateConnecting || st == StateRetrying {
		m.startTunnel(conn)
	}
}
