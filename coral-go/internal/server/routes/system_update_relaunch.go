// Coral Hub's half of the auto-update relaunch (#5280, section 13 of the auto-update design): GET /api/system/update and
// POST /api/system/update/relaunch.
//
// The hub can only trigger, never choose: a relaunch runs the updater's fixed apply, which re-verifies what it staged. Nothing the hub
// sends selects a version, a channel, a path, a URL or a hash. The routes answer 404 until the feature is switched on.

package routes

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/subgentic/coral/internal/httputil"
	setreg "github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/internal/updatecheck"
	"github.com/subgentic/coral/pkg/tunnel"
)

// The fixed vocabularies the hub maps (section 13): the machine emits only these.
const (
	updNone     = "none"
	updStaged   = "staged"
	updApplying = "applying"
	updFailed   = "failed"

	reasonOwnerOnly   = "owner_only"
	reasonMaxMode     = "max_mode"
	reasonSwitchOff   = "switch_off"
	reasonNothing     = "nothing_staged"
	reasonNotVerified = "not_verified"
	reasonBusy        = "busy"
	reasonTooSoon     = "too_soon"
	reasonAlready     = "already_relaunched"
	reasonUnsupported = "unsupported_install"
	reasonApplying    = "applying"
	// reasonLocalApproval: on Windows the install needs a person at this computer to allow Windows Installer (UAC), so Coral Hub can neither start it nor leave
	// Coral stopped behind a prompt nobody sees: only the local "Install update" (system_update_install.go) starts it (#166).
	reasonLocalApproval = "local_approval_needed"
	// reasonNoRunRecord: Coral could not be started again after the update ended it (no record of how it was started, and no service manager), so no relaunch is
	// offered or started: it would end Coral and not bring it back (#5419).
	reasonNoRunRecord = "no_run_record"

	// install_kind: how this Coral was installed, as the updater found it (#5388).
	installApp          = "app"
	installMSI          = "msi"
	installPortable     = "portable"
	installTarball      = "tarball"
	installHomebrew     = "homebrew"
	installUnwritable   = "unwritable"
	installUnregistered = "unregistered"

	// upgradeCommandHomebrew is what a Homebrew install is told to run: fixed text, never from the updater's record.
	upgradeCommandHomebrew = "brew upgrade --cask coral"

	resultRelaunched = "relaunched"
	resultRefused    = "refused"
	resultRolledBack = "failed_rolled_back"
)

const (
	ackTTL            = 60 * time.Second
	relaunchSpacing   = 10 * time.Minute
	relaunchBodyMax   = 1 << 10
	maxAgentNameRunes = 64
	maxRestartRows    = 200
	updatesDirName    = "updates"
	ackKeyFile        = "ack.key"
	ackUsedFile       = "ack_used.json"
)

// relaunchNeedsLocalApproval is true where installing an update needs a person at the computer (Windows). A variable so a test can stand for Windows.
var relaunchNeedsLocalApproval = runtime.GOOS == "windows"

var (
	updateVersionRe = regexp.MustCompile(`^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,4}$`)
	hubByRe         = regexp.MustCompile(`^(local|hub:hub-user-[0-9a-f]{8})$`)
)

// StagedUpdate is what the updater staged and verified.
type StagedUpdate struct {
	Version  string
	StagedAt time.Time
	Verified bool
}

// UpdateTrust is what the staged version was verified against, for display.
type UpdateTrust struct {
	Issuer, Signer       string
	ManifestSequence     int64
	PromotedAt, ExpireAt time.Time
}

// installKinds, notReadyReasons and rollbackReasons are the vocabularies the status sends for the #5388 fields: anything else is not sent (or, for a roll-back's reason, is "other").
var (
	installKinds     = map[string]bool{installApp: true, installMSI: true, installPortable: true, installTarball: true, installHomebrew: true, installUnwritable: true, installUnregistered: true}
	notReadyReasons  = map[string]bool{"not_checked": true, "up_to_date": true, "not_promoted": true, "backoff": true, "check_failed": true, "unsupported_install": true, "switch_off": true, "applying": true}
	rollbackReasons  = map[string]bool{"did_not_start": true, "not_healthy": true, "swap_failed": true, "verify_failed": true}
	rollbackOtherTag = "other"
)

// UpdateRollback is the last apply that was rolled back: the previous version was put back.
type UpdateRollback struct {
	At       time.Time
	From, To string
	Reason   string // one of rollbackReasons, else shown as "other"
	// RestartFailed: the previous Coral could not be started again after the roll-back, so nothing was running (#5419).
	RestartFailed bool
}

// UpdateResult is the last relaunch's outcome.
type UpdateResult struct {
	At       time.Time
	From, To string
	Result   string // resultRelaunched, resultRefused or resultRolledBack
	By       string // "local" or "hub:<pseudonym>"
}

// UpdaterRecord is the updater's own record, read fresh for every request.
type UpdaterRecord struct {
	Enabled   bool
	Installed string
	State     string // none, staged, applying, failed
	Staged    *StagedUpdate
	Trust     UpdateTrust
	// Supported is false for an install the updater can't update (Homebrew, a package manager, not writable).
	Supported bool
	// TmuxAgentsRestart: the bundled tmux changed, so tmux agents restart too.
	TmuxAgentsRestart bool
	// HubRelaunched is the staged version a hub relaunch was already started for; LastHubRelaunch is when.
	HubRelaunched   string
	LastHubRelaunch time.Time
	LastResult      *UpdateResult
	// The fields of the #5388 status: when the last check ran (a failed one counts), how Coral was installed, why nothing is staged
	// (one of notReadyReasons) with the newer version when one is known, and the last roll-back. Zero or empty: not sent.
	CheckedAt    time.Time
	InstallKind  string
	NotReady     string
	Latest       string
	LastRollback *UpdateRollback
}

// RestartAgent is one pty agent a relaunch would restart.
type RestartAgent struct {
	SessionID, Name, Backend, State string
}

// RelaunchAudit is one attempt, allowed or refused: no email or name, the hub user's pseudonym, ids and a count.
type RelaunchAudit struct {
	At                time.Time
	Installed, Staged string
	AgentIDs          []string
	By                string
	Code              string // the refusal code, or "started"
}

// RelaunchUpdater is what the relaunch routes need from the updater (the updater implements it; tests stub it). It owns state.json:
// the routes read it through Record and ask it to change it.
type RelaunchUpdater interface {
	Record(ctx context.Context) (UpdaterRecord, error)
	// Apply starts the fixed "apply" for a hub relaunch by the pseudonym by, and records hub_relaunched. The updater re-verifies.
	Apply(ctx context.Context, by string) (operation string, err error)
	// Audit appends an attempt to the history (capped at 50 there).
	Audit(ctx context.Context, a RelaunchAudit) error
	// Check runs one check ("coral-update check": find the newest promoted release, verify it, stage it) and waits for it. The result is the updater's own word for how it ended
	// ("" when it could not say); an error is a check that could not run at all. It never installs.
	Check(ctx context.Context) (result string, err error)
}

// UpdateRelaunchHandler serves the two routes.
type UpdateRelaunchHandler struct {
	// Enabled is the off flag: false answers both routes 404.
	Enabled func() bool
	Updater RelaunchUpdater
	// Settings reads the settings store (the mode cap, the switch, the Operator opt-out) on every request.
	Settings func(ctx context.Context) (map[string]string, error)
	// OwnerUser is the owner's hub-user label recorded at enrolment; "" when none was.
	OwnerUser func(ctx context.Context) (string, error)
	// Agents lists the pty agents a relaunch would restart.
	Agents func(ctx context.Context) ([]RestartAgent, error)
	// CanRestart says whether Coral can be started again once it has ended (the server's own record of how it was started, or a service manager that restarts it);
	// nil is yes. A relaunch is not offered, and the route answers 409, while it says no (#5419).
	CanRestart func() bool
	// Dir is <data dir>/updates.
	Dir string
	Now func() time.Time
	// Feeds names the release feeds the channel facts (approved, past_approved, waiting) are read from; nil adds the channel only and asks nothing
	// (#5557). The server sets releaseFeeds; a test that does not mean to reach a feed leaves it nil, so it never reaches GitHub.
	Feeds func() updatecheck.Feeds

	// SetSetting writes a setting (the update_auto switch's own route uses it).
	SetSetting func(ctx context.Context, key, value string) error

	// checking is true while a check runs (a person's "Check now" and the background job share it: one at a time); lastManual is when a person's check was last started.
	checking   atomic.Bool
	checkMu    sync.Mutex
	lastManual time.Time
	// checkTimes are the background job's timing (the defaults unless a test says otherwise).
	checkTimes checkTimes

	mu            sync.Mutex
	noLabelLogged sync.Once
	// keyMu makes the first creation of the acknowledgement key one creation: two first requests at once (the status takes no other lock)
	// would each make a key and the later rename would leave tokens made with the first unverifiable (#5290).
	keyMu sync.Mutex
	// startedVersion and startedAt are this process's own memory of the last relaunch it asked the updater to start: the once-per-version
	// and spacing rules hold even when the updater's record has not caught up yet (#5290).
	startedVersion string
	startedAt      time.Time
}

func (h *UpdateRelaunchHandler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *UpdateRelaunchHandler) on() bool {
	return h.Enabled != nil && h.Enabled() && h.Updater != nil
}

// identify runs the stamp and owner checks (identifyOwner with this handler's own label source).
func (h *UpdateRelaunchHandler) identify(r *http.Request) callerIdentity {
	return ownerIdentityWith(r, h.OwnerUser, &h.noLabelLogged)
}

// gate is the status of the checks that don't depend on the update's state, as the reason the relaunch is unavailable ("" = pass).
func (h *UpdateRelaunchHandler) gate(ctx context.Context, id callerIdentity) (reason string, code string) {
	if id.local {
		return "", "" // the owner at this computer: the Hub's mode cap, Operator opt-out and switch are about what Coral Hub may do, not about the person here
	}
	if !id.owner {
		return reasonOwnerOnly, "owner_only"
	}
	s, err := h.Settings(ctx)
	if err != nil {
		return reasonSwitchOff, "hub_relaunch_off" // fail closed
	}
	if HubOperatorDisabled(s) {
		return reasonSwitchOff, "hub_relaunch_off"
	}
	if mode, ok := tunnel.ParseMode(setreg.Raw(s, MaxModeSetting)); !ok || mode < tunnel.TierLaunch {
		return reasonMaxMode, "max_mode"
	}
	if !HubRelaunchAllowed(s) {
		return reasonSwitchOff, "hub_relaunch_off"
	}
	return "", ""
}

// -- state files --

func (h *UpdateRelaunchHandler) ensureDir() error { return updatesDir{h.Dir}.ensure() }

// readPrivate reads a state file that must be a plain file only this user can read. A missing file is (nil, ErrNotExist).
func (h *UpdateRelaunchHandler) readPrivate(name string, max int64) ([]byte, error) {
	return updatesDir{h.Dir}.read(name, max)
}

// writePrivate replaces a state file atomically: a temp file made 0600, written, synced, renamed.
func (h *UpdateRelaunchHandler) writePrivate(name string, data []byte) error {
	return updatesDir{h.Dir}.write(name, data)
}

// ackKey is the per-install HMAC key (32 random bytes, created on first use). An unreadable or odd key is an error, never a new key:
// replacing it would let a swapped file mint tokens.
func (h *UpdateRelaunchHandler) ackKey() ([]byte, error) {
	h.keyMu.Lock()
	defer h.keyMu.Unlock()
	if err := h.ensureDir(); err != nil {
		return nil, err
	}
	b, err := h.readPrivate(ackKeyFile, 256)
	if errors.Is(err, os.ErrNotExist) {
		k := make([]byte, 32)
		if _, err := rand.Read(k); err != nil {
			return nil, err
		}
		if err := h.writePrivate(ackKeyFile, []byte(hex.EncodeToString(k))); err != nil {
			return nil, err
		}
		return k, nil
	}
	if err != nil {
		return nil, err
	}
	k, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(k) != 32 {
		return nil, errors.New("the acknowledgement key is damaged")
	}
	return k, nil
}

// usedAck is a token that was used: until it would have expired.
type usedAck struct {
	Exp       int64  `json:"exp"`
	TokenHash string `json:"token"`
	Operation string `json:"operation"`
}

func (h *UpdateRelaunchHandler) readUsed() (map[string]usedAck, error) {
	b, err := h.readPrivate(ackUsedFile, 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]usedAck{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]usedAck{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, errors.New("the used-token record is damaged")
	}
	return m, nil
}

func (h *UpdateRelaunchHandler) writeUsed(m map[string]usedAck) error {
	now := h.now().Unix()
	for k, v := range m {
		if v.Exp < now {
			delete(m, k)
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return h.writePrivate(ackUsedFile, b)
}

// -- the bound token --

// ackDigest is what the token is bound to besides who asked: the staged version, the pty agents (id and state, sorted) and whether
// tmux agents restart too.
func ackDigest(version string, agents []RestartAgent, tmux bool) string {
	rows := make([]string, 0, len(agents))
	for _, a := range agents {
		rows = append(rows, a.SessionID+"="+a.State)
	}
	sort.Strings(rows)
	sum := sha256.Sum256([]byte(version + "\n" + strings.Join(rows, "\n") + "\n" + strconv.FormatBool(tmux)))
	return hex.EncodeToString(sum[:])
}

type ackPayload struct {
	Nonce  string `json:"n"`
	Exp    int64  `json:"e"`
	Digest string `json:"d"`
}

// ackMAC covers the payload and the identity the token was issued to: the owner marker and the label are inside it.
func ackMAC(key []byte, p ackPayload, label string) []byte {
	m := hmac.New(sha256.New, key)
	fmt.Fprintf(m, "coral-relaunch-ack-1\nrole=owner\nuser=%s\nnonce=%s\nexp=%d\ndigest=%s", label, p.Nonce, p.Exp, p.Digest)
	return m.Sum(nil)
}

func (h *UpdateRelaunchHandler) issue(key []byte, id callerIdentity, digest string) (string, error) {
	n := make([]byte, 16)
	if _, err := rand.Read(n); err != nil {
		return "", err
	}
	p := ackPayload{Nonce: hex.EncodeToString(n), Exp: h.now().Add(ackTTL).Unix(), Digest: digest}
	pj, _ := json.Marshal(p)
	return base64.RawURLEncoding.EncodeToString(pj) + "." + base64.RawURLEncoding.EncodeToString(ackMAC(key, p, id.pseudo)), nil
}

// parseAck verifies the token's MAC for the identity; it says nothing about the digest or the expiry.
func parseAck(key []byte, id callerIdentity, tok string) (ackPayload, bool) {
	var p ackPayload
	pb, mb, ok := strings.Cut(tok, ".")
	if !ok || len(tok) > 512 {
		return p, false
	}
	pj, err1 := base64.RawURLEncoding.DecodeString(pb)
	mac, err2 := base64.RawURLEncoding.DecodeString(mb)
	if err1 != nil || err2 != nil || json.Unmarshal(pj, &p) != nil || p.Nonce == "" || len(p.Nonce) > 64 {
		return p, false
	}
	return p, hmac.Equal(mac, ackMAC(key, p, id.pseudo))
}

// -- the view --

func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029 {
			return -1
		}
		return r
	}, s)
	if r := []rune(s); len(r) > maxAgentNameRunes {
		s = string(r[:maxAgentNameRunes])
	}
	return s
}

func cleanState(s string) string {
	switch s {
	case "working", "idle", "waiting":
		return s
	}
	return "working"
}

// printable keeps display text of at most max runes with no control or format characters.
func printable(s string, max int) string {
	return func() string {
		out := strings.Map(func(r rune) rune {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				return -1
			}
			return r
		}, s)
		if r := []rune(out); len(r) > max {
			out = string(r[:max])
		}
		return out
	}()
}

// relaunchView is the status with the reason a relaunch is unavailable ("" when it is available), computed in the order of the checks.
type relaunchView struct {
	reason  string
	code    string // the refusal code of the POST for the same reason
	rec     UpdaterRecord
	agents  []RestartAgent
	digest  string
	version string // the staged version when it has the pattern
}

func (h *UpdateRelaunchHandler) view(ctx context.Context, id callerIdentity) (relaunchView, error) {
	var v relaunchView
	rec, err := h.Updater.Record(ctx)
	if err != nil {
		return v, err
	}
	v.rec = rec
	if rec.Staged != nil && updateVersionRe.MatchString(rec.Staged.Version) {
		v.version = rec.Staged.Version
	}
	if h.Agents != nil {
		if v.agents, err = h.Agents(ctx); err != nil {
			return v, err
		}
		if len(v.agents) > maxRestartRows {
			v.agents = v.agents[:maxRestartRows]
		}
	}
	v.digest = ackDigest(v.version, v.agents, rec.TmuxAgentsRestart)
	switch reason, code := h.gate(ctx, id); {
	case reason != "":
		v.reason, v.code = reason, code
	case relaunchNeedsLocalApproval:
		v.reason, v.code = reasonLocalApproval, reasonLocalApproval
	case rec.State == updApplying:
		v.reason, v.code = reasonApplying, reasonBusy
	case !rec.Supported:
		v.reason, v.code = reasonUnsupported, reasonNothing
	case h.CanRestart != nil && !h.CanRestart():
		v.reason, v.code = reasonNoRunRecord, reasonNoRunRecord
	case rec.Staged == nil || v.version == "":
		v.reason, v.code = reasonNothing, reasonNothing
	case !rec.Staged.Verified:
		v.reason, v.code = reasonNotVerified, reasonNotVerified
	case rec.HubRelaunched == v.version:
		v.reason, v.code = reasonAlready, reasonAlready
	case !rec.LastHubRelaunch.IsZero() && h.now().Sub(rec.LastHubRelaunch) < relaunchSpacing:
		v.reason, v.code = reasonTooSoon, reasonTooSoon
	}
	return v, nil
}

func (h *UpdateRelaunchHandler) relaunchJSON(key []byte, id callerIdentity, v relaunchView) (map[string]any, error) {
	restarts := make([]map[string]any, 0, len(v.agents))
	for _, a := range v.agents {
		restarts = append(restarts, map[string]any{"session_id": printable(a.SessionID, 64), "name": cleanName(a.Name), "backend": "pty", "state": cleanState(a.State)})
	}
	out := map[string]any{
		"available": v.reason == "", "reason": v.reason, "hub_allowed": v.reason != reasonSwitchOff && v.reason != reasonMaxMode,
		"restarts": restarts, "tmux_agents_restart": v.rec.TmuxAgentsRestart,
	}
	if v.reason == "" {
		tok, err := h.issue(key, id, v.digest)
		if err != nil {
			return nil, err
		}
		out["ack"], out["ack_expires_in"] = tok, int(ackTTL/time.Second)
	}
	return out, nil
}

func (h *UpdateRelaunchHandler) statusJSON(id callerIdentity, v relaunchView, key []byte) (map[string]any, error) {
	rec := v.rec
	state := rec.State
	switch state {
	case updNone, updStaged, updApplying, updFailed:
	default:
		state = updFailed
	}
	out := map[string]any{"enabled": rec.Enabled}
	if updateVersionRe.MatchString(rec.Installed) {
		out["installed"] = rec.Installed
	} else {
		out["installed"] = nil
		state = updFailed
	}
	if rec.Staged != nil {
		if v.version != "" {
			days := int(h.now().Sub(rec.Staged.StagedAt).Hours() / 24)
			if days < 0 {
				days = 0
			}
			out["staged"] = map[string]any{"version": v.version, "staged_at": rec.Staged.StagedAt.UTC().Format(time.RFC3339), "days_ready": days, "verified": rec.Staged.Verified}
		} else {
			state = updFailed
		}
	}
	out["state"] = state
	if rec.Staged != nil && v.version != "" {
		out["trust"] = map[string]any{
			"issuer": printable(rec.Trust.Issuer, 200), "signer": printable(rec.Trust.Signer, 300),
			"manifest_sequence": rec.Trust.ManifestSequence,
			"promoted_at":       rec.Trust.PromotedAt.UTC().Format(time.RFC3339), "expires_at": rec.Trust.ExpireAt.UTC().Format(time.RFC3339),
		}
	}
	rj, err := h.relaunchJSON(key, id, v)
	if err != nil {
		return nil, err
	}
	out["relaunch"] = rj
	if lr := rec.LastResult; lr != nil && hubByRe.MatchString(lr.By) && updateVersionRe.MatchString(lr.From) && updateVersionRe.MatchString(lr.To) &&
		(lr.Result == resultRelaunched || lr.Result == resultRefused || lr.Result == resultRolledBack) {
		out["last_result"] = map[string]any{"at": lr.At.UTC().Format(time.RFC3339), "from": lr.From, "to": lr.To, "result": lr.Result, "by": lr.By}
	}
	h.addUpdateFacts(out, rec)
	return out, nil
}

// addUpdateFacts adds the fields the screens and the docs need beyond the design's (#5388), each only when known and only in its fixed vocabulary:
// no free text, path or URL from the updater's record reaches the reply.
func (h *UpdateRelaunchHandler) addUpdateFacts(out map[string]any, rec UpdaterRecord) {
	if !rec.CheckedAt.IsZero() {
		out["checked_at"] = rec.CheckedAt.UTC().Format(time.RFC3339)
	}
	if installKinds[rec.InstallKind] {
		out["install_kind"] = rec.InstallKind
		if rec.InstallKind == installHomebrew {
			out["upgrade_command"] = upgradeCommandHomebrew
		}
	}
	if rec.Staged == nil && notReadyReasons[rec.NotReady] {
		out["not_ready_reason"] = rec.NotReady
		if updateVersionRe.MatchString(rec.Latest) {
			out["latest"] = rec.Latest
		}
	}
	if rb := rec.LastRollback; rb != nil && !rb.At.IsZero() && updateVersionRe.MatchString(rb.From) && updateVersionRe.MatchString(rb.To) {
		reason := rollbackOtherTag
		if rollbackReasons[rb.Reason] {
			reason = rb.Reason
		}
		lr := map[string]any{"at": rb.At.UTC().Format(time.RFC3339), "from": rb.From, "to": rb.To, "reason": reason}
		if rb.RestartFailed {
			lr["restart_failed"] = true // present only when true: Coral was stopped by an update that could not start it again
		}
		out["last_rollback"] = lr
	}
}

// Status: GET /api/system/update.
func (h *UpdateRelaunchHandler) Status(w http.ResponseWriter, r *http.Request) {
	if !h.on() {
		errNotFound(w, "not found")
		return
	}
	id := h.identify(r)
	if !viaTunnel(r) {
		id = localIdentity // the local dashboard gets the acknowledgement for its own relaunch (RelaunchLocal)
	}
	v, err := h.view(r.Context(), id)
	if err != nil {
		slog.Warn("[update] status unavailable", "error", err)
		errInternalServer(w, "update status is unavailable")
		return
	}
	var key []byte
	if v.reason == "" { // a token only for a request that may use it
		if key, err = h.ackKey(); err != nil {
			slog.Warn("[update] acknowledgement key unavailable", "error", err)
			errInternalServer(w, "update status is unavailable")
			return
		}
	}
	out, err := h.statusJSON(id, v, key)
	if err != nil {
		errInternalServer(w, "update status is unavailable")
		return
	}
	h.addChannelFacts(r, out)
	if !viaTunnel(r) { // the local dashboard's "check automatically" switch; the Hub's shape is unchanged
		if settings, err := h.Settings(r.Context()); err == nil {
			out["auto"] = UpdateAutoOn(settings)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// -- the relaunch --

func (h *UpdateRelaunchHandler) refuse(w http.ResponseWriter, r *http.Request, id callerIdentity, v *relaunchView, status int, code, msg string, extra map[string]any) {
	body := map[string]any{"code": code, "error": msg}
	for k, e := range extra {
		body[k] = e
	}
	h.audit(r.Context(), id, v, code)
	writeJSON(w, status, body)
}

func (h *UpdateRelaunchHandler) audit(ctx context.Context, id callerIdentity, v *relaunchView, code string) {
	a := RelaunchAudit{At: h.now().UTC(), By: id.pseudo, Code: code}
	if v != nil {
		a.Installed = v.rec.Installed
		a.Staged = v.version
		for _, ag := range v.agents {
			a.AgentIDs = append(a.AgentIDs, ag.SessionID)
		}
	}
	slog.Info("[update] hub relaunch attempt", "result", code, "user", id.pseudo, "installed", a.Installed, "staged", a.Staged, "agents", len(a.AgentIDs))
	if err := h.Updater.Audit(ctx, a); err != nil {
		slog.Warn("[update] could not record the attempt", "error", err)
	}
}

// The field is called "ack", not "ack_token": the redactors (the hub's, and the base one for some values) take any value under a
// key named for a token as a secret and replace it, which would send the hub "[redacted token]" and make every relaunch stale (#5290).
type relaunchBody struct {
	AckToken string `json:"ack"`
}

// readRelaunchBody is the strict body: one object of exactly ack, at most 1 KB.
func readRelaunchBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, relaunchBodyMax))
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return "", false
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var body relaunchBody
	if err := dec.Decode(&body); err != nil || dec.More() {
		return "", false
	}
	if _, err := dec.Token(); err != io.EOF || body.AckToken == "" {
		return "", false
	}
	return body.AckToken, true
}

// Relaunch: POST /api/system/update/relaunch.
func (h *UpdateRelaunchHandler) Relaunch(w http.ResponseWriter, r *http.Request) {
	if !h.on() {
		errNotFound(w, "not found")
		return
	}
	// 1 and 2: the stamp is authentic and the caller is the owner. (A local caller is not: the local control relaunches locally.)
	id := h.identify(r)
	if !id.owner {
		h.refuse(w, r, id, nil, http.StatusForbidden, "owner_only", "only the account this computer is linked to can relaunch it from Coral Hub", nil)
		return
	}
	// 3 and 4, read now: the cap, the Operator opt-out and the switch.
	if reason, code := h.gate(r.Context(), id); reason != "" {
		h.refuse(w, r, id, nil, http.StatusForbidden, code, "this computer does not allow Coral Hub to relaunch it for an update", nil)
		return
	}
	// Windows: the install asks the person at this computer (UAC); a relaunch from Coral Hub is refused before anything is read or started (#166).
	if relaunchNeedsLocalApproval {
		h.refuse(w, r, id, nil, http.StatusConflict, reasonLocalApproval, "installing an update on Windows needs someone at this computer to approve it: use Install update in Settings there", nil)
		return
	}
	h.relaunch(w, r, id)
}

// localIdentity is the owner at this computer's own dashboard or command line.
var localIdentity = callerIdentity{owner: true, pseudo: "local", local: true}

// RelaunchLocal: POST /api/system/update/relaunch-local. The same relaunch for the person at this computer, with the same acknowledgement token (issued by the local GET
// /api/system/update, bound to "local" so it is no good on the Hub's route and a Hub token is no good here). Local only: through Coral Hub it is refused, and an agent's token is
// refused before it gets here.
func (h *UpdateRelaunchHandler) RelaunchLocal(w http.ResponseWriter, r *http.Request) {
	if !h.on() {
		errNotFound(w, "not found")
		return
	}
	if relaunchNeedsLocalApproval { // Windows installs through its own local route (POST /api/system/update/install, #166)
		errNotFound(w, "not found")
		return
	}
	if !h.localOnly(w, r) {
		return
	}
	// The yes is a person's: an agent's session token (or anything that is not the dashboard or the signed command line) never installs, whatever else the request carries (#6290).
	if !humanInputCaller(r) {
		writeError(w, http.StatusForbidden, "operator_only", "only the Operator can install an update")
		return
	}
	h.relaunch(w, r, localIdentity)
}

// relaunch is the rest of a relaunch once the caller is known and, for the Hub, has passed the owner and gate checks.
func (h *UpdateRelaunchHandler) relaunch(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	// 5: the strict body.
	tok, ok := readRelaunchBody(w, r)
	if !ok {
		h.audit(r.Context(), id, nil, "bad_request")
		errBadRequest(w, `the body is exactly {"ack": "<token from the update status>"}`)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	key, err := h.ackKey()
	if err != nil {
		slog.Warn("[update] acknowledgement key unavailable", "error", err)
		errInternalServer(w, "the relaunch is unavailable")
		return
	}
	used, err := h.readUsed()
	if err != nil {
		slog.Warn("[update] used-token record unavailable", "error", err)
		errInternalServer(w, "the relaunch is unavailable")
		return
	}
	tokHash := sha256.Sum256([]byte(tok))
	p, macOK := parseAck(key, id, tok)
	// A token that was used and is sent again (a retry, a double click, a hub that did not see the reply) starts nothing.
	if macOK {
		if u, seen := used[p.Nonce]; seen && u.TokenHash == hex.EncodeToString(tokHash[:]) && u.Exp >= h.now().Unix() {
			h.audit(r.Context(), id, nil, "repeat")
			writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "operation": u.Operation})
			return
		}
	}
	v, err := h.view(r.Context(), id)
	if err != nil {
		slog.Warn("[update] status unavailable", "error", err)
		errInternalServer(w, "the relaunch is unavailable")
		return
	}
	// 6 to 8, in the status' own order; the token is the last of them that needs the list.
	switch v.reason {
	case "":
	case reasonApplying:
		h.refuse(w, r, id, &v, http.StatusConflict, reasonBusy, "an update is being applied", nil)
		return
	case reasonTooSoon:
		// A per-action limit: marked, so the dashboard does not take it for an overloaded server.
		h.audit(r.Context(), id, &v, reasonTooSoon)
		wait := int((relaunchSpacing - h.now().Sub(v.rec.LastHubRelaunch)) / time.Second)
		httputil.WriteActionLimit(w, max(wait, 1), reasonTooSoon, "a relaunch was started a moment ago")
		return
	default:
		h.refuse(w, r, id, &v, http.StatusConflict, v.code, "there is no verified update this computer can relaunch into", nil)
		return
	}
	// 7: the token verifies, is unexpired, unused, and matches what the machine sees now.
	_, spent := used[p.Nonce]
	if !macOK || spent || p.Exp < h.now().Unix() || p.Digest != v.digest {
		fresh, err := h.relaunchJSON(key, id, v)
		if err != nil {
			errInternalServer(w, "the relaunch is unavailable")
			return
		}
		h.refuse(w, r, id, &v, http.StatusConflict, "stale_ack", "what a relaunch would restart changed: check the list and confirm again", map[string]any{"relaunch": fresh})
		return
	}
	// An attempt this process started that has since ended without an update (the updater recorded a result for it and nothing is running) is over: its version may be tried again,
	// after the spacing below.
	if h.startedVersion != "" && v.rec.State != updApplying && v.rec.LastResult != nil && v.rec.LastResult.At.After(h.startedAt) {
		h.startedVersion = ""
	}
	// The same two rules as the status', from this process's own memory: whatever the updater's record says, a version already started
	// is not started twice and a second start inside the spacing is refused.
	if h.startedVersion != "" && h.startedVersion == v.version {
		h.refuse(w, r, id, &v, http.StatusConflict, reasonAlready, "a relaunch into this version was already started", nil)
		return
	}
	if !h.startedAt.IsZero() && h.now().Sub(h.startedAt) < relaunchSpacing {
		h.audit(r.Context(), id, &v, reasonTooSoon)
		httputil.WriteActionLimit(w, max(int((relaunchSpacing-h.now().Sub(h.startedAt))/time.Second), 1), reasonTooSoon, "a relaunch was started a moment ago")
		return
	}
	op, err := h.Updater.Apply(r.Context(), id.pseudo)
	if err != nil {
		slog.Warn("[update] the updater did not start", "error", err)
		h.audit(r.Context(), id, &v, "failed_to_start")
		errInternalServer(w, "the update could not be started")
		return
	}
	h.startedVersion, h.startedAt = v.version, h.now()
	used[p.Nonce] = usedAck{Exp: p.Exp, TokenHash: hex.EncodeToString(tokHash[:]), Operation: printable(op, 64)}
	if err := h.writeUsed(used); err != nil {
		// The updater already runs, and records that a hub relaunch started for this version (one per version).
		slog.Warn("[update] could not record the used token", "error", err)
	}
	h.audit(r.Context(), id, &v, "started")
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "operation": used[p.Nonce].Operation})
}
