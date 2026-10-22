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
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/redact"
	"github.com/subgentic/coral/pkg/tunnel"
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
	RedactTokens func() bool        // hide tokens from Coral Hub viewers (#3560); nil means on
	// Redactor removes secrets from what Coral Hub viewers see (#3808: the server passes
	// internal/secrets'); nil refuses every reply that would be redacted. An option, so the
	// commands that use this package without serving (the tray, launch-coral) don't link the
	// detector.
	Redactor redact.Redactor
	// HubLimits gives the Coral Hub request pools' sizes for each request (#3642); nil means
	// the defaults.
	HubLimits    func() tunnel.HubLimits
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
	// SigninOrigins gives the exact return origins Hub sign-in may use (#3966): sent to the hub
	// on every tunnel that offers sign-in and again on SigninOriginsChanged. Nil: none are sent
	// and no sign-in starts.
	SigninOrigins func() []string
	// OnSigninEvent tells the local side about the owner pin (pinned, or another account
	// named: end every Hub session).
	OnSigninEvent func(ctx context.Context, ev SigninEvent)
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
	// RetryIn is how many seconds until Connect may ask Coral Hub again (a hub that could not be reached, or that is busy), 0 when it may now (#5962). The dashboard counts it down.
	RetryIn int `json:"retry_in_s,omitempty"`
	// Loading is true from startup until the connector has read the stored
	// enrollment (and a startup credential, if any): State is not final yet.
	Loading bool `json:"loading,omitempty"`
	// ConnectedAt is when the tunnel came up, while State is connected (the dashboard's hub
	// indicator says "connected since", #3544); zero in every other state.
	ConnectedAt time.Time `json:"connected_at,omitzero"`
	// HubMoved: the stored enrollment is for another hub than the configured one (Coral Hub
	// moved, #3546). It stays set through any failed attempt to enroll again (their errors show
	// in LastError and LastErrorCode) until an enrollment succeeds or the operator disconnects.
	HubMoved bool `json:"hub_moved,omitempty"`
}

// applyTunnelState records a tunnel state change: ConnectedAt is set when the tunnel comes
// up, kept while it stays up, and cleared in any other state.
func applyTunnelState(st *Status, state, msg string, now time.Time) {
	if state == StateConnected && st.State != StateConnected {
		st.ConnectedAt = now.UTC()
	} else if state != StateConnected {
		st.ConnectedAt = time.Time{}
	}
	st.State, st.LastError, st.LastErrorCode = state, msg, ""
}

// Last error codes (Status.LastErrorCode).
const (
	ErrCodeExpired      = "code_expired"
	ErrCodeAccessDenied = "access_denied"
	ErrCodeRateLimited  = "rate_limited"
	// ErrCodeHubUnreachable: the first call to Coral Hub, to start an enrollment, could not reach it (#5962): the dashboard says it in a sentence and counts down to the next try.
	ErrCodeHubUnreachable = "hub_unreachable"
	// ErrCodeHubMoved: this server is enrolled with another hub than the configured one (Coral
	// Hub moved to app.coralhub.dev, #3546). Nothing dials either hub until the operator
	// enrolls again; the dashboard shows it as disconnected, and the startup step leaves it.
	ErrCodeHubMoved = "hub_moved"
)

// codeExpiredMessage is coral-hub's required text for a code that expired. A user outside
// Coral Hub's closed beta can't sign in, so their code just expires: the hub gives no
// distinct answer for that, by design (it would leak who is on the allowlist).
const codeExpiredMessage = "The code expired before it was approved. If you couldn't sign in at app.coralhub.dev, Coral Hub is in closed beta: request access at https://coralhub.dev/hub.html"

// Manager owns the enrollment and the tunnel.
type Manager struct {
	o  Options
	hc *http.Client

	mu       sync.Mutex
	redeemMu sync.Mutex // one redeem at a time
	root     context.Context
	cancel   context.CancelFunc // current enrollment or tunnel goroutine
	st       Status
	hubMoved bool      // Status.HubMoved: enrolled with another hub than the configured one (#3546)
	retryAt  time.Time // when Connect may ask Coral Hub again after a failed start (#5962); only read with the idle failure codes
	gen      int       // bumped on every start/stop so stale goroutines cannot write state
	// loaded is closed once Run has read the stored enrollment (at once with no
	// hub): until then an enrolled server still looks idle.
	loaded     chan struct{}
	loadedOnce sync.Once
	// session is the live tunnel session, nil while disconnected (#3489: relay requests).
	session atomic.Pointer[tunnel.ClientSession]
	// relaySlots keeps this Coral's relay requests at or below the hub's per-tunnel
	// in-flight cap (4), so a legitimate request is never closed for being over its
	// stream cap (which the connector would read as "no relay").
	relaySlots chan struct{}
	// artifactSlots keeps this Coral to one file transfer at a time through the tunnel: the hub
	// budgets them per tunnel, and a file takes the bulk lane for as long as it is sent.
	artifactSlots chan struct{}
	// signinSlots is Hub sign-in's own in-flight budget (2, the hub's per-tunnel cap for it), so
	// a relay burst never makes a sign-in check wait or come back busy (#3966).
	signinSlots chan struct{}
	// originsAccepted is the return-origin list the hub accepted on tunnel originsSession
	// (#3966); a sign-in starts only on that tunnel and only for an origin on it.
	originsAccepted []string
	originsSession  *tunnel.ClientSession
	// local is the current tunnel's handler, so that lowering this computer's mode can end the streams it allowed above the new mode (#5918).
	local atomic.Pointer[tunnel.LocalHandler]
	// The mode the hub is told about (#5918): lastMode is the mode as of the last dial or change (a lowering is a change below it), announced is what the hub last learned (by Hello or by
	// a local_mode message), modeAt when the last message went, wantMode the latest mode asked for, worker whether the one goroutine that sends them is running (so at most one message is
	// in flight, and a change made meanwhile is the latest wanted, never a second send); modeGap is the least time between two messages (a test's own).
	modeMu     sync.Mutex
	lastMode   tunnel.Mode
	announced  tunnel.Mode
	modeAt     time.Time
	wantMode   tunnel.Mode
	worker     bool
	modeGap    time.Duration
	closeAbove func(tunnel.Mode) int // ends the streams allowed above a lowered mode (the current tunnel's handler); a test sets its own to see when it runs
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
	m := &Manager{o: o, hc: hc, loaded: make(chan struct{}), relaySlots: make(chan struct{}, 4), artifactSlots: make(chan struct{}, 1), signinSlots: make(chan struct{}, 2), modeGap: tunnel.LocalModeMinGap + 100*time.Millisecond}
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
		// Coral Hub moved to app.coralhub.dev (#3546): a server enrolled with the old host stops
		// here, says which host, and is re-enrolled; a token is never sent to a host it wasn't
		// enrolled with.
		m.mu.Lock()
		m.hubMoved = true
		m.st.State, m.st.LastErrorCode = StateIdle, ErrCodeHubMoved
		m.st.LastError = "this server is enrolled with a different hub (" + conn.HubURL + "); run 'coral cloud connect --force' to enroll with " + m.o.HubURL
		m.mu.Unlock()
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
	s.HubMoved = m.hubMoved
	s.MaxMode = m.currentMode().String()
	s.Loading = !m.isLoaded()
	if s.State == StateIdle && (s.LastErrorCode == ErrCodeRateLimited || s.LastErrorCode == ErrCodeHubUnreachable) {
		if d := m.retryAt.Sub(m.o.Now()); d > 0 {
			s.RetryIn = int(math.Ceil(d.Seconds()))
		}
	}
	return s
}

func (m *Manager) stopLocked() {
	m.gen++
	m.session.Store(nil) // the stopped tunnel's session is gone; its own clear is now stale
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

	// The token must be storable here before the hub enrolls this server (#3693): enrolling
	// first and failing to store the token left an enrollment in Coral Hub with no connection.
	if err := m.o.Store.CheckSecrets(ctx); err != nil {
		m.setError(StateIdle, "Coral can't store the Coral Hub token securely on this computer, so nothing was enrolled: "+err.Error())
		return fmt.Errorf("the token can't be stored here: %w", err)
	}

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
		m.retryAt = pend.NextStartAt
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
			m.retryAt = now.Add(wait)
			m.mu.Unlock()
		} else {
			m.mu.Lock()
			m.st.State, m.st.LastError, m.st.LastErrorCode = StateIdle, "could not reach the hub to start enrollment", ErrCodeHubUnreachable
			m.retryAt = now.Add(wait)
			m.mu.Unlock()
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
	go func() { defer safego.Recover("cloud/manager:m.pollEnrollment"); m.pollEnrollment(ectx, gen, dc) }()
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
				// No token in the message or the log: serr is about the key, never the token.
				m.finishEnroll(gen, "Coral Hub enrolled this computer, but the token could not be stored here ("+serr.Error()+
					"), so it can't connect. Remove this computer in Coral Hub, then connect again.")
				return
			}
			if en.Account != "" {
				// The owner Hub sign-in compares every reply with (#3966).
				if pinned, oerr := m.o.Store.PinOwner(ctx, en.ServerID, en.Account); oerr == nil && pinned {
					m.signinEvent(ctx, SigninEvent{Kind: SigninOwnerPinned, How: "enrollment"})
				}
			}
			if en.OwnerUser != "" {
				// Recorded once, here: the update routes' owner check compares every relaunch with it (#5280).
				_, _ = m.o.Store.PinOwnerUser(ctx, en.ServerID, en.OwnerUser)
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
	m.hubMoved = false // enrolled with the configured hub: it's where the tunnel goes
	m.st = Status{Configured: true, HubURL: m.o.HubURL, State: StateConnecting, Label: conn.Label, ServerID: conn.ServerID}
	m.mu.Unlock()
	go func() { defer safego.Recover("cloud/manager:m.runTunnel"); m.runTunnel(tctx, gen) }()
}

func (m *Manager) runTunnel(ctx context.Context, gen int) {
	set := func(state, msg string) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if !m.stale(gen) {
			applyTunnelState(&m.st, state, msg, time.Now())
		}
	}
	token, err := m.o.Store.Token(ctx)
	if err != nil {
		set(StateKeyUnreadable, "the stored token cannot be decrypted (master key missing or changed); disconnect and enroll again")
		return
	}
	m.modeMu.Lock()
	m.lastMode, m.announced = m.currentMode(), m.currentMode() // this dial's Hello carries it
	m.modeMu.Unlock()
	local := &tunnel.LocalHandler{
		BaseURL:       m.o.LocalBaseURL,
		AllowRequest:  tunnel.AllowRequest(m.o.MaxMode), // read on every request: a change applies at once
		MaxMode:       m.currentMode,                    // gates WebSocket input frame by frame
		ViaValue:      ViaSecret(),                      // lets the server tell the connector from a forged stamp
		RedactTokens:  m.o.RedactTokens,                 // read on every reply: a change applies at once
		Redactor:      m.o.Redactor,                     // betterleaks' rules (#3808)
		HubLimitsFunc: m.o.HubLimits,                    // read on every request: a change applies at once
	}
	m.local.Store(local)
	err = tunnel.Run(ctx, TunnelURL(m.o.HubURL), token, tunnel.ClientOptions{
		Handler:       local,
		CoralVersion:  m.o.CoralVersion,
		LocalMode:     m.currentMode,
		AllowInsecure: config.AllowInsecureCloud,
		HTTPClient:    m.hc,
		Backoff:       m.o.Backoff,
		Logger:        m.o.Logger,
		OnSession:     func(cs *tunnel.ClientSession) { m.setSession(gen, cs) },
		// The hub's routing hint (#3683): kept for this hub URL only (a new enrollment or another
		// hub drops it), sent back on the next dial to it, never logged.
		RouteKey: func() string {
			k, _ := m.o.Store.RouteKey(ctx, m.o.HubURL)
			return k
		},
		OnRouteKey: func(k string) {
			if old, _ := m.o.Store.RouteKey(ctx, m.o.HubURL); old != k {
				_ = m.o.Store.SetRouteKey(ctx, m.o.HubURL, k)
			}
		},
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
	m.hubMoved = false // the old enrollment is forgotten
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

// ModeChanged tells the hub that this computer's maximum mode changed, so it learns it ("limited by this machine"). Enforcement does not wait for it: the connector reads the mode on
// every request and every WebSocket frame.
//
// To a hub that advertised local_mode/1 (#5918) it is a message, not a reconnect: first, on a lowering, this Coral ends its own open streams that were allowed more than the new mode, then it
// sends {"local_mode": ...} (at most one a second: a faster change is held and only the latest sent, an identical one not sent at all). Any answer but 204, a timeout, or no capability falls back to what
// it always did, a reconnect, so the next Hello carries the mode: the hub turns the capability on with its own go, and until then nothing changes.
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
	if st != StateConnected && st != StateConnecting && st != StateRetrying {
		return
	}
	cs := m.session.Load()
	if cs == nil || !cs.CanSendLocalMode() {
		m.startTunnel(conn)
		return
	}
	mode := m.currentMode()
	m.modeMu.Lock()
	lowering := mode < m.lastMode
	m.lastMode = mode
	m.modeMu.Unlock()
	if lowering {
		closeAbove := m.closeAbove
		if closeAbove == nil {
			if lh := m.local.Load(); lh != nil {
				closeAbove = lh.CloseAbove
			}
		}
		if closeAbove != nil {
			if n := closeAbove(mode); n > 0 {
				m.o.Logger.Info("[cloud] ended streams allowed above the new mode", "streams", n, "mode", mode.String())
			}
		}
	}
	m.modeMu.Lock()
	m.wantMode = mode
	start := !m.worker
	m.worker = true
	m.modeMu.Unlock()
	if start {
		go func() { defer safego.Recover("cloud/manager:m.modeWorker"); m.modeWorker(conn) }()
	}
}

// modeWorker sends the latest wanted mode to the hub, one message at a time and no faster than modeGap: a change made while one is in flight, or inside the gap, is not a second send but the
// new wanted value, and it goes out when the worker gets to it (so the hub never sees two at once or out of order, and an identical value is never sent). Anything but the hub's 204, and a tunnel
// that is gone or no longer advertises the capability, ends in the reconnect (the next Hello carries the mode); the worker then stops.
func (m *Manager) modeWorker(conn *store.CloudConnection) {
	for {
		m.modeMu.Lock()
		mode := m.wantMode
		if mode == m.announced {
			m.worker = false
			m.modeMu.Unlock()
			return // the hub already has it: coalesced
		}
		wait := m.modeGap - m.o.Now().Sub(m.modeAt)
		if m.modeAt.IsZero() || wait < 0 {
			wait = 0
		}
		m.modeMu.Unlock()
		if wait > 0 {
			select {
			case <-time.After(wait):
			case <-m.root.Done():
				m.modeMu.Lock()
				m.worker = false
				m.modeMu.Unlock()
				return
			}
			continue // the wanted mode may have changed meanwhile: look again
		}
		cs := m.session.Load()
		if cs == nil || !cs.CanSendLocalMode() {
			m.modeWorkerEnds()
			m.reconnectForMode(conn) // the tunnel was replaced meanwhile: its Hello carries the mode
			return
		}
		ctx, cancel := context.WithTimeout(m.root, 5*time.Second)
		err := cs.SendLocalMode(ctx, mode)
		cancel()
		if err != nil {
			m.o.Logger.Info("[cloud] the hub did not take the local mode message: reconnecting so the next hello carries it", "error", err.Error())
			m.modeWorkerEnds()
			m.reconnectForMode(conn)
			return
		}
		m.modeMu.Lock()
		m.announced, m.modeAt = mode, m.o.Now()
		m.modeMu.Unlock()
	}
}

// reconnectForMode restarts the tunnel on the connection record as it is NOW, not the one of the call that started the worker: the record may have changed since (a disconnect, another
// enrollment), and then there is nothing to reconnect (#5918, Security's low).
func (m *Manager) reconnectForMode(_ *store.CloudConnection) {
	conn, err := m.o.Store.Get(m.root)
	if err != nil || conn.State != store.CloudStateActive || conn.HubURL != m.o.HubURL {
		return
	}
	m.startTunnel(conn)
}

func (m *Manager) modeWorkerEnds() {
	m.modeMu.Lock()
	m.worker = false
	m.modeMu.Unlock()
}

// setSession records generation gen's live tunnel session (nil when it ends). Only the
// current generation sets or clears it, so an old tunnel ending after a reconnect can't
// clear the new one; the check and the store are one critical section with stopLocked,
// so a stop can't land between them and leave a closed session behind.
func (m *Manager) setSession(gen int, cs *tunnel.ClientSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.stale(gen) {
		m.session.Store(cs)
		m.originsAccepted, m.originsSession = nil, nil
		if cs != nil && cs.CanSignin() {
			go func() { defer safego.Recover("cloud/manager:m.pushOrigins"); m.pushOrigins(gen, cs) }() // takes m.mu itself
		}
	}
}

// ErrNotLinked: this Coral has no live Coral Hub tunnel right now.
var ErrNotLinked = errors.New("this Coral isn't connected to Coral Hub")

// Relay sends one relay request (tunnel.RelayMessagePath or tunnel.RelayTargetsPath) to
// Coral Hub over the live tunnel (#3489): ErrNotLinked without one, and
// tunnel.ErrRelayUnsupported when the hub doesn't relay for this server.
func (m *Manager) Relay(ctx context.Context, path string, body []byte) (tunnel.RelayResponse, error) {
	cs := m.session.Load()
	if cs == nil {
		return tunnel.RelayResponse{}, ErrNotLinked
	}
	select {
	case m.relaySlots <- struct{}{}:
		defer func() { <-m.relaySlots }()
	case <-ctx.Done():
		return tunnel.RelayResponse{}, ctx.Err()
	}
	return cs.Relay(ctx, path, body)
}

// RelayArtifact sends one file frame (n bytes read from body: the 4-byte metadata length, the metadata,
// then the file's bytes) to Coral Hub over the live tunnel (cross-team artifacts): ErrNotLinked without
// one, tunnel.ErrRelayArtifactUnsupported when the hub doesn't relay files for this server. One transfer
// at a time; a second waits its turn or gives up with ctx.
func (m *Manager) RelayArtifact(ctx context.Context, n int64, body io.Reader) (tunnel.RelayResponse, error) {
	cs := m.session.Load()
	if cs == nil {
		return tunnel.RelayResponse{}, ErrNotLinked
	}
	select {
	case m.artifactSlots <- struct{}{}:
		defer func() { <-m.artifactSlots }()
	case <-ctx.Done():
		return tunnel.RelayResponse{}, ctx.Err()
	}
	return cs.RelayArtifact(ctx, n, body)
}
