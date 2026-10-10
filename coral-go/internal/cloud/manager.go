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
	"net/http"
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
var ErrNotConfigured = errors.New("cloud hub is not configured: start Coral with --cloud-url or CORAL_CLOUD_URL")

// Options configures a Manager.
type Options struct {
	HubURL       string // validated origin from NormalizeHubURL; "" disables everything
	Store        *store.CloudStore
	LocalBaseURL string // loopback URL of this server, e.g. http://127.0.0.1:8420
	AllowLaunch  func() bool
	CoralVersion string
	HTTPClient   *http.Client // enrollment and tunnel handshake; nil uses NewHubHTTPClient()
	Backoff      tunnel.Backoff
	Logger       *slog.Logger
	Now          func() time.Time
	// PollInterval overrides the hub's suggested enrollment poll interval (tests).
	PollInterval time.Duration
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
	AllowLaunch     bool      `json:"allow_launch"`
}

// Manager owns the enrollment and the tunnel.
type Manager struct {
	o  Options
	hc *http.Client

	mu     sync.Mutex
	root   context.Context
	cancel context.CancelFunc // current enrollment or tunnel goroutine
	st     Status
	gen    int // bumped on every start/stop so stale goroutines cannot write state
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
	m := &Manager{o: o, hc: hc}
	m.st = Status{Configured: o.HubURL != "", HubURL: o.HubURL, State: StateOff}
	if o.HubURL != "" {
		m.st.State = StateIdle
	}
	return m
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
	<-ctx.Done()
	m.mu.Lock()
	m.stopLocked()
	m.mu.Unlock()
}

// Status returns the current state.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.st
	if m.o.AllowLaunch != nil {
		s.AllowLaunch = m.o.AllowLaunch()
	}
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
	m.st.State, m.st.LastError = state, msg
	m.mu.Unlock()
}

// Connect starts enrollment: it asks the hub for a code and polls until the user
// confirms it in the browser, the code expires, or Disconnect is called.
func (m *Manager) Connect(ctx context.Context) error {
	if m.o.HubURL == "" {
		return ErrNotConfigured
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

	dc, err := StartEnrollment(ctx, m.hc, m.o.HubURL, m.o.Now())
	if err != nil {
		m.setError(StateIdle, "could not reach the hub to start enrollment")
		return err
	}
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
	return nil
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
			m.finishEnroll(gen, "the code expired; start again")
			return
		}
		en, err := PollEnrollment(ctx, m.hc, m.o.HubURL, dc.DeviceCode)
		switch {
		case err == nil:
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
			m.finishEnroll(gen, "the code expired; start again")
			return
		default:
			if failures++; failures >= 10 {
				m.finishEnroll(gen, "the hub is not answering; enrollment stopped")
				return
			}
		}
	}
}

func (m *Manager) finishEnroll(gen int, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stale(gen) {
		return
	}
	m.st = Status{Configured: true, HubURL: m.o.HubURL, State: StateIdle, LastError: msg}
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
			m.st.State, m.st.LastError = state, msg
		}
	}
	token, err := m.o.Store.Token(ctx)
	if err != nil {
		set(StateKeyUnreadable, "the stored token cannot be decrypted (master key missing or changed); disconnect and enroll again")
		return
	}
	local := &tunnel.LocalHandler{
		BaseURL:   m.o.LocalBaseURL,
		AllowPath: Rules(m.o.AllowLaunch),
		Authorize: func(h http.Header) { h.Set(TunnelHeader, "1") },
	}
	err = tunnel.Run(ctx, TunnelURL(m.o.HubURL), token, tunnel.ClientOptions{
		Handler:       local,
		CoralVersion:  m.o.CoralVersion,
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
	return m.o.Store.Delete(ctx)
}
