package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/cdknorow/coral/internal/auth"
	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/peercred"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// MaxModeSetting is the user setting holding this server's maximum cloud access mode:
// "view" (default), "interact" or "launch". It is local: the connector reads it on every
// request, so neither the hub nor a tunneled request can change it (the /api/cloud
// routes are not in the tunnel route table).
const MaxModeSetting = "cloud_max_mode"

// AllowHubAutoAcceptSetting is the local owner switch "Cloud Hub can turn on
// auto-accept" (#3182). It is on unless set to "false": the Launch access cap is
// already an explicit local opt-in, and this is the kill switch. Local only, like
// the mode cap; turning it off ends every enable that came from the hub.
const AllowHubAutoAcceptSetting = "cloud_allow_hub_auto_accept"

// HubAutoAcceptAllowed reads the switch (default on).
func HubAutoAcceptAllowed(settings map[string]string) bool {
	return settings[AllowHubAutoAcceptSetting] != "false"
}

// MaxModeFromSettings reads the local maximum mode. Anything missing or unrecognised is view.
func MaxModeFromSettings(ss *store.SessionStore) func() tunnel.Mode {
	return func() tunnel.Mode {
		s, err := ss.GetSettings(context.Background())
		if err != nil {
			return tunnel.TierView
		}
		if m, ok := tunnel.ParseMode(s[MaxModeSetting]); ok {
			return m
		}
		return tunnel.TierView
	}
}

// CloudHandler serves the "Connect to Coral Hub" settings section.
type CloudHandler struct {
	ss       *store.SessionStore
	identity *IdentityObserver
	// bs holds the record of Operator actions taken through Coral Hub (#3404).
	bs *board.Store
	// mintLogin mints a dashboard sign-in token (#3387).
	mintLogin func() (string, error)

	// cliAuth checks `coral cloud connect`: it proves it knows the 0600 token
	// without sending it (cloud.CLIAuth);
	// empty if it could not be written, which turns the command-line path off.
	cliAuth  *cloud.CLIAuth
	cliToken string
	dataDir  string
	// sameUser tells whether a loopback request comes from Coral's own user
	// (peercred.SameUser; a test seam).
	sameUser func(*http.Request) (same, supported bool)
	// linked overrides whether a Hub link exists (a test seam; nil reads the manager).
	linked func() bool

	mu  sync.RWMutex
	mgr *cloud.Manager
}

// NewCloudHandler creates the handler and writes a fresh command-line token in
// dataDir. The manager is attached later (SetManager) because it needs the
// running server's address.
func NewCloudHandler(ss *store.SessionStore, dataDir string) *CloudHandler {
	h := &CloudHandler{ss: ss, sameUser: peercred.SameUser, dataDir: dataDir}
	tok, err := cloud.WriteCLIToken(dataDir)
	if err != nil {
		slog.Warn("cloud: could not write the command-line token; `coral cloud connect` will be refused", "error", err)
	}
	h.cliAuth = cloud.NewCLIAuth(tok)
	h.cliToken = tok
	return h
}

// Close removes the command-line token this server wrote, at a graceful
// shutdown: a stopped server leaves no token behind (a new one is written at
// every start).
func (h *CloudHandler) Close() {
	if err := cloud.RemoveCLIToken(h.dataDir, h.cliToken); err != nil {
		slog.Warn("cloud: could not remove the command-line token", "error", err)
	}
}

// SetPeerCheck replaces the same-user check (tests).
func (h *CloudHandler) SetPeerCheck(f func(*http.Request) (same, supported bool)) { h.sameUser = f }

// SetManager attaches the connector manager.
func (h *CloudHandler) SetManager(m *cloud.Manager) {
	h.mu.Lock()
	h.mgr = m
	h.mu.Unlock()
}

func (h *CloudHandler) manager() *cloud.Manager {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.mgr
}

// cloudStatus is the connector status plus the local auto-accept switch.
type cloudStatus struct {
	cloud.Status
	AllowHubAutoAccept bool `json:"allow_hub_auto_accept"`
	// Bootstrap is the last one-time-credential enrollment's outcome
	// ({status, detail, at}); absent when there was none.
	Bootstrap json.RawMessage `json:"bootstrap,omitempty"`
}

func (h *CloudHandler) status() cloudStatus {
	st := cloud.Status{State: cloud.StateOff}
	if m := h.manager(); m != nil {
		st = m.Status()
	}
	allow := true
	var boot json.RawMessage
	if s, err := h.ss.GetSettings(context.Background()); err == nil {
		allow = HubAutoAcceptAllowed(s)
		if v := s[cloud.BootstrapStatusSetting]; json.Valid([]byte(v)) {
			boot = json.RawMessage(v)
		}
	}
	return cloudStatus{Status: st, AllowHubAutoAccept: allow, Bootstrap: boot}
}

// Who may use the /api/cloud routes. Never the Cloud Hub (they are not in the
// tunnel table, and a tunneled request is refused here too).
const (
	allowDashboard = 1 << iota // the positively identified local dashboard
	allowCLI                   // `coral cloud connect`: a loopback command-line request with the token
)

// refuseCloud admits only someone using Coral on this computer, as Coral's own
// user. On Linux the kernel names the user behind a loopback socket: another
// user (or a pod sidecar running as another uid) is refused whatever it sends,
// before any other rule, since browser headers can be forged with curl. Then
// either the dashboard (loopback peer, localhost Host, same-origin browser
// fetch, no tunnel stamp, no Bearer credential) or the command line with the
// 0600 token from Coral's data folder, as allowed.
func (h *CloudHandler) refuseCloud(w http.ResponseWriter, r *http.Request, allow int) bool {
	if cloud.Tunneled(r) {
		errForbidden(w, "Cloud connector settings are only available on this computer")
		return true
	}
	if same, supported := h.sameUser(r); supported && !same {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Cloud connector settings are only available to the user Coral runs as", "code": "other_user"})
		return true
	}
	if allow&allowDashboard != 0 && r.Header.Get(cloud.CLIAuthHeader) == "" && localDashboard(r) {
		return false
	}
	if allow&allowCLI != 0 && r.Header.Get(cloud.CLIAuthHeader) != "" && localCLI(r) {
		// The MAC covers the body, so read it here and give the handler a copy.
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		r.Body = io.NopCloser(bytes.NewReader(body))
		if err == nil && h.cliAuth.Verify(r, body) {
			return false
		}
	}
	if allow&allowDashboard != 0 {
		// The Settings screen shows this as is, with a copy button for the command
		// (#3285): connecting is for someone at this computer, never a remote browser.
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":   "Connecting has to happen on the computer running Coral. On that computer, open this dashboard, or run: coral cloud connect",
			"code":    "local_dashboard_required",
			"command": "coral cloud connect",
		})
	} else {
		errForbidden(w, "Only `coral cloud connect`, run as the user Coral runs as on this computer, can do this")
	}
	return true
}

// CLIHello: POST /api/cloud/cli-hello {"nonce": c}, step 1 of the command
// line's handshake (cloud.CLIAuth): the server proves it knows the token. Only a
// loopback command-line request as Coral's user, never through the tunnel.
func (h *CloudHandler) CLIHello(w http.ResponseWriter, r *http.Request) {
	if cloud.Tunneled(r) {
		errForbidden(w, "Cloud connector settings are only available on this computer")
		return
	}
	if same, supported := h.sameUser(r); supported && !same {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Cloud connector settings are only available to the user Coral runs as", "code": "other_user"})
		return
	}
	if !localCLI(r) {
		errForbidden(w, "Only `coral cloud connect`, run on this computer, can do this")
		return
	}
	if !requireJSON(w, r) {
		return
	}
	var body struct {
		Nonce string `json:"nonce"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "nonce is required")
		return
	}
	sn, proof, err := h.cliAuth.Hello(body.Nonce)
	if err != nil {
		errBadRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"nonce": sn, "proof": proof})
}

// requireJSON makes state-changing calls non-"simple" cross-site requests, so a web
// page in the user's browser cannot toggle connector settings without a CORS preflight.
func requireJSON(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		errBadRequest(w, "Content-Type must be application/json")
		return false
	}
	return true
}

// GetStatus: GET /api/cloud/status
func (h *CloudHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	if h.refuseCloud(w, r, allowDashboard|allowCLI) {
		return
	}
	writeJSON(w, http.StatusOK, h.status())
}

// Connect: POST /api/cloud/connect — starts enrollment and returns the user code.
func (h *CloudHandler) Connect(w http.ResponseWriter, r *http.Request) {
	if h.refuseCloud(w, r, allowDashboard|allowCLI) || !requireJSON(w, r) {
		return
	}
	m := h.manager()
	if m == nil {
		errBadRequest(w, cloud.ErrNotConfigured.Error())
		return
	}
	if err := m.Connect(r.Context()); err != nil {
		if err == cloud.ErrNotConfigured {
			errBadRequest(w, err.Error())
			return
		}
		var rl *cloud.RateLimitedError
		if errors.As(err, &rl) {
			if rl.RetryAfter > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(int(rl.RetryAfter.Seconds())))
			}
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": rl.Error()})
			return
		}
		if errors.Is(err, cloud.ErrHubUnavailable) {
			// Transient (unreachable, or a hub server error): 503, so the CLI says to try again.
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Could not start enrollment: Coral Hub could not be reached or had an error; try again in a minute"})
			return
		}
		errInternalServer(w, "Could not start enrollment; check that the hub is reachable")
		return
	}
	writeJSON(w, http.StatusOK, h.status())
}

// CancelConnect: POST /api/cloud/connect/cancel — stops an enrollment in
// progress (Ctrl-C in `coral cloud connect`); never undoes a connection.
func (h *CloudHandler) CancelConnect(w http.ResponseWriter, r *http.Request) {
	if h.refuseCloud(w, r, allowDashboard|allowCLI) || !requireJSON(w, r) {
		return
	}
	if m := h.manager(); m != nil {
		m.CancelEnrollment(r.Context())
	}
	writeJSON(w, http.StatusOK, h.status())
}

// Disconnect: POST /api/cloud/disconnect — forgets the token locally.
func (h *CloudHandler) Disconnect(w http.ResponseWriter, r *http.Request) {
	if h.refuseCloud(w, r, allowDashboard|allowCLI) || !requireJSON(w, r) {
		return
	}
	m := h.manager()
	if m == nil {
		errBadRequest(w, cloud.ErrNotConfigured.Error())
		return
	}
	if err := m.Disconnect(r.Context()); err != nil {
		errInternalServer(w, "Could not disconnect")
		return
	}
	writeJSON(w, http.StatusOK, h.status())
}

// HubOperatorDisabledSetting is the local owner opt-out "Don't let Coral Hub act as the
// Operator" (#3404). Off by default: a Coral Hub user at Interact or Launch acts as the
// Operator (task planning, cross-team posts, completing others' tasks), as before
// v1.4.18, with every use attributed to the validated Hub user. It is kept when the Hub
// link is removed. Local only: the hub can read it (GET /api/system/hub-operator, view
// tier) but never change it.
const HubOperatorDisabledSetting = "hub_operator_disabled"

// legacyHubOperatorSetting is #3377's opt-in (v1.4.18), "hub_acts_as_operator". It is
// dropped at startup (DropLegacyHubOperatorSetting): the opt-out replaces it, and its "true"
// means what the opt-out's default now means.
const legacyHubOperatorSetting = "hub_acts_as_operator"

// DropLegacyHubOperatorSetting removes #3377's hub_acts_as_operator, if present.
func DropLegacyHubOperatorSetting(ctx context.Context, ss *store.SessionStore) {
	if ok, err := ss.DeleteSetting(ctx, legacyHubOperatorSetting); err == nil && ok {
		slog.Info("[cloud] dropped the v1.4.18 setting hub_acts_as_operator: Coral Hub acts as the Operator unless 'Don't let Coral Hub act as the Operator' is on")
	}
}

func hubOperatorDisabled(settings map[string]string) bool {
	return settings[HubOperatorDisabledSetting] == "true"
}

func (h *CloudHandler) hubLinked() bool {
	if h.linked != nil {
		return h.linked()
	}
	m := h.manager()
	if m == nil {
		return false
	}
	switch m.Status().State {
	case cloud.StateConnected, cloud.StateConnecting, cloud.StateRetrying:
		return true
	}
	return false
}

// HubOperatorAllowed reports whether this tunnel request may act as the Operator: it
// carries the connector's own stamp, this server is linked to Coral Hub, and the owner
// has not turned on the opt-out. The tunnel's route table has already admitted the
// request at the effective mode (a View request never reaches a board write).
func (h *CloudHandler) HubOperatorAllowed(r *http.Request) bool {
	if !cloud.TunneledAuthentic(r) || !h.hubLinked() {
		return false
	}
	s, err := h.ss.GetSettings(r.Context())
	return err == nil && !hubOperatorDisabled(s)
}

// HubOperator: GET/PUT /api/system/hub-operator. On this computer, from the dashboard:
// {"disabled": bool} turns the opt-out on or off ({"enabled": bool}, the v1.4.18 form,
// is its inverse); the answer adds whether a Hub link exists and the recent Operator
// actions taken through Coral Hub. Through the tunnel only GET, read only, and only
// {"enabled", "disabled"}, so the Hub can show it.
func (h *CloudHandler) HubOperator(w http.ResponseWriter, r *http.Request) {
	if cloud.Tunneled(r) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		s, _ := h.ss.GetSettings(r.Context())
		off := hubOperatorDisabled(s)
		writeJSON(w, http.StatusOK, map[string]any{"enabled": !off, "disabled": off})
		return
	}
	if h.refuseCloud(w, r, allowDashboard) {
		return
	}
	if r.Method == http.MethodPut {
		if !requireJSON(w, r) {
			return
		}
		var body struct {
			Disabled *bool `json:"disabled"`
			Enabled  *bool `json:"enabled"`
		}
		if err := decodeJSON(r, &body); err != nil || (body.Disabled == nil) == (body.Enabled == nil) {
			errBadRequest(w, "disabled (true or false) is required")
			return
		}
		off := body.Disabled != nil && *body.Disabled || body.Enabled != nil && !*body.Enabled
		if err := h.ss.SetSetting(r.Context(), HubOperatorDisabledSetting, strconv.FormatBool(off)); err != nil {
			errInternalServer(w, err.Error())
			return
		}
		slog.Info("[cloud] 'Don't let Coral Hub act as the Operator' changed on this computer", "disabled", off)
	}
	s, _ := h.ss.GetSettings(r.Context())
	off := hubOperatorDisabled(s)
	out := map[string]any{"enabled": !off, "disabled": off, "linked": h.hubLinked(), "recent_actions": []board.HubOperatorAction{}}
	if h.bs != nil {
		if recent, err := h.bs.RecentHubOperatorActions(r.Context(), 50); err == nil {
			out["recent_actions"] = recent
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// SetLinkedCheck replaces how the handler tells whether a Hub link exists (tests that
// run a tunnel without enrolling with a hub).
func (h *CloudHandler) SetLinkedCheck(fn func() bool) { h.linked = fn }

// SetBoardStore gives the handler the board store, for the recent Operator actions
// taken through Coral Hub (#3404).
func (h *CloudHandler) SetBoardStore(bs *board.Store) { h.bs = bs }

// SetLoginMinter gives the handler the dashboard login-token minter (#3387).
func (h *CloudHandler) SetLoginMinter(fn func() (string, error)) {
	h.mu.Lock()
	h.mintLogin = fn
	h.mu.Unlock()
}

// LoginToken: POST /api/system/login-token: a fresh single-use dashboard sign-in
// token for `coral open` (#3387). Only the coral CLI on this computer, through its
// HMAC handshake (or the dashboard here).
func (h *CloudHandler) LoginToken(w http.ResponseWriter, r *http.Request) {
	if h.refuseCloud(w, r, allowDashboard|allowCLI) {
		return
	}
	h.mu.RLock()
	mint := h.mintLogin
	h.mu.RUnlock()
	if mint == nil {
		errBadRequest(w, "sign-in links are not available")
		return
	}
	token, err := mint()
	if err != nil {
		errInternalServer(w, "could not mint a sign-in link")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"login_token": token, "path": "/?login_token=" + token, "expires_in": int(auth.LoginTokenTTL.Seconds())})
}

// SetIdentityObserver gives the handler the identity observer for its report (#3387).
func (h *CloudHandler) SetIdentityObserver(o *IdentityObserver) {
	h.mu.Lock()
	h.identity = o
	h.mu.Unlock()
}

// IdentityReport: GET /api/system/identity-report: who called and what strict mode
// would refuse (#3387, phase 1). Only the dashboard on this computer or the coral CLI
// (its HMAC handshake): an agent can't read other sessions' mismatch details.
func (h *CloudHandler) IdentityReport(w http.ResponseWriter, r *http.Request) {
	if h.refuseCloud(w, r, allowDashboard|allowCLI) {
		return
	}
	h.mu.RLock()
	o := h.identity
	h.mu.RUnlock()
	if o == nil {
		writeJSON(w, http.StatusOK, map[string]any{"mode": "off"})
		return
	}
	writeJSON(w, http.StatusOK, o.Report())
}

// SetMaxMode: PUT /api/cloud/max-mode {"mode": "view"|"interact"|"launch"}
func (h *CloudHandler) SetMaxMode(w http.ResponseWriter, r *http.Request) {
	if h.refuseCloud(w, r, allowDashboard|allowCLI) || !requireJSON(w, r) {
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "mode is required")
		return
	}
	mode, ok := tunnel.ParseMode(body.Mode)
	if !ok {
		errBadRequest(w, "mode must be view, interact or launch")
		return
	}
	if err := h.ss.SetSetting(r.Context(), MaxModeSetting, mode.String()); err != nil {
		errInternalServer(w, "Could not save the setting")
		return
	}
	if mode < tunnel.TierLaunch {
		// Enabling auto-accept from the hub needs Launch: what it enabled ends.
		if _, err := h.ss.ClearHubAutoAccept(r.Context(), store.AutoAcceptModeLowered); err != nil {
			errInternalServer(w, "Could not turn off Coral Hub auto-accept")
			return
		}
	}
	if m := h.manager(); m != nil {
		m.ModeChanged(r.Context())
	}
	writeJSON(w, http.StatusOK, h.status())
}

// SetAllowHubAutoAccept: PUT /api/cloud/allow-auto-accept {"allow": bool}. Local
// dashboard only (never through the tunnel). Off turns off every agent's
// auto-accept that the Cloud Hub enabled.
func (h *CloudHandler) SetAllowHubAutoAccept(w http.ResponseWriter, r *http.Request) {
	if h.refuseCloud(w, r, allowDashboard|allowCLI) || !requireJSON(w, r) {
		return
	}
	var body struct {
		Allow *bool `json:"allow"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Allow == nil {
		errBadRequest(w, "allow is required")
		return
	}
	v := "true"
	if !*body.Allow {
		v = "false"
	}
	if err := h.ss.SetSetting(r.Context(), AllowHubAutoAcceptSetting, v); err != nil {
		errInternalServer(w, "Could not save the setting")
		return
	}
	if !*body.Allow {
		if _, err := h.ss.ClearHubAutoAccept(r.Context(), store.AutoAcceptHubAllowOff); err != nil {
			errInternalServer(w, "Could not turn off Coral Hub auto-accept")
			return
		}
	}
	writeJSON(w, http.StatusOK, h.status())
}

// localCLI admits only a command-line client on this computer: a loopback peer
// with a localhost Host, no tunnel stamp (any value), and nothing a browser
// sends (Origin, Referer, fetch metadata), so neither the Cloud Hub, another
// machine nor a web page can reach it.
func localCLI(r *http.Request) bool {
	if viaTunnel(r) || r.Header.Get("Origin") != "" || r.Header.Get("Referer") != "" || r.Header.Get("Sec-Fetch-Site") != "" {
		return false
	}
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	if ip := net.ParseIP(peer); ip == nil || !ip.IsLoopback() {
		return false
	}
	return localHostOK(r.Host)
}

// Redeem: POST /api/cloud/redeem, from `coral cloud connect`. The running
// server redeems, so it stays the only writer of the identity. The command
// reads the credential file itself, as the user who runs it, and sends the
// credential only in the Authorization header and the proof's contents as
// {"proof": {...}}: the server never opens a path a caller names (any local
// user can reach this route). {"check": true}, with no credential, asks only
// whether a bootstrap could proceed, so the command reads nothing when it
// could not; it passes the same gate as a redeem, so it is no oracle for
// another user. Local command line only, as Coral's user with its token; never
// through the tunnel. Answers 200
// {status, detail, exit_code}; status "ready" for a check that may proceed.
func (h *CloudHandler) Redeem(w http.ResponseWriter, r *http.Request) {
	if h.refuseCloud(w, r, allowCLI) || !requireJSON(w, r) {
		return
	}
	var body struct {
		Check bool            `json:"check"`
		Proof json.RawMessage `json:"proof"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || dec.More() {
		errBadRequest(w, "send only {\"check\": true} or {\"proof\": {...}}; never a file path")
		return
	}
	cred, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if bearer == body.Check {
		errBadRequest(w, "send either check or the credential in the Authorization header")
		return
	}
	src := cloud.CredentialSource{Value: []byte(cred)}
	if len(body.Proof) > 0 && string(body.Proof) != "null" {
		src.Proof = body.Proof
	}
	m := h.manager()
	var res cloud.BootstrapResult
	switch {
	case m == nil:
		res = cloud.BootstrapResult{Status: cloud.BootstrapNoHub, Detail: "no Coral Hub is configured (--cloud-url)"}
	case body.Check:
		if res = m.PreCheck(r.Context()); res.Status == cloud.BootstrapNone {
			res.Status = cloud.BootstrapReady
		}
	default:
		hostname, _ := os.Hostname()
		res = m.Redeem(r.Context(), h.ss, hostname, src)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": res.Status, "detail": res.Detail, "exit_code": cloud.BootstrapExitCode(res.Status)})
}
