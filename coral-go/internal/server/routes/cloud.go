package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/subgentic/coral/internal/auth"
	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/cliauth"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/httputil"
	"github.com/subgentic/coral/internal/peercred"
	"github.com/subgentic/coral/internal/secrets"
	setreg "github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/redact"
	"github.com/subgentic/coral/pkg/tunnel"
)

// MaxModeSetting is the user setting holding this server's maximum cloud access mode:
// "view" (default), "interact" or "launch". It is local: the connector reads it on every
// request, so neither the hub nor a tunneled request can change it (the /api/cloud
// routes are not in the tunnel route table).
const MaxModeSetting = "cloud_max_mode"

// AllowHubAutoAcceptSetting is the local owner switch "Coral Hub can turn on
// auto-accept" (#3182): auto-accept from the hub, and a hub launch, team launch,
// restart or task run that skips permission prompts (#126 F4). It is ON unless the
// owner turned it off (the Operator's decision, #5840: Coral Hub is the primary source
// for a connected computer; an explicit off stays off): Launch lets the hub start agents,
// this lets them act without asking. The owner of this computer sets it here (Settings >
// Network and Hub, PUT /api/cloud/allow-auto-accept, local only), and the Coral Hub OWNER
// (the account this computer is linked to) through POST /api/hub-auto-accept {"allow": bool}; turning it off ends every enable that came from the hub.
const AllowHubAutoAcceptSetting = "cloud_allow_hub_auto_accept"

// HubRelaunchSetting is the local switch "Let Coral Hub relaunch this Coral to install updates" (#5280). On unless set to "false",
// read from the settings store on every relaunch request, and never reachable through the tunnel: its write has its own route, and
// PUT /api/settings refuses it like the mode cap.
const HubRelaunchSetting = "hub_relaunch_updates"

// HubCrossTeamSetting is the local lock "Let Coral Hub change cross-team settings" (#5572). On unless set to "false" (the default is the settings registry's, written by code); when it is off the Coral Hub
// owner's dedicated routes refuse both directions. Written only by PUT /api/cloud/hub-cross-team, never through the tunnel: its write has its own route, and PUT /api/settings refuses it like the mode cap.
const HubCrossTeamSetting = "hub_cross_team_settings"

// HubCrossTeamAllowed reads the lock (default on).
func HubCrossTeamAllowed(settings map[string]string) bool {
	return setreg.Bool(settings, HubCrossTeamSetting)
}

// HubRelaunchAllowed reads the switch (default on).
func HubRelaunchAllowed(settings map[string]string) bool {
	return setreg.Bool(settings, HubRelaunchSetting)
}

// RedactTokensSetting is the local switch "hide tokens from Coral Hub viewers" (#3560): the
// connector replaces well-known credential formats and private keys in View replies before
// they leave this computer. On unless set to "false"; local only, like the mode cap.
const RedactTokensSetting = "hub_redact_tokens"

// HubRedactTokens reads the switch (default on).
func HubRedactTokens(settings map[string]string) bool {
	return setreg.Bool(settings, RedactTokensSetting)
}

// RedactTokensFromSettings reads the switch for each redacted reply, from the settings at most
// a second old (a change applies within a second); an unreadable store means on.
func RedactTokensFromSettings(ss *store.SessionStore) func() bool {
	var mu sync.Mutex
	var at time.Time
	on := true
	return func() bool {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(at) < time.Second {
			return on
		}
		s, err := ss.GetSettings(context.Background())
		on, at = err != nil || HubRedactTokens(s), time.Now()
		return on
	}
}

// RedactKeywordsSetting is the local switch for the keyword rules of the connector's redactor (#5141): the value after a name such as
// api_key, token or password is replaced in what goes to Coral Hub whatever it looks like. On unless set off; written only through
// PUT /api/settings, which the tunnel never reaches, so the hub cannot change it. The environment variable CORAL_REDACT_KEYWORDS=off turns them off too.
const RedactKeywordsSetting = "hub_redact_keywords"

// RedactKeywordsFromSettings reads the switch for each redacted reply, from the settings at most a second old; an unreadable store means on.
func RedactKeywordsFromSettings(ss *store.SessionStore) func() bool {
	var mu sync.Mutex
	var at time.Time
	on := true
	return func() bool {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(at) < time.Second {
			return on
		}
		s, err := ss.GetSettings(context.Background())
		on, at = err != nil || setreg.Bool(s, RedactKeywordsSetting), time.Now()
		return on
	}
}

// keywordSwitch is the keyword rules' switch for the hub-bound paths of this package (chat_trim, saved, the scans of what is sent to another Coral):
// set once at startup (SetKeywordSwitch) from the same reader the cloud connector's redactor has; nil is on. Read on each use.
var keywordSwitch atomic.Pointer[func() bool]

// SetKeywordSwitch sets keywordSwitch.
func SetKeywordSwitch(on func() bool) { keywordSwitch.Store(&on) }

func keywordsOn() bool {
	if f := keywordSwitch.Load(); f != nil && *f != nil {
		return (*f)()
	}
	return true
}

// requestRedactor is the redactor for text a request will show: Coral Hub's (the keyword rules too) when the request came through the tunnel,
// otherwise the local one. The reply is redacted again on its way out, but a field cut or kept first (chat_trim, saved) must be redacted
// before it is cut, or a fragment of a value the keyword rules would take can pass (#5172).
func requestRedactor(r *http.Request) redact.Redactor {
	if r != nil && viaTunnel(r) {
		return secrets.HubRedactor(keywordsOn)
	}
	return secrets.Redactor()
}

// HubAutoAcceptAllowed reads the switch (default on, #5840).
func HubAutoAcceptAllowed(settings map[string]string) bool {
	return setreg.Bool(settings, AllowHubAutoAcceptSetting)
}

// MaxModeFromSettings reads the local maximum mode. Anything missing or unrecognised is view.
func MaxModeFromSettings(ss *store.SessionStore) func() tunnel.Mode {
	return func() tunnel.Mode {
		s, err := ss.GetSettings(context.Background())
		if err != nil {
			return tunnel.TierView
		}
		if m, ok := tunnel.ParseMode(setreg.Raw(s, MaxModeSetting)); ok {
			return m
		}
		return tunnel.TierView
	}
}

// CloudHandler serves the "Connect to Coral Hub" settings section.
type CloudHandler struct {
	// OfferAtLaunch is config.OffersCloudConnect (#3459), set by the server.
	OfferAtLaunch bool

	// Hub mode (#3460), set by the server: whether this process runs as a hub, whether
	// --hub or CORAL_HUB=1 forced it, and how to restart, when this process can.
	HubModeRunning bool
	HubModeForced  bool
	Restart        func() error
	// QuitForUpdate, set by the server where a tray or the desktop app supervises it, makes this process quit with UpdateQuitExitCode after the answer is
	// written, so the updater (started just before) has Coral to itself and the supervisor does not start the server again (#166).
	QuitForUpdate func() error
	// stagedUpdate, launchUpdater and installing belong to InstallUpdate (system_update_install.go); the three funcs are nil in production (the staged
	// folder, the updater's own answer to "will you install?" and the Windows updater launch are used) and are set by a test.
	stagedUpdate     func(context.Context) (string, error)
	preflightUpdater func(context.Context) error
	launchUpdater    func(context.Context) error
	installing       int32
	// The terminal backend (#3634), set by the server: the one this run uses, and whether
	// --backend forced it.
	BackendRunning string
	BackendForced  bool

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
	h.mu.RLock()
	tok := h.cliToken
	h.mu.RUnlock()
	if err := cloud.RemoveCLIToken(h.dataDir, tok); err != nil {
		slog.Warn("cloud: could not remove the command-line token", "error", err)
	}
}

// RotateCLIToken writes a new command-line token and makes the server use it, after
// `coral reset-password` (#4041): whatever read the old one (an agent, a copy) can no longer
// sign as the Operator. Pending hellos are dropped; each command line reads the new token on
// its next request.
func (h *CloudHandler) RotateCLIToken() {
	tok, err := cloud.WriteCLIToken(h.dataDir)
	if err != nil {
		// Fail closed: an old token that may be known must not keep signing.
		slog.Warn("cloud: could not write a new command-line token after a password reset; the command line is off until Coral restarts", "error", err)
		tok = ""
	}
	h.cliAuth.Rotate(tok)
	h.mu.Lock()
	h.cliToken = tok
	h.mu.Unlock()
	slog.Info("cloud: the command-line token was replaced after a password reset")
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
	// MaxMode is the saved cap (the connector's own max_mode is what the tunnel enforces); MaxModeSaved says the owner chose it (Connect's question, or Settings): an unset cap reads as view, so without this an
	// existing install and a new one look the same, and the Connect screen could not keep what an install has while offering Launch to a new one.
	MaxModeSaved bool `json:"max_mode_saved"`
	// RedactTokens: tokens and private keys are hidden from Coral Hub viewers (#3560).
	RedactTokens bool `json:"redact_tokens"`
	// HubRelaunchUpdates is the local switch "Let Coral Hub relaunch this Coral to install updates" (#5280).
	HubRelaunchUpdates bool `json:"hub_relaunch_updates"`
	// HubCrossTeamSettings is the local lock "Let Coral Hub change cross-team settings" (#5572); on, the Coral Hub owner may turn a team's cross-team messaging and file sharing on or off.
	HubCrossTeamSettings bool `json:"hub_cross_team_settings"`
	// AllowHubAutoAcceptChanged is who last changed the auto-accept switch and when ({by: "hub"|"local", user, at}), shown beside it in Settings > Network and Hub (#5840); absent until it was changed.
	AllowHubAutoAcceptChanged json.RawMessage `json:"allow_hub_auto_accept_changed,omitempty"`
	// Bootstrap is the last one-time-credential enrollment's outcome
	// ({status, detail, at}); absent when there was none.
	Bootstrap json.RawMessage `json:"bootstrap,omitempty"`
	// OfferAtLaunch: the dashboard opens its first-launch Connect to Coral Hub
	// screen only when this server was started to offer it (#3459).
	OfferAtLaunch bool `json:"offer_at_launch"`
	// SecretKey is present only when the key that seals the Hub token and the remote servers'
	// API keys can't be used (#3698): the dashboard then offers the re-key, listing what it discards.
	SecretKey *store.SecretKeyState `json:"secret_key,omitempty"`
}

func (h *CloudHandler) status() cloudStatus {
	st := cloud.Status{State: cloud.StateOff}
	if m := h.manager(); m != nil {
		st = m.Status()
	}
	allow, redact, relaunch, crossTeam := true, true, true, true
	var boot, changed json.RawMessage
	modeSaved := false
	if s, err := h.ss.GetSettings(context.Background()); err == nil {
		_, modeSaved = tunnel.ParseMode(setreg.Raw(s, MaxModeSetting))
		if v := setreg.Raw(s, setreg.HubAutoAcceptChanged); json.Valid([]byte(v)) {
			changed = json.RawMessage(v)
		}
		allow, redact, relaunch, crossTeam = HubAutoAcceptAllowed(s), HubRedactTokens(s), HubRelaunchAllowed(s), HubCrossTeamAllowed(s)
		if v := setreg.Raw(s, cloud.BootstrapStatusSetting); json.Valid([]byte(v)) {
			boot = json.RawMessage(v)
		}
	}
	out := cloudStatus{Status: st, AllowHubAutoAccept: allow, MaxModeSaved: modeSaved, RedactTokens: redact, HubRelaunchUpdates: relaunch, HubCrossTeamSettings: crossTeam, AllowHubAutoAcceptChanged: changed, Bootstrap: boot, OfferAtLaunch: h.OfferAtLaunch}
	if rs := h.secrets(); rs != nil {
		if ks, err := rs.KeyState(context.Background()); err == nil && ks.State == "refused" {
			out.SecretKey = &ks
		}
	}
	return out
}

// secrets is the store of the key that seals the Hub token and the remote servers' API keys.
func (h *CloudHandler) secrets() *store.RemoteServerStore {
	if h.ss == nil {
		return nil
	}
	return store.NewRemoteServerStore(h.ss.DB(), h.dataDir)
}

// Rekey replaces a secret key Coral refuses (for example one other accounts could read) with a
// new one, discarding what the old one sealed: the Coral Hub enrollment (the tunnel stops first)
// and the remote servers' API keys, which must be entered again (#3698). It is the Operator's,
// on this computer only (never through Coral Hub), and needs {"confirm": true}. A usable key is
// never replaced (409).
// POST /api/cloud/rekey
func (h *CloudHandler) Rekey(w http.ResponseWriter, r *http.Request) {
	if h.refuseCloud(w, r, allowDashboard|allowCLI) || !requireJSON(w, r) {
		return
	}
	var body struct {
		Confirm bool `json:"confirm"`
	}
	if err := decodeJSONLimit(r, &body, 4096); err != nil || !body.Confirm {
		errBadRequest(w, `re-keying discards the Coral Hub connection and the remote servers' API keys: send {"confirm": true}`)
		return
	}
	rs := h.secrets()
	if rs == nil {
		errInternalServer(w, "no store")
		return
	}
	ks, err := rs.KeyState(r.Context())
	if err != nil {
		errInternalServer(w, "could not read the secret key's state")
		return
	}
	switch ks.State {
	case "refused":
	case "unreadable": // an I/O or access error, not a refusal of the key: no re-key (#3698)
		writeJSON(w, http.StatusConflict, map[string]any{"error": store.ErrKeyNotRefused.Error() + ": " + ks.Message, "code": "key_unreadable", "state": ks.State})
		return
	default:
		writeJSON(w, http.StatusConflict, map[string]any{"error": store.ErrRekeyNotNeeded.Error(), "code": "rekey_not_needed", "state": ks.State})
		return
	}
	if m := h.manager(); m != nil {
		if err := m.Disconnect(r.Context()); err != nil && !errors.Is(err, cloud.ErrNotConfigured) {
			errInternalServer(w, "could not stop the Coral Hub connection")
			return
		}
	}
	if err := rs.Rekey(r.Context()); err != nil {
		if errors.Is(err, store.ErrRekeyNotNeeded) {
			writeError(w, http.StatusConflict, "rekey_not_needed", err.Error())
			return
		}
		if errors.Is(err, store.ErrKeyNotRefused) {
			writeError( // it changed between the check and the re-key
				w, http.StatusConflict, "key_unreadable", err.Error())
			return
		}
		errInternalServer(w, "re-key failed: "+err.Error())
		return
	}
	slog.Info("secret key replaced by a re-key; the Coral Hub enrollment and remote API keys were discarded",
		"remote_server_keys", ks.RemoteServerKeys, "had_hub_enrollment", ks.HubURL != "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "discarded_remote_server_keys": ks.RemoteServerKeys,
		"discarded_hub_enrollment": ks.HubURL != "", "status": h.status()})
}

// Who may use the /api/cloud routes. Never the Cloud Hub (they are not in the
// tunnel table, and a tunneled request is refused here too).
const (
	allowDashboard = 1 << iota // the positively identified local dashboard
	allowCLI                   // `coral cloud connect`: a loopback command-line request with the token
)

// refuseCloud admits only someone using Coral on this computer, as Coral's own
// user. On Linux, macOS and Windows the kernel names the user behind a loopback
// socket (peercred): another user (or a pod sidecar running as another uid) is
// refused whatever it sends,
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
		writeError(w, http.StatusForbidden, "other_user", "Cloud connector settings are only available to the user Coral runs as")
		return true
	}
	if allow&allowDashboard != 0 && r.Header.Get(cloud.CLIAuthHeader) == "" && localDashboard(r) {
		return false
	}
	if allow&allowCLI != 0 && cliauth.Verified(r) {
		return false // strict local auth checked the signature already (#3418)
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
		writeError(w, http.StatusForbidden, "other_user", "Cloud connector settings are only available to the user Coral runs as")
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
	// Only a direct local peer gets here (localCLI), so every hello is one caller: a program
	// can't get a bucket per loopback address and fill the pending table (#4041).
	sn, proof, err := h.cliAuth.HelloFrom("local", body.Nonce)
	if errors.Is(err, cliauth.ErrHelloLimit) {
		httputil.WriteActionLimit(w, 1, "cli_hello_limit", err.Error())
		return
	}
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

// Connect: POST /api/cloud/connect — starts enrollment and returns the user code. The body is optional: {"mode": "view|interact|launch", "allow_auto_accept": bool} is the answer to "What may Coral Hub do on this computer?", saved in this call before the enrollment starts (see decodeConnectAnswer).
func (h *CloudHandler) Connect(w http.ResponseWriter, r *http.Request) {
	if h.refuseCloud(w, r, allowDashboard|allowCLI) || !requireJSON(w, r) {
		return
	}
	m := h.manager()
	if m == nil {
		errBadRequest(w, cloud.ErrNotConfigured.Error())
		return
	}
	// The optional answer to "What may Coral Hub do on this computer?" (the dashboard's Connect screen): saved here, before the enrollment starts, in this one call, so nothing is half done. A closed body
	// (unknown fields are refused); nothing is saved when any part is invalid. This route is local only (refuseCloud above), so Coral Hub can never choose its own mode through the tunnel.
	if !m.Status().Configured { // nothing is saved for a server that has no hub to connect to
		errBadRequest(w, cloud.ErrNotConfigured.Error())
		return
	}
	ans, err := decodeConnectAnswer(r)
	if err != nil {
		errBadRequest(w, err.Error())
		return
	}
	save := context.WithoutCancel(r.Context()) // a page that goes away mid-call must not leave half of the answer saved
	if ans.mode != nil {
		if err := h.applyMaxMode(save, *ans.mode); err != nil {
			errInternalServer(w, err.Error())
			return
		}
	}
	if ans.allowAutoAccept != nil {
		if err := h.applyAllowAutoAccept(save, *ans.allowAutoAccept); err != nil {
			errInternalServer(w, err.Error())
			return
		}
	}
	if err := m.Connect(r.Context()); err != nil {
		if err == cloud.ErrNotConfigured {
			errBadRequest(w, err.Error())
			return
		}
		var rl *cloud.RateLimitedError
		if errors.As(err, &rl) {
			// Coral Hub refused to start an enrollment now: a limit on this action, not this
			// server being busy (#3434).
			httputil.WriteActionLimit(w, int(math.Ceil(rl.RetryAfter.Seconds())), "rate_limited", rl.Error())
			return
		}
		if errors.Is(err, cloud.ErrHubUnavailable) {
			writeError( // Transient (unreachable, or a hub server error): 503, so the CLI says to try again.
				w, http.StatusServiceUnavailable, "", "Could not start enrollment: Coral Hub could not be reached or had an error; try again in a minute")
			return
		}
		errInternalServer(w, "Could not start enrollment; check that the hub is reachable")
		return
	}
	writeJSON(w, http.StatusOK, h.status())
}

// connectAnswer is the decoded optional body of POST /api/cloud/connect.
type connectAnswer struct {
	mode            *tunnel.Mode
	allowAutoAccept *bool
}

// decodeConnectAnswer reads POST /api/cloud/connect's optional body {"mode": "view|interact|launch", "allow_auto_accept": bool}: both fields optional, an empty body or {} is the plain connect the
// command line sends, unknown fields and a second value are refused, and the body is capped at 256 bytes.
func decodeConnectAnswer(r *http.Request) (connectAnswer, error) {
	var out connectAnswer
	var body struct {
		Mode            *string `json:"mode"`
		AllowAutoAccept *bool   `json:"allow_auto_accept"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 257))
	if err != nil || len(raw) > 256 {
		return out, errors.New("the body is too large")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		return out, errors.New(`the body is {"mode": "view"|"interact"|"launch", "allow_auto_accept": true|false}, both optional`)
	}
	if dec.More() {
		return out, errors.New("the body must be one JSON object")
	}
	if body.Mode != nil {
		m, ok := tunnel.ParseMode(*body.Mode)
		if !ok {
			return out, errors.New("mode must be view, interact or launch")
		}
		out.mode = &m
	}
	out.allowAutoAccept = body.AllowAutoAccept
	return out, nil
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
	return setreg.Bool(settings, HubOperatorDisabledSetting)
}

// HubOperatorDisabled reports whether "Don't let Coral Hub act as the Operator" (#3404) is on in
// settings; it also refuses Sign in with Coral Hub (#3966).
func HubOperatorDisabled(settings map[string]string) bool { return hubOperatorDisabled(settings) }

// HubLinked reports whether this computer is linked to Coral Hub (enrolled, not revoked).
func (h *CloudHandler) HubLinked() bool { return h.hubLinked() }

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
// {"disabled": bool} turns the opt-out on or off; the answer adds whether a Hub link exists and
// the recent Operator actions taken through Coral Hub. Through the tunnel only GET, read only,
// and only {"disabled"}.
func (h *CloudHandler) HubOperator(w http.ResponseWriter, r *http.Request) {
	if cloud.Tunneled(r) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		s, _ := h.ss.GetSettings(r.Context())
		off := hubOperatorDisabled(s)
		writeJSON(w, http.StatusOK, map[string]any{"disabled": off})
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
		}
		if err := decodeJSON(r, &body); err != nil || body.Disabled == nil {
			errBadRequest(w, "disabled (true or false) is required")
			return
		}
		off := *body.Disabled
		if err := h.ss.SetSetting(r.Context(), HubOperatorDisabledSetting, strconv.FormatBool(off)); err != nil {
			errInternalServer(w, err.Error())
			return
		}
		slog.Info("[cloud] 'Don't let Coral Hub act as the Operator' changed on this computer", "disabled", off)
	}
	s, _ := h.ss.GetSettings(r.Context())
	off := hubOperatorDisabled(s)
	out := map[string]any{"disabled": off, "linked": h.hubLinked(), "recent_actions": []board.HubOperatorAction{}}
	if h.bs != nil {
		if recent, err := h.bs.RecentHubOperatorActions(r.Context(), 50); err == nil {
			out["recent_actions"] = recent
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// VerifyCLI checks a command-line signature for strict local auth (#3418): a loopback
// request without browser headers, from Coral's own user, signed with the command-line
// token. It uses up the nonce and returns the request marked cliauth.Verified, with its
// body restored, so the cloud routes accept it without checking again.
func (h *CloudHandler) VerifyCLI(r *http.Request) (*http.Request, bool) {
	if r.Header.Get(cloud.CLIAuthHeader) == "" || !localCLI(r) {
		return r, false
	}
	if h.OtherUser(r) {
		return r, false
	}
	var body []byte
	if r.Body != nil && r.Body != http.NoBody {
		b, err := io.ReadAll(io.LimitReader(r.Body, cliSignedBodyMax+1))
		r.Body.Close()
		if err != nil || len(b) > cliSignedBodyMax {
			return r, false
		}
		body = b
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if !h.cliAuth.Verify(r, body) {
		return r, false
	}
	return cliauth.WithVerified(r), true
}

// SameUser is the peer-user check (#3949): whether a loopback request comes from Coral's own OS
// user, and whether the system can tell at all.
func (h *CloudHandler) SameUser(r *http.Request) (same, supported bool) { return h.sameUser(r) }

// OtherUser reports whether a loopback request comes from another OS user (where the
// system can tell, peercred): such a caller is refused with code other_user.
func (h *CloudHandler) OtherUser(r *http.Request) bool {
	same, supported := h.sameUser(r)
	return supported && !same
}

// cliSignedBodyMax matches the command-line transport's limit (an artifact upload).
const cliSignedBodyMax = 32 << 20

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

// applyMaxMode saves the cap and applies it: below Launch what the hub enabled auto-accept for ends, and a running tunnel learns the new mode. The error text is for the person.
func (h *CloudHandler) applyMaxMode(ctx context.Context, mode tunnel.Mode) error {
	if err := h.ss.SetSetting(ctx, MaxModeSetting, mode.String()); err != nil {
		return errors.New("Could not save the setting")
	}
	if mode < tunnel.TierLaunch {
		// Enabling auto-accept from the hub needs Launch: what it enabled ends.
		if _, err := h.ss.ClearHubAutoAccept(ctx, store.AutoAcceptModeLowered); err != nil {
			return errors.New("Could not turn off Coral Hub auto-accept")
		}
	}
	if m := h.manager(); m != nil {
		m.ModeChanged(ctx)
	}
	return nil
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
	if err := h.applyMaxMode(r.Context(), mode); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, h.status())
}

// SetRedactTokens: PUT /api/cloud/redact-tokens {"redact": bool}. Local dashboard and the
// signed command line only (never through the tunnel). It applies to the next reply.
func (h *CloudHandler) SetRedactTokens(w http.ResponseWriter, r *http.Request) {
	if h.refuseCloud(w, r, allowDashboard|allowCLI) || !requireJSON(w, r) {
		return
	}
	var body struct {
		Redact *bool `json:"redact"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Redact == nil {
		errBadRequest(w, "redact is required")
		return
	}
	v := "true"
	if !*body.Redact {
		v = "false"
	}
	if err := h.ss.SetSetting(r.Context(), RedactTokensSetting, v); err != nil {
		errInternalServer(w, "Could not save the setting")
		return
	}
	writeJSON(w, http.StatusOK, h.status())
}

// SetHubRelaunch: PUT /api/cloud/hub-relaunch {"allow": bool}. Local dashboard and the signed command line only (never through
// the tunnel): whether Coral Hub's owner may relaunch this Coral into an update it has staged (#5280).
func (h *CloudHandler) SetHubRelaunch(w http.ResponseWriter, r *http.Request) {
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
	if err := h.ss.SetSetting(r.Context(), HubRelaunchSetting, strconv.FormatBool(*body.Allow)); err != nil {
		errInternalServer(w, "Could not save the setting")
		return
	}
	writeJSON(w, http.StatusOK, h.status())
}

// SetHubCrossTeam: PUT /api/cloud/hub-cross-team {"allow": bool}. Local dashboard and the signed command line only (never through the tunnel, any role): whether Coral Hub's owner may turn
// cross-team messaging and file sharing on or off for this computer's teams (#5572). Off refuses the Hub in both directions.
func (h *CloudHandler) SetHubCrossTeam(w http.ResponseWriter, r *http.Request) {
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
	if err := h.ss.SetSetting(r.Context(), HubCrossTeamSetting, strconv.FormatBool(*body.Allow)); err != nil {
		errInternalServer(w, "Could not save the setting")
		return
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
	if err := h.applyAllowAutoAccept(r.Context(), *body.Allow); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, h.status())
}

// applyAllowAutoAccept saves this computer's switch (off ends every auto-accept the hub enabled) and records that it was changed here.
func (h *CloudHandler) applyAllowAutoAccept(ctx context.Context, allow bool) error {
	v := "true"
	if !allow {
		v = "false"
	}
	if err := h.ss.SetSetting(ctx, AllowHubAutoAcceptSetting, v); err != nil {
		return errors.New("Could not save the setting")
	}
	if !allow {
		if _, err := h.ss.ClearHubAutoAccept(ctx, store.AutoAcceptHubAllowOff); err != nil {
			return errors.New("Could not turn off Coral Hub auto-accept")
		}
	}
	h.recordAutoAcceptChange(ctx, "local", "")
	return nil
}

// hubAutoAcceptChange is who last changed the Coral Hub auto-accept switch and when (HubAutoAcceptChanged, #5840): By is "hub" (the Coral Hub owner, User the validated hub user) or "local"
// (this computer's Settings), At an RFC 3339 time (UTC).
type hubAutoAcceptChange struct {
	By   string `json:"by"`
	User string `json:"user,omitempty"`
	At   string `json:"at"`
}

// recordAutoAcceptChange stores who changed the switch, to show beside it. A failure is logged and does not undo the change.
func (h *CloudHandler) recordAutoAcceptChange(ctx context.Context, by, user string) hubAutoAcceptChange {
	c := hubAutoAcceptChange{By: by, User: user, At: time.Now().UTC().Format(time.RFC3339)}
	b, _ := json.Marshal(c)
	if err := h.ss.SetSetting(context.WithoutCancel(ctx), setreg.HubAutoAcceptChanged, string(b)); err != nil {
		slog.Warn("[cloud] could not record who changed the Coral Hub auto-accept switch", "error", err)
	}
	return c
}

// HubOwnerSetAutoAccept: POST /api/hub-auto-accept {"allow": bool} (#5840), Coral Hub's owner turning "Coral Hub can turn on auto-accept" on or off. The route table marks it owner-only for the
// hub (tunnel.OwnerHubOnly, Launch tier): refuseNonOwner refuses anyone but the account this computer is linked to, and a request that did not come through Coral Hub, with 403 owner_only
// before anything is read. The body is closed (exactly {"allow": true|false}, 64 bytes at most, no query). It sets the same setting as the Settings switch (PUT /api/cloud/allow-auto-accept);
// turning it off ends every auto-accept the hub had enabled, as that route does. The change is recorded on this computer (who, when: shown beside the switch), in the Operator-actions
// log and in the log.
func (h *CloudHandler) HubOwnerSetAutoAccept(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if refuseNonOwner(w, r) {
		return
	}
	id := hubOwnerRequest(r)
	if r.URL.RawQuery != "" {
		errBadRequest(w, textHubAutoAcceptBody)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 65))
	if err != nil || len(raw) > 64 {
		errBadRequest(w, textHubAutoAcceptBody)
		return
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var body struct {
		Allow *bool `json:"allow"`
	}
	if err := dec.Decode(&body); err != nil || body.Allow == nil || dec.More() {
		errBadRequest(w, textHubAutoAcceptBody)
		return
	}
	allow := *body.Allow
	v := "false"
	if allow {
		v = "true"
	}
	if err := h.ss.SetSetting(r.Context(), AllowHubAutoAcceptSetting, v); err != nil {
		errInternalServer(w, "Could not save the setting")
		return
	}
	ended := 0
	if !allow {
		n, err := h.ss.ClearHubAutoAccept(r.Context(), store.AutoAcceptHubAllowOff)
		if err != nil {
			errInternalServer(w, "Could not turn off Coral Hub auto-accept")
			return
		}
		ended = n
	}
	change := h.recordAutoAcceptChange(r.Context(), "hub", id.pseudo)
	recordHubOperatorAction(r.Context(), h.bs, id.pseudo, r.Method, r.URL.Path+" allow="+v, "", http.StatusOK)
	slog.Info("[cloud] Coral Hub's owner set the Coral Hub auto-accept switch", "allow", allow, "auto_accepts_ended", ended, "hub_user", id.pseudo)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "allow_hub_auto_accept": allow, "auto_accepts_ended": ended, "changed_by": change})
}

const textHubAutoAcceptBody = `the body is {"allow": true} or {"allow": false} and nothing else (and no query)`

// localCLI admits only a command-line client on this computer: a loopback peer
// with a localhost Host, no tunnel stamp (any value), and nothing a browser
// sends (Origin, Referer, fetch metadata), so neither the Cloud Hub, another
// machine nor a web page can reach it.
func localCLI(r *http.Request) bool {
	if viaTunnel(r) || r.Header.Get("Origin") != "" || r.Header.Get("Referer") != "" || r.Header.Get("Sec-Fetch-Site") != "" {
		return false
	}
	// A direct local peer only (#3585): a request a local proxy forwarded is not the command line
	// on this computer. The require_auth_on_loopback clause is intentionally NOT applied here --
	// the signed handshakes signature is the credential, so the setting is already satisfied and
	// must not block `coral cloud connect`.
	if !auth.IsDirectLocalPeer(r) {
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

// refuseSystemSetting is refuseCloud's dashboard-only gate for Coral's own system settings
// (hub mode, restart, #3460): the same checks, with answers that point at this computer's
// dashboard rather than at `coral cloud connect`. With strict local auth an agent token is
// already refused by the identity observer (operatorOnlyRoutes).
func (h *CloudHandler) refuseSystemSetting(w http.ResponseWriter, r *http.Request) bool {
	const where = "This is set in the Coral dashboard on the computer running Coral"
	if cloud.Tunneled(r) {
		errForbidden(w, where+", not through Coral Hub")
		return true
	}
	if same, supported := h.sameUser(r); supported && !same {
		writeError(w, http.StatusForbidden, "other_user", where+", as the user Coral runs as")
		return true
	}
	if r.Header.Get(cloud.CLIAuthHeader) == "" && localDashboard(r) {
		return false
	}
	writeError(w, http.StatusForbidden, "local_dashboard_required", where+": open Settings there")
	return true
}

// HubModeSetting is Settings > Run as a hub (#3460): "true" starts this Coral as a
// multi-server hub, like --hub. It applies at the next start.
const HubModeSetting = "hub_mode"

// HubMode: GET/PUT /api/system/hub-mode, on this computer only, from the dashboard
// (never through Coral Hub, never another local user). {"enabled": bool} saves the
// setting; the answer is {saved, running, forced, restart_required, can_restart}.
func (h *CloudHandler) HubMode(w http.ResponseWriter, r *http.Request) {
	if h.refuseSystemSetting(w, r) {
		return
	}
	if r.Method == http.MethodPut {
		if !requireJSON(w, r) {
			return
		}
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if err := decodeJSON(r, &body); err != nil || body.Enabled == nil {
			errBadRequest(w, "enabled (true or false) is required")
			return
		}
		if err := h.ss.SetSetting(r.Context(), HubModeSetting, strconv.FormatBool(*body.Enabled)); err != nil {
			errInternalServer(w, err.Error())
			return
		}
		slog.Info("[hub] 'Run as a hub' changed on this computer; it applies at the next start", "enabled", *body.Enabled)
	}
	s, _ := h.ss.GetSettings(r.Context())
	saved := setreg.Bool(s, HubModeSetting)
	want := saved || h.HubModeForced
	writeJSON(w, http.StatusOK, map[string]any{
		"saved": saved, "running": h.HubModeRunning, "forced": h.HubModeForced,
		"restart_required": want != h.HubModeRunning, "can_restart": h.Restart != nil,
	})
}

// RestartServer: POST /api/system/restart, on this computer only, from the dashboard,
// and only where this process can restart itself (the desktop apps); 409 elsewhere.
func (h *CloudHandler) RestartServer(w http.ResponseWriter, r *http.Request) {
	if h.refuseSystemSetting(w, r) {
		return
	}
	if !requireJSON(w, r) {
		return
	}
	if !humanInputCaller(r) {
		writeError( // a person only: never an agent with forged browser headers (#3634)
			w, http.StatusForbidden, "operator_only", "only the Operator can restart Coral")
		return
	}
	if h.Restart == nil {
		writeError(w, http.StatusConflict, "restart_unavailable", "This Coral can't restart itself: quit it and start it again")
		return
	}
	if err := h.Restart(); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	slog.Info("[system] restarting at the dashboard's request")
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}
