package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cdknorow/coral/internal/agentmodels"
	"github.com/cdknorow/coral/internal/agentprio"
	"github.com/cdknorow/coral/internal/background"
	"github.com/cdknorow/coral/internal/safego"
	"github.com/cdknorow/coral/internal/updatecheck"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	qrcode "github.com/skip2/go-qrcode"
	"gopkg.in/yaml.v3"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/auth"
	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/dbcrypt"
	"github.com/cdknorow/coral/internal/executil"
	"github.com/cdknorow/coral/internal/inputlog"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/safefs"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/tmux"
	"github.com/cdknorow/coral/internal/tracking"
)

// Indexer is the interface for triggering an index refresh.
type Indexer interface {
	RunOnce(ctx context.Context) error
}

// SystemHandler handles settings, tags, and filesystem endpoints.
type SystemHandler struct {
	// models lists each agent CLI's models (#4065).
	models  *agentmodels.Service
	db      *store.DB
	ss      *store.SessionStore
	cfg     *config.Config
	indexer Indexer

	generateJobs   map[string]*generateJob
	generateJobsMu sync.Mutex

	// terminal identifies the terminal backend this server actually runs, so
	// status can report the effective backend (set by the server wiring).
	terminal ptymanager.SessionTerminal

	// tmux discovery can start a login shell when tmux is not on PATH, so
	// routine status polls reuse a recent result; an explicit re-check bypasses
	// the cache. tmuxCacheTTL 0 means the default.
	tmuxMu       sync.Mutex
	tmuxAt       time.Time
	tmuxPath     string
	tmuxFound    bool
	tmuxCacheTTL time.Duration
}

const defaultTmuxCacheTTL = 30 * time.Second

// wantsFreshStatus reports an explicit re-check: ?recheck=1, or a request that
// asks to bypass caches (fetch cache:"no-store" sends Cache-Control: no-cache).
func wantsFreshStatus(r *http.Request) bool {
	if v := r.URL.Query().Get("recheck"); v == "1" || v == "true" {
		return true
	}
	cc := strings.ToLower(r.Header.Get("Cache-Control"))
	return strings.Contains(cc, "no-cache") || strings.Contains(cc, "no-store")
}

// discoverTmux finds tmux for the status endpoint. This is discovery only (the
// file exists and is executable); it does not run tmux, so it says nothing
// about whether the executable works.
func (h *SystemHandler) discoverTmux(fresh bool) (string, bool) {
	ttl := h.tmuxCacheTTL
	if ttl == 0 {
		ttl = defaultTmuxCacheTTL
	}
	h.tmuxMu.Lock()
	defer h.tmuxMu.Unlock()
	if !fresh && !h.tmuxAt.IsZero() && time.Since(h.tmuxAt) < ttl {
		return h.tmuxPath, h.tmuxFound
	}
	h.tmuxPath, h.tmuxFound = tmuxProbe()
	h.tmuxAt = time.Now()
	return h.tmuxPath, h.tmuxFound
}

// SetTerminal tells the handler which terminal backend the server is running.
func (h *SystemHandler) SetTerminal(t ptymanager.SessionTerminal) { h.terminal = t }

// effectiveBackend reports the backend in use: "tmux", "pty", or "unknown".
func (h *SystemHandler) effectiveBackend() string { return TerminalKind(h.terminal) }

// TerminalKind is "tmux", "pty" or "unknown" for a session terminal.
func TerminalKind(t ptymanager.SessionTerminal) string {
	switch t.(type) {
	case *ptymanager.TmuxSessionTerminal:
		return "tmux"
	case *ptymanager.PTYSessionTerminal:
		return "pty"
	}
	return "unknown"
}

// NewSystemHandler creates a SystemHandler.
func NewSystemHandler(db *store.DB, cfg *config.Config) *SystemHandler {
	h := &SystemHandler{
		db:           db,
		ss:           store.NewSessionStore(db),
		cfg:          cfg,
		generateJobs: make(map[string]*generateJob),
	}
	// Cleanup expired generation jobs every minute
	go func() {
		defer safego.Recover("server/routes/system:127")
		for {
			time.Sleep(time.Minute)
			h.generateJobsMu.Lock()
			for id, job := range h.generateJobs {
				if time.Since(job.CreatedAt) > 10*time.Minute {
					delete(h.generateJobs, id)
				}
			}
			h.generateJobsMu.Unlock()
		}
	}()
	h.models = h.newAgentModels()
	return h
}

// SetIndexer injects the session indexer for manual refresh triggers.
func (h *SystemHandler) SetIndexer(idx Indexer) {
	h.indexer = idx
}

// ── Settings ────────────────────────────────────────────────────────────

// sensitiveKeys are settings that should never be returned to the frontend.
var sensitiveKeys = map[string]bool{}

// GetSettings returns all user settings, filtering out sensitive keys.
// GET /api/settings
func (h *SystemHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Reader().QueryxContext(r.Context(), "SELECT key, value FROM user_settings")
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err == nil {
			if !sensitiveKeys[k] {
				settings[k] = v
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": settings})
}

// PutSettings upserts one or more settings.
// PUT /api/settings
// hubTrustSettingRoutes are the Coral Hub link's trust settings and the routes that write them
// (all three are denied to a Coral Hub sign-in, and the dashboard writes them there).
var hubTrustSettingRoutes = map[string]string{
	MaxModeSetting:            "PUT /api/cloud/max-mode",
	AllowHubAutoAcceptSetting: "PUT /api/cloud/allow-auto-accept",
	RedactTokensSetting:       "PUT /api/cloud/redact-tokens",
}

// hubSigninSettingKey: a Sign in with Coral Hub setting (server.HubSigninEnabledSetting and its
// siblings, all named hub_signin_*), never written by PUT /api/settings.
func hubSigninSettingKey(k string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(k)), "hub_signin")
}

func (h *SystemHandler) PutSettings(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	// The terminal backend is written only by its own route, which needs a person (#3634).
	for k := range body {
		if isTerminalBackendKey(k) {
			errBadRequest(w, "terminal_backend is set with Settings > Terminal backend (PUT /api/system/terminal-backend)")
			return
		}
	}
	// cli_path_<type> (Settings > CLI Paths) is the program Coral runs for that agent
	// type: only a known type, and only an absolute path to an executable file with no
	// shell metacharacters (#3364). Checked before anything is saved; a value that is
	// unchanged is not re-checked, so a CLI that was uninstalled since doesn't block
	// saving other settings.
	// The Hub-as-Operator opt-out has its own route, which logs every change (#3377, #3404).
	for _, k := range []string{HubOperatorDisabledSetting, legacyHubOperatorSetting} {
		if _, ok := body[k]; ok {
			errBadRequest(w, k+" is set with PUT /api/system/hub-operator")
			return
		}
	}
	// Sign in with Coral Hub's settings have their own routes (#3966, Security condition 4):
	// only a local Operator may change them, and turning it off ends Hub sessions.
	for k := range body {
		if hubSigninSettingKey(k) {
			errBadRequest(w, k+" is set in Settings > Browser sign-in (PUT /api/auth/hub-signin or /api/auth/hub-origins)")
			return
		}
		// The Coral Hub link's trust settings have their own routes, which a Coral Hub sign-in
		// can't reach (#3966 round 1): never through the generic PUT, for any caller.
		if route, ok := hubTrustSettingRoutes[strings.ToLower(strings.TrimSpace(k))]; ok {
			errBadRequest(w, k+" is set with "+route)
			return
		}
	}
	// Run as a hub has its own route, on this computer only (#3460).
	if _, ok := body[HubModeSetting]; ok {
		errBadRequest(w, HubModeSetting+" is set with PUT /api/system/hub-mode")
		return
	}
	// The Coral Hub request pools' sizes (#3642): whole numbers in range, or empty for the default.
	if err := validateHubLimitSettings(body); err != nil {
		errBadRequest(w, err.Error())
		return
	}
	// The auto-wake setting and cap (#3636): a boolean, and a whole number from 1 to 50.
	if err := validateAutoWakeSettings(body); err != nil {
		errBadRequest(w, err.Error())
		return
	}
	// Resume agents stopped by an API error (#3613): a boolean.
	if err := validateAutoResumeSetting(body); err != nil {
		errBadRequest(w, err.Error())
		return
	}
	// agent_priority is "low" or "normal" (#3437); anything else was stored and silently
	// taken as low (#3470). Checked before anything is saved; the stored value is normalised.
	if v, ok := body[agentprio.SettingKey]; ok {
		s, isString := v.(string)
		lvl, valid := agentprio.Parse(s)
		if !isString || !valid {
			errBadRequest(w, agentprio.SettingKey+" must be \""+agentprio.Low+"\" or \""+agentprio.Normal+"\"")
			return
		}
		body[agentprio.SettingKey] = lvl
	}
	// Artifact retention (#3554): whole days from 0 (off) to 3650, and a list of kinds. Its
	// bookkeeping keys are Coral's own.
	for _, k := range []string{retentionEnabledAtKey, retentionSeenDaysKey, RetentionAckAtKey, RetentionLastKey} {
		if _, ok := body[k]; ok {
			errBadRequest(w, k+" is set by Coral (acknowledge a dry run with POST /api/artifacts/retention/ack)")
			return
		}
	}
	if v, ok := body[RetentionDaysSetting]; ok {
		s, valid := validRetentionDays(v)
		if !valid {
			errBadRequest(w, RetentionDaysSetting+" must be a whole number of days from 0 (off) to 3650")
			return
		}
		body[RetentionDaysSetting] = s
		// A changed value starts its wait now, so Settings can offer the review at once (an
		// acknowledgement has to come after this moment to count).
		if cur, _ := h.ss.GetSettings(r.Context()); cur == nil || cur[RetentionDaysSetting] != s {
			if s == "0" {
				body[retentionEnabledAtKey], body[retentionSeenDaysKey] = "", ""
			} else {
				body[retentionEnabledAtKey], body[retentionSeenDaysKey] = time.Now().UTC().Format(time.RFC3339), s
			}
		}
	}
	if v, ok := body[RetentionKindsSetting]; ok {
		s, valid := validRetentionKinds(v)
		if !valid {
			errBadRequest(w, RetentionKindsSetting+" must list diffs, objects, diff-objects or all")
			return
		}
		body[RetentionKindsSetting] = s
	}
	// require_auth_on_loopback (#3585): a boolean, normalised to "true"/"false". This route is
	// Operator-only and not reachable through the Coral Hub (operatorOnlyRoutes), so the setting is too.
	if v, ok := body[auth.RequireAuthOnLoopbackSetting]; ok {
		on, valid := validBoolSetting(v)
		if !valid {
			errBadRequest(w, auth.RequireAuthOnLoopbackSetting+" must be true or false")
			return
		}
		body[auth.RequireAuthOnLoopbackSetting] = strconv.FormatBool(on)
	}
	var current map[string]string
	for k, v := range body {
		agentType, isCLIPath := agent.IsCLIPathSetting(k)
		if !isCLIPath {
			continue
		}
		path, _ := v.(string)
		if _, isString := v.(string); !isString && v != nil {
			errBadRequest(w, k+" must be a string")
			return
		}
		if !agent.KnownCLIAgentType(agentType) {
			errBadRequest(w, "unknown agent type in "+k)
			return
		}
		if current == nil {
			if current, _ = h.ss.GetSettings(r.Context()); current == nil {
				current = map[string]string{}
			}
		}
		path = strings.TrimSpace(path)
		if path == current[k] {
			continue
		}
		if err := agent.ValidateCLIPath(path); err != nil {
			errBadRequest(w, k+": "+err.Error())
			return
		}
		body[k] = path
	}
	// Convert any value to string, matching Python's str() behavior.
	// Python's str(True)="True", str(False)="False" — capitalize bools.
	values := make(map[string]string, len(body))
	for k, v := range body {
		switch val := v.(type) {
		case bool:
			if val {
				values[k] = "True"
			} else {
				values[k] = "False"
			}
		default:
			values[k] = fmt.Sprintf("%v", v)
		}
	}
	// One request's settings are written together (one transaction): a later PUT can never
	// land between two of its keys and be half overwritten by the rest (#3547: the dashboard
	// saved welcome_choice and welcome_pending, and a PUT in between lost its welcome_pending).
	tx, err := h.db.BeginTxx(r.Context(), nil)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	defer tx.Rollback()
	for k, s := range values {
		if _, err := tx.ExecContext(r.Context(),
			"INSERT INTO user_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
			k, s); err != nil {
			errInternalServer(w, err.Error())
			return
		}
	}
	if err := tx.Commit(); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	// Apply select settings live so new WebSocket attaches see the change
	// without requiring a server restart.
	for k, s := range values {
		if k == "terminal_replay_bytes" {
			if n, err := strconv.Atoi(s); err == nil {
				ptymanager.SetReplayBytes(n)
			}
		}
		if k == "telemetry_enabled" {
			tracking.SetTelemetryEnabled(!strings.EqualFold(strings.TrimSpace(s), "false"))
		}
		if k == agentprio.SettingKey {
			agentprio.SetSetting(s) // agents started from now on (#3437)
		}
		if k == inputlog.SettingKey {
			inputlog.SetSetting(config.Truthy(s))
		}
		if k == auth.RequireAuthOnLoopbackSetting {
			auth.SetRequireAuthOnLoopback(config.Truthy(s)) // #3585: takes effect for the next request
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// DeleteDebugInputLog deletes the terminal input debug log and its rotated copy
// (#3256). It holds typed text, so only someone at this machine may delete it: the
// request must come from a loopback address and not through the Coral Hub tunnel
// (the route is also absent from the tunnel's table). While the log is on it answers 409: turn it off first.
// DELETE /api/system/debug-input-log
func (h *SystemHandler) DeleteDebugInputLog(w http.ResponseWriter, r *http.Request) {
	// Coral Hub tunnel traffic also arrives from loopback (the connector proxies to
	// 127.0.0.1), so a tunnel-stamped request is refused too, not only by the table.
	if viaTunnel(r) || !auth.IsDirectLocal(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "Delete the terminal input log from the computer running Coral."})
		return
	}
	n, err := inputlog.Delete()
	if errors.Is(err, inputlog.ErrOn) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n, "debug_input_log": inputlog.State()})
}

// gitDiffModes are the comparison bases the changed-files panel offers.
var gitDiffModes = map[string]bool{"branch_point": true, "previous_commit": true, "main_head": true}

// PutGitDiffMode sets only the git_diff_mode setting, validated against the three bases the dashboard offers. It exists so
// the changed-files panel can change the comparison base through the Cloud Hub without exposing PUT /api/settings (which can
// set CLI paths and other machine settings and stays unreachable through the hub).
// PUT /api/settings/git-diff-mode {"mode": "branch_point" | "previous_commit" | "main_head"}
func (h *SystemHandler) PutGitDiffMode(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
	var body struct {
		Mode string `json:"mode"`
	}
	if err := decodeJSON(r, &body); err != nil || !gitDiffModes[body.Mode] {
		errBadRequest(w, "mode must be branch_point, previous_commit or main_head")
		return
	}
	if _, err := h.db.ExecContext(r.Context(),
		"INSERT INTO user_settings (key, value) VALUES ('git_diff_mode', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", body.Mode); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": body.Mode})
}

// DatabaseSecurityStatus is intentionally metadata-only. Passwords and key
// material never cross this API boundary.
func (h *SystemHandler) DatabaseSecurityStatus(w http.ResponseWriter, r *http.Request) {
	b, err := dbcrypt.Load(h.cfg.CoralDir())
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	saved := b.DatabaseEncryption
	if saved == "" {
		saved = "disabled"
	}
	effective := dbcrypt.EffectiveMode()
	migrationCommand := ""
	if dbcrypt.FeatureAvailable() {
		migrationCommand = "CORAL_DB_ENCRYPTION_MIGRATE=1 coral"
	}
	available := dbcrypt.FeatureAvailable() && (saved != "password" || dbcrypt.UnlockSurface() != "headless")
	migrationRequired := false
	if saved != "disabled" {
		for _, path := range []string{h.cfg.DBPath, filepath.Join(h.cfg.CoralDir(), "messageboard.db")} {
			if plain, probeErr := dbcrypt.EncryptedFile(path); probeErr == nil && !plain && fileExists(path) {
				migrationRequired = true
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"saved_mode":          saved,
		"effective_mode":      effective,
		"restart_required":    saved != effective,
		"feature_available":   available,
		"encryption_compiled": dbcrypt.FeatureAvailable(),
		"build_variant":       dbcrypt.BuildVariant(),
		"unlock_surface":      dbcrypt.UnlockSurface(),
		"key_file_configured": b.KeyFile != "" || fileExists(filepath.Join(h.cfg.CoralDir(), dbcrypt.KeyName)),
		"migration_required":  migrationRequired,
		"migration_command":   migrationCommand,
	})
}

// SetDatabaseSecurity changes only the saved mode. It never accepts a
// password/key and takes effect after a successful restart/unlock.
func (h *SystemHandler) SetDatabaseSecurity(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := decodeJSON(r, &raw); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	for key := range raw {
		if key != "mode" {
			errBadRequest(w, "only mode may be configured; credentials are never accepted")
			return
		}
	}
	var mode string
	if value, ok := raw["mode"]; !ok || json.Unmarshal(value, &mode) != nil {
		errBadRequest(w, "mode is required")
		return
	}
	if mode != "disabled" && mode != "key_file" && mode != "password" {
		errBadRequest(w, "unsupported database encryption mode")
		return
	}
	if mode != "disabled" && !dbcrypt.FeatureAvailable() {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":      "experimental database encryption is not included in this standard build",
			"error_code": "encryption_unavailable",
		})
		return
	}
	if effective := dbcrypt.EffectiveMode(); effective != "disabled" && mode != effective {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":      "changing encryption mode requires the explicit offline migration command",
			"error_code": "migration_required",
		})
		return
	}
	current, err := dbcrypt.Load(h.cfg.CoralDir())
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if !dbcrypt.FeatureAvailable() && current.DatabaseEncryption != "" && current.DatabaseEncryption != "disabled" {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":      "saved encryption settings require an encryption-enabled build; refusing to replace them",
			"error_code": "encryption_unavailable",
		})
		return
	}
	current.DatabaseEncryption = mode
	if err := dbcrypt.Save(h.cfg.CoralDir(), current); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	h.DatabaseSecurityStatus(w, r)
}

// GetPrivacyStatus reports persisted privacy settings and the effective
// running bind state. Remote access changes are restart-applied.
// GET /api/system/privacy
func (h *SystemHandler) GetPrivacyStatus(w http.ResponseWriter, r *http.Request) {
	settings, err := h.ss.GetSettings(r.Context())
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	telemetryEnabled := !strings.EqualFold(strings.TrimSpace(settings["telemetry_enabled"]), "false")
	// Remote access is opt-in: only an explicit saved "true" enables it.
	savedValue, configured := settings["remote_access_enabled"]
	remoteEnabled := strings.EqualFold(strings.TrimSpace(savedValue), "true")
	effectiveRemote := !config.IsLoopbackHost(h.cfg.Host)
	status := "disabled"
	switch {
	case remoteEnabled && effectiveRemote:
		status = "enabled"
	case remoteEnabled:
		status = "enable_pending_restart"
	case effectiveRemote:
		status = "disable_pending_restart"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"telemetry_enabled":              telemetryEnabled,
		"remote_access_enabled":          remoteEnabled,
		"remote_access_configured":       configured,
		"remote_access_effective":        effectiveRemote,
		"remote_access_restart_required": remoteEnabled != effectiveRemote,
		"remote_access_status":           status,
		// TLS (#3355): whether the port also serves HTTPS. Remote access without it is
		// unencrypted, and the dashboard says so.
		"remote_access_tls": h.cfg.TLS,
		"effective_host":    h.cfg.Host,
		"debug_input_log":   inputlog.State(),
	})
}

// ── Default Prompt Constants ─────────────────────────────────────────
// These reference the authoritative constants in the agent package so
// the frontend defaults stay in sync with the runtime prompts.

// GetDefaultPrompts returns the hardcoded default prompt templates.
// GET /api/settings/default-prompts
func (h *SystemHandler) GetDefaultPrompts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"default_prompt_orchestrator":        agent.DefaultOrchestratorActionPrompt,
		"default_prompt_worker":              agent.DefaultWorkerActionPrompt,
		"default_system_prompt_orchestrator": agent.DefaultOrchestratorSystemPrompt,
		"default_system_prompt_worker":       agent.DefaultWorkerSystemPrompt,
		"team_reminder_orchestrator":         "Remember to coordinate with your team and check the message board for updates",
		"team_reminder_worker":               "Remember to work with your team",
	})
}

// GetAgentModels returns the canonical per-agent-type model list used to
// populate the Model dropdown in the UI. The frontend still accepts free-form
// model strings — this list is only the curated suggestions.
// GET /api/agent-models
func (h *SystemHandler) GetAgentModels(w http.ResponseWriter, r *http.Request) {
	out := make(map[string][]string, len(agenttypes.AgentTypeModels))
	for k, v := range agenttypes.AgentTypeModels {
		if v == nil {
			out[k] = []string{}
		} else {
			out[k] = v
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// Status returns server status.
// GET /api/system/status
func (h *SystemHandler) Status(w http.ResponseWriter, r *http.Request) {
	tmuxPath, tmuxAvailable := h.discoverTmux(wantsFreshStatus(r))
	// Analytics: observe tmux availability (deduplicated per run, so frequent
	// status polling sends at most one event per result).
	tmuxStatus := tracking.PrereqMissing
	if tmuxAvailable {
		tmuxStatus = tracking.PrereqAvailable
	}
	tracking.TrackPrerequisite(tracking.ToolTmux, tmuxStatus, tracking.SourceSystemStatus)
	writeJSON(w, http.StatusOK, map[string]any{
		"startup_complete":  true,
		"version":           config.Version,
		"tier_name":         config.TierName,
		"tmux_available":    tmuxAvailable,
		"tmux_status":       tmuxStatus,
		"tmux_server_issue": tmuxServerIssue(),
		"effective_backend": h.effectiveBackend(),
		// tmux_required: agents cannot be launched without tmux on this server
		// (it runs the tmux backend). false on the PTY backend.
		"tmux_required":        h.effectiveBackend() == "tmux",
		"tmux_path":            tmuxPath,
		"tmux_install_command": tmuxInstallCommand(),
		// A data folder other people on this computer can read (Windows; #3637): shown on the
		// dashboard's first page, not only in the log. "" when there is nothing to say.
		"security_warning": dataDirWarning(h.cfg.CoralDir()),
		// Damage the integrity check found a minute after start (#4157); "" when none.
		"database_warning": background.DatabaseWarning(),
	})
}

// dataDirWarning is config.DataDirWarning (a variable so a test can stand in for a Windows
// folder with a broad access list).
var dataDirWarning = config.DataDirWarning

// tmuxInstallCommand returns the platform-appropriate install command for tmux.
func tmuxInstallCommand() string {
	switch runtime.GOOS {
	case "darwin":
		return "brew install tmux"
	case "linux":
		return "sudo apt install tmux  (or your distro's package manager)"
	default:
		return "install tmux for your platform"
	}
}

// githubReleasesAPI is where the update check asks (tests point it at their own server).
var githubReleasesAPI = updatecheck.ReleasesAPI

const githubReleasesURL = updatecheck.ReleasesURL

// FetchLatestVersion is the latest release's version (without "v"), or "" on any error: a reply
// that isn't a 200, too large, or a tag that isn't a version (updatecheck, #3660).
func FetchLatestVersion() string {
	v, _ := updatecheck.Default.Latest(context.Background(), githubReleasesAPI)
	return v
}

// fetchLatestVersionRefresh is FetchLatestVersion for a person's "Check for Updates": it asks
// GitHub again when the cached answer is over a minute old (updatecheck.RefreshAfter), else it
// returns the cached one, so the button can't be used to hammer GitHub (#4133).
func fetchLatestVersionRefresh() string {
	v, _ := updatecheck.Default.Refresh(context.Background(), githubReleasesAPI)
	return v
}

// UpdateCheck returns update availability info.
// GET /api/system/update-check
func (h *SystemHandler) UpdateCheck(w http.ResponseWriter, r *http.Request) {
	if !config.TierChecksForUpdates {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"current":   config.Version,
		})
		return
	}
	// Answered from a cache (an hour, a failure five minutes) with one request in flight at a time,
	// for every caller, an agent's token included (#4133). ?refresh=1 is the dashboard's manual
	// "Check for Updates": it asks again, at most once a minute.
	var latest string
	if r.URL.Query().Get("refresh") == "1" {
		latest = fetchLatestVersionRefresh()
	} else {
		latest = FetchLatestVersion()
	}
	// Only a strictly newer release: a dev, beta or newer build is never sent to an older one.
	available := updatecheck.Newer(latest, config.Version)
	writeJSON(w, http.StatusOK, map[string]any{
		"available":   available,
		"current":     config.Version,
		"latest":      latest,
		"release_url": githubReleasesURL,
	})
}

// CLICheck verifies whether a CLI tool is installed and returns its version.
// GET /api/system/cli-check?type=claude  (check by agent type)
// GET /api/system/cli-check?binary=/path/to/codex  (check specific path)
func (h *SystemHandler) CLICheck(w http.ResponseWriter, r *http.Request) {
	binaryPath := r.URL.Query().Get("binary")
	agentType := r.URL.Query().Get("type")

	if binaryPath == "" && agentType == "" {
		errBadRequest(w, "type or binary parameter required")
		return
	}
	if binaryPath != "" {
		// Running a named binary is the Operator's, and only a CLI's: never a path an agent
		// chose or could have written (#3583, Security's sweep item 5).
		if cloud.Tunneled(r) || !humanInputCaller(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the Operator can check a CLI by its path", "code": "operator_only"})
			return
		}
		if why := h.cliCheckRefusal(r.Context(), binaryPath, agentType); why != "" {
			errBadRequest(w, why)
			return
		}
	}

	var installCmd string
	if binaryPath == "" {
		info := agent.GetCLIInfo(agentType)
		if info == nil {
			writeJSON(w, http.StatusOK, map[string]any{"found": true, "agent_type": agentType})
			return
		}
		binaryPath = info.Binary
		installCmd = info.InstallCommand
		// The CLI path setting is what launch runs, so a check by type looks
		// there first: a configured path is not reported as "not found" (#3169).
		if h.ss != nil {
			if settings, _ := h.ss.GetSettings(r.Context()); settings != nil {
				if p := agent.ResolveCLIPath(settings, agentType); p != "" {
					if _, err := cliLookPath(p); err == nil {
						binaryPath = p
					}
				}
			}
		}
	}

	// Analytics tracks only the known tools checked by agent type; a caller-
	// supplied binary path is not a prerequisite observation.
	tool := prerequisiteTool(agentType)
	trackable := tool != "" && r.URL.Query().Get("binary") == ""
	source := tracking.SourceCLICheck
	if r.URL.Query().Get("source") == tracking.SourceCLIRecheck {
		source = tracking.SourceCLIRecheck
	}

	// Try LookPath first, then common install locations
	resolvedPath, err := cliLookPath(binaryPath)
	if err != nil {
		if found := cliCommonPath(binaryPath); found != "" {
			resolvedPath = found
		} else {
			result := map[string]any{
				"found":      false,
				"status":     tracking.PrereqMissing,
				"binary":     binaryPath,
				"agent_type": agentType,
			}
			if installCmd != "" {
				result["install_command"] = installCmd
			}
			if trackable {
				tracking.TrackPrerequisite(tool, tracking.PrereqMissing, source)
			}
			writeJSON(w, http.StatusOK, result)
			return
		}
	}

	// Try to get version. A failed or timed-out probe is reported separately
	// from "found": the binary exists, but running it did not work.
	version, probe := probeCLIVersion(r.Context(), resolvedPath)
	status := tracking.PrereqAvailable
	switch probe {
	case probeFailed:
		status = tracking.PrereqProbeFailed
	case probeTimeout:
		status = tracking.PrereqTimeout
	}
	if trackable && probe != probeCanceled {
		tracking.TrackPrerequisite(tool, status, source)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"found":      true,
		"status":     status,
		"probe":      probe,
		"path":       resolvedPath,
		"version":    version,
		"agent_type": agentType,
	})
}

// Seams for the CLI and tmux prerequisite checks; tests replace them so no real
// executable or user directory is consulted.
var (
	cliLookPath     = exec.LookPath
	cliCommonPath   = agent.FindCLIInCommonPaths
	cliProbeTimeout = 3 * time.Second
	tmuxProbe       = tmux.IsAvailable
)

// Version probe results.
const (
	probeOK       = "ok"
	probeFailed   = "failed"
	probeTimeout  = "timeout"
	probeCanceled = "canceled" // the caller went away; not an observation
)

// prerequisiteTool maps an agent type to the tool name analytics may report.
func prerequisiteTool(agentType string) string {
	switch agentType {
	case "claude":
		return tracking.ToolClaude
	case "codex":
		return tracking.ToolCodex
	}
	return ""
}

// probeCLIVersion runs "<binary> --version" under a bounded timeout. It returns
// the first line of output and a classification; raw errors are never returned.
const maxCLIVersionBytes = 8 * 1024

type cliVersionOutput struct{ strings.Builder }

func (b *cliVersionOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := maxCLIVersionBytes - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Builder.Write(p)
	}
	// Drain excess output without allocating or breaking the child's pipe.
	return n, nil
}

func probeCLIVersion(parent context.Context, path string) (version, probe string) {
	ctx, cancel := context.WithTimeout(parent, cliProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	// A wrapper script can leave a child holding the output pipe after it is
	// killed; do not wait on it past this.
	cmd.WaitDelay = 200 * time.Millisecond
	var out cliVersionOutput
	cmd.Stdout = &out
	err := cmd.Run()
	if err == nil {
		version = strings.TrimSpace(out.String())
		// Take first line only
		if idx := strings.IndexByte(version, '\n'); idx > 0 {
			version = version[:idx]
		}
		return version, probeOK
	}
	switch {
	case parent.Err() != nil:
		return "", probeCanceled
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "", probeTimeout
	}
	return "", probeFailed
}

// RefreshIndexer triggers a manual re-index.
// POST /api/indexer/refresh
func (h *SystemHandler) RefreshIndexer(w http.ResponseWriter, r *http.Request) {
	if h.indexer == nil {
		errInternalServer(w, "Indexer not available")
		return
	}
	if err := h.indexer.RunOnce(r.Context()); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// expandHome expands a leading ~ to the home directory.
func expandHome(p string) string {
	if strings.HasPrefix(p, "~") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[1:])
	}
	return p
}

// browsable resolves a requested directory. Through the tunnel it must lie inside the home
// directory or a configured project root after symlink resolution and must not be a
// credential location; otherwise it is reported exactly like a missing directory.
func browsable(r *http.Request, reqPath string) (string, bool) {
	// A network path is reported like a missing folder, before anything stats it (#3671): on
	// Windows the stat alone connects to the host as the user. Every caller (the folder browser,
	// is-git) stats what this returns.
	if safefs.RefuseRemote(reqPath) != nil {
		return "", false
	}
	expanded, err := filepath.Abs(expandHome(reqPath))
	if err != nil || safefs.RefuseRemote(expanded) != nil {
		return "", false
	}
	if !strictBounds(r) {
		return expanded, true
	}
	real := realPath(expanded)
	if real == "" || !withinAny(projectRoots(), real) || sensitivePath(real) {
		return "", false
	}
	return real, true
}

// ListFilesystem lists directories for the directory browser.
// GET /api/filesystem/list?path=~
func (h *SystemHandler) ListFilesystem(w http.ResponseWriter, r *http.Request) {
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" {
		reqPath = "~"
	}
	expanded, ok := browsable(r, reqPath)
	if !ok {
		errForbidden(w, "access denied")
		return
	}

	// Verify the path exists and is a directory (no traversal beyond real paths)
	info, err := os.Stat(expanded)
	if err != nil || !info.IsDir() {
		errForbidden(w, "access denied")
		return
	}

	entries, err := os.ReadDir(expanded)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"path":    expanded,
			"entries": []any{},
			"error":   "cannot read directory",
		})
		return
	}

	// Python returns flat string array of directory names (not objects)
	dirs := make([]string, 0)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue // skip hidden
		}
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)

	writeJSON(w, http.StatusOK, map[string]any{
		"path":    expanded,
		"entries": dirs,
	})
}

// IsGitDir checks if a directory is a git repository.
// GET /api/filesystem/is-git?path=/some/dir
func (h *SystemHandler) IsGitDir(w http.ResponseWriter, r *http.Request) {
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" {
		writeJSON(w, http.StatusOK, map[string]bool{"is_git": false})
		return
	}
	expanded, ok := browsable(r, reqPath)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]bool{"is_git": false})
		return
	}
	_, err := os.Stat(filepath.Join(expanded, ".git"))
	writeJSON(w, http.StatusOK, map[string]bool{"is_git": err == nil})
}

// ── Tags ────────────────────────────────────────────────────────────────

// ListTags returns all tags.
// GET /api/tags
func (h *SystemHandler) ListTags(w http.ResponseWriter, r *http.Request) {
	type tag struct {
		ID    int    `json:"id" db:"id"`
		Name  string `json:"name" db:"name"`
		Color string `json:"color" db:"color"`
	}
	var tags []tag
	if err := h.db.Reader().SelectContext(r.Context(), &tags, "SELECT id, name, color FROM tags ORDER BY name"); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(tags))
}

// CreateTag creates a new tag.
// POST /api/tags
func (h *SystemHandler) CreateTag(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Name == "" {
		errBadRequest(w, "Tag name is required")
		return
	}
	if body.Color == "" {
		body.Color = "#58a6ff"
	}
	result, err := h.db.ExecContext(r.Context(),
		"INSERT INTO tags (name, color) VALUES (?, ?)", body.Name, body.Color)
	if err != nil {
		errConflict(w, "tag already exists")
		return
	}
	id, _ := result.LastInsertId()
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": body.Name, "color": body.Color})
}

// DeleteTag removes a tag.
// DELETE /api/tags/{tagID}
func (h *SystemHandler) DeleteTag(w http.ResponseWriter, r *http.Request) {
	tagID, _ := strconv.Atoi(chi.URLParam(r, "tagID"))
	h.db.ExecContext(r.Context(), "DELETE FROM tags WHERE id = ?", tagID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// AddSessionTag adds a tag to a session.
// POST /api/sessions/history/{sessionID}/tags
func (h *SystemHandler) AddSessionTag(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	var body struct {
		TagID int `json:"tag_id"`
	}
	if err := decodeJSON(r, &body); err != nil || body.TagID == 0 {
		errBadRequest(w, "tag_id is required")
		return
	}
	h.db.ExecContext(r.Context(),
		"INSERT OR IGNORE INTO session_tags (session_id, tag_id) VALUES (?, ?)",
		sessionID, body.TagID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// RemoveSessionTag removes a tag from a session.
// DELETE /api/sessions/history/{sessionID}/tags/{tagID}
func (h *SystemHandler) RemoveSessionTag(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	tagID, _ := strconv.Atoi(chi.URLParam(r, "tagID"))
	h.db.ExecContext(r.Context(),
		"DELETE FROM session_tags WHERE session_id = ? AND tag_id = ?",
		sessionID, tagID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── Folder Tags ─────────────────────────────────────────────────────────

// GetAllFolderTags returns all folder tags grouped by folder name.
// GET /api/folder-tags
func (h *SystemHandler) GetAllFolderTags(w http.ResponseWriter, r *http.Request) {
	result, err := h.ss.GetAllFolderTags(r.Context())
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if result == nil {
		result = map[string][]store.Tag{}
	}
	writeJSON(w, http.StatusOK, result)
}

// GetFolderTags returns tags for a specific folder.
// GET /api/folder-tags/{folderName}
func (h *SystemHandler) GetFolderTags(w http.ResponseWriter, r *http.Request) {
	folderName := chi.URLParam(r, "folderName")
	tags, err := h.ss.GetFolderTags(r.Context(), folderName)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, emptyIfNil(tags))
}

// AddFolderTag adds a tag to a folder.
// POST /api/folder-tags/{folderName}
func (h *SystemHandler) AddFolderTag(w http.ResponseWriter, r *http.Request) {
	folderName := chi.URLParam(r, "folderName")
	var body struct {
		TagID int64 `json:"tag_id"`
	}
	if err := decodeJSON(r, &body); err != nil || body.TagID == 0 {
		errBadRequest(w, "tag_id is required")
		return
	}
	if err := h.ss.AddFolderTag(r.Context(), folderName, body.TagID); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// RemoveFolderTag removes a tag from a folder.
// DELETE /api/folder-tags/{folderName}/{tagID}
func (h *SystemHandler) RemoveFolderTag(w http.ResponseWriter, r *http.Request) {
	folderName := chi.URLParam(r, "folderName")
	tagID, _ := strconv.ParseInt(chi.URLParam(r, "tagID"), 10, 64)
	if err := h.ss.RemoveFolderTag(r.Context(), folderName, tagID); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// QRCode generates a QR code PNG for the given URL.
// GET /api/system/qr?url=<encoded_url>
func (h *SystemHandler) QRCode(w http.ResponseWriter, r *http.Request) {
	url := r.URL.Query().Get("url")
	if url == "" {
		http.Error(w, "url parameter required", http.StatusBadRequest)
		return
	}
	png, err := qrcode.Encode(url, qrcode.Medium, 256)
	if err != nil {
		http.Error(w, "failed to generate QR code", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	// The URL it encodes can carry a sign-in token: never kept by a browser or a shared cache (#3682).
	w.Header().Set("Cache-Control", "private, no-store")
	w.Write(png)
}

// NetworkInfo returns the server's LAN IP addresses and port.
// GET /api/system/network-info
func (h *SystemHandler) NetworkInfo(w http.ResponseWriter, r *http.Request) {
	var ips []string
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
				ips = append(ips, ipNet.IP.String())
			}
		}
	}
	primary := ""
	for _, ip := range ips {
		if strings.HasPrefix(ip, "192.168.") || strings.HasPrefix(ip, "10.") || strings.HasPrefix(ip, "172.") {
			primary = ip
			break
		}
	}
	if primary == "" && len(ips) > 0 {
		primary = ips[0]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ips":                     ips,
		"primary":                 primary,
		"port":                    h.cfg.Port,
		"remote_access_effective": !config.IsLoopbackHost(h.cfg.Host),
		// The scheme other devices must use: with TLS on, plain HTTP is refused from the
		// network (#3355).
		"scheme": map[bool]string{true: "https", false: "http"}[h.cfg.TLS],
	})
}

// ── Team Import ─────────────────────────────────────────────────────────

// ImportTeam parses a folder-based team definition and returns team config JSON.
// POST /api/teams/import
//
// The folder structure is:
//
//	my-team/
//	  SKILL.md         → Orchestrator agent (frontmatter: name, description, tools)
//	  agents/
//	    agent-name.md  → Worker agents (frontmatter: name, description, tools)
//
// A file whose frontmatter has mcp_servers is refused: Coral doesn't configure MCP servers (#3644).
//
// Each .md file has YAML frontmatter (---\nkey: value\n---) followed by the prompt body.
func (h *SystemHandler) ImportTeam(w http.ResponseWriter, r *http.Request) {
	// Reading a folder of the caller's choosing is the Operator's (#3583, Security's sweep item 4):
	// an agent's token is refused, and the folder and its files are bounded (importTeamRoot,
	// readImportFile): no symlink is followed, no dot-file or sensitive path is read, files are
	// capped at 256 KiB and agents/ at 50.
	if !humanInputCaller(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "only the Operator can import a team from a folder", "code": "operator_only"})
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.Path == "" {
		errBadRequest(w, "path is required")
		return
	}

	root, err := importTeamRoot(body.Path)
	if err != nil {
		errBadRequest(w, strings.TrimPrefix(err.Error(), errImportRefused.Error()+": "))
		return
	}

	teamName := filepath.Base(body.Path)
	var agents []map[string]any

	// Parse SKILL.md as orchestrator
	skillPath := filepath.Join(root, "SKILL.md")
	data, found, err := readImportFile(root, skillPath)
	if err != nil {
		errBadRequest(w, strings.TrimPrefix(err.Error(), errImportRefused.Error()+": "))
		return
	}
	if found {
		meta, prompt := parseFrontmatterMD(string(data))
		if meta.Name == "" {
			meta.Name = "Orchestrator"
		}
		agentDef := map[string]any{
			"name":   meta.Name,
			"role":   "orchestrator",
			"prompt": prompt,
		}
		if meta.Description != "" {
			agentDef["description"] = meta.Description
		}
		if len(meta.Tools) > 0 {
			agentDef["tools"] = meta.Tools
		}
		if meta.MCPServers != nil {
			errBadRequest(w, "SKILL.md: mcp_servers is not supported: configure MCP servers in the agent CLI's own settings")
			return
		}
		agents = append(agents, agentDef)
	}

	// Parse agents/*.md as workers
	agentsDir := filepath.Join(root, "agents")
	if fi, lerr := os.Lstat(agentsDir); lerr == nil && fi.IsDir() {
		entries, _ := os.ReadDir(agentsDir)
		read := 0
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			if read >= importAgentsMax {
				errBadRequest(w, fmt.Sprintf("agents/ has more than %d agent files", importAgentsMax))
				return
			}
			data, found, err := readImportFile(root, filepath.Join(agentsDir, entry.Name()))
			if err != nil {
				errBadRequest(w, strings.TrimPrefix(err.Error(), errImportRefused.Error()+": "))
				return
			}
			if !found {
				continue
			}
			read++
			meta, prompt := parseFrontmatterMD(string(data))
			if meta.Name == "" {
				meta.Name = strings.TrimSuffix(entry.Name(), ".md")
			}
			agentDef := map[string]any{
				"name":   meta.Name,
				"prompt": prompt,
			}
			if meta.Description != "" {
				agentDef["description"] = meta.Description
			}
			if len(meta.Tools) > 0 {
				agentDef["tools"] = meta.Tools
			}
			if meta.MCPServers != nil {
				errBadRequest(w, "agents/"+entry.Name()+": mcp_servers is not supported: configure MCP servers in the agent CLI's own settings")
				return
			}
			agents = append(agents, agentDef)
		}
	}

	if len(agents) == 0 {
		errBadRequest(w, "no agent definitions found in directory")
		return
	}

	teamConfig := map[string]any{
		"name":   teamName,
		"agents": agents,
	}

	writeJSON(w, http.StatusOK, teamConfig)
}

// ── Team Generation (async) ─────────────────────────────────────────

type generateJob struct {
	Status    string         `json:"status"` // "pending", "complete", "error"
	Result    map[string]any `json:"result,omitempty"`
	Error     string         `json:"error,omitempty"`
	CreatedAt time.Time      `json:"-"`
}

// GenerateTeam kicks off an async Claude CLI call and returns a job ID.
// POST /api/teams/generate
func (h *SystemHandler) GenerateTeam(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Directive   string `json:"directive"`
		Composition string `json:"composition"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if strings.TrimSpace(body.Directive) == "" {
		errBadRequest(w, "directive is required")
		return
	}

	const maxInputLen = 4000
	if len(body.Directive) > maxInputLen {
		errBadRequest(w, "directive too long (max 4000 characters)")
		return
	}
	if len(body.Composition) > maxInputLen {
		errBadRequest(w, "composition too long (max 4000 characters)")
		return
	}

	settings, _ := h.ss.GetSettings(r.Context())
	claudePath, err := agent.FindCLI(settings, agenttypes.Claude)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "Claude CLI not found — install Claude Code (npm install -g @anthropic-ai/claude-code) or set its path in Settings > CLI Paths",
		})
		return
	}

	jobID := "gen-" + uuid.New().String()
	job := &generateJob{Status: "pending", CreatedAt: time.Now()}

	h.generateJobsMu.Lock()
	h.generateJobs[jobID] = job
	h.generateJobsMu.Unlock()

	prompt := teamGeneratePrompt +
		"\n\n<inputs>" +
		"\n<directive>\n" + body.Directive + "\n</directive>" +
		"\n<composition>\n" + body.Composition + "\n</composition>" +
		"\n</inputs>"

	coralDir := h.cfg.CoralDir()

	go func() {
		defer safego.Recover("server/routes/system:1327")
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()

		result, errMsg := runTeamGeneration(ctx, claudePath, prompt, coralDir)

		h.generateJobsMu.Lock()
		defer h.generateJobsMu.Unlock()
		if errMsg != "" {
			job.Status = "error"
			job.Error = errMsg
		} else {
			job.Status = "complete"
			job.Result = result
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": jobID, "status": "pending"})
}

// GenerateTeamStatus returns the status of an async team generation job.
// GET /api/teams/generate/{jobId}
func (h *SystemHandler) GenerateTeamStatus(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobId")

	h.generateJobsMu.Lock()
	job, ok := h.generateJobs[jobID]
	h.generateJobsMu.Unlock()

	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}

	resp := map[string]any{"job_id": jobID, "status": job.Status}
	if job.Status == "complete" {
		resp["result"] = job.Result
	}
	if job.Status == "error" {
		resp["error"] = job.Error
	}
	writeJSON(w, http.StatusOK, resp)
}

// runTeamGeneration executes Claude CLI and parses/validates the response.
// Returns (result, errorMessage). If errorMessage is non-empty, result is nil.
func runTeamGeneration(ctx context.Context, claudePath, prompt, coralDir string) (map[string]any, string) {
	cmd := executil.Command(ctx, claudePath,
		"--print",
		"--model", "opus",
		"--no-session-persistence",
		prompt,
	)
	output, err := cmd.Output()
	if err != nil {
		return nil, "Claude CLI failed: " + err.Error()
	}

	// Write raw response to ~/.coral/last_generated_team.json for debugging
	if coralDir != "" {
		_ = os.WriteFile(filepath.Join(coralDir, "last_generated_team.json"), output, 0o600)
	}

	// Strip markdown fences if present
	raw := strings.TrimSpace(string(output))
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		var cleaned []string
		for _, line := range lines {
			if !strings.HasPrefix(strings.TrimSpace(line), "```") {
				cleaned = append(cleaned, line)
			}
		}
		raw = strings.TrimSpace(strings.Join(cleaned, "\n"))
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, "Failed to parse LLM response as JSON"
	}

	// Validate that agents array exists
	agents, ok := result["agents"].([]any)
	if !ok || len(agents) == 0 {
		return nil, "Generated team has no agents"
	}

	// Validate and normalize each agent — fill defaults for missing fields
	for i, a := range agents {
		ag, ok := a.(map[string]any)
		if !ok {
			return nil, fmt.Sprintf("agent at index %d is not a valid object", i)
		}

		name, _ := ag["name"].(string)
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Sprintf("agent at index %d is missing a name", i)
		}

		agPrompt, _ := ag["prompt"].(string)
		if strings.TrimSpace(agPrompt) == "" {
			return nil, fmt.Sprintf("agent '%s' is missing a prompt", name)
		}

		if at, _ := ag["agent_type"].(string); at == "" {
			ag["agent_type"] = "claude"
		}
		if _, exists := ag["model"]; !exists {
			ag["model"] = ""
		}
		if tools, ok := ag["tools"]; !ok || tools == nil {
			ag["tools"] = []any{}
		} else if _, ok := tools.([]any); !ok {
			return nil, fmt.Sprintf("agent '%s' has invalid tools; expected array", name)
		}
		// Coral doesn't configure MCP servers (#3644): a generated team never carries any.
		delete(ag, "mcpServers")

		caps, _ := ag["capabilities"].(map[string]any)
		if caps == nil {
			caps = map[string]any{"allow": []any{"file_read"}, "deny": []any{}}
			ag["capabilities"] = caps
		} else {
			if _, ok := caps["allow"]; !ok {
				caps["allow"] = []any{"file_read"}
			}
			if _, ok := caps["deny"]; !ok {
				caps["deny"] = []any{}
			}
		}

		agents[i] = ag
	}
	result["agents"] = agents

	if name, _ := result["name"].(string); strings.TrimSpace(name) == "" {
		result["name"] = "Generated Team"
	}
	if _, exists := result["flags"]; !exists {
		result["flags"] = ""
	}

	return result, ""
}

const teamGeneratePrompt = `Role and task:
You generate team configuration JSON for Coral, an AI agent orchestration platform. Read the user's directive and composition guidance, then produce a single valid team configuration object.

Output contract:
You MUST respond with ONLY one valid JSON object.
Do not include markdown, code fences, comments, trailing commas, or explanatory text.
Every string must be valid escaped JSON.
The response must match this exact schema:

{
  "name": "Short team name (2-4 words)",
  "agents": [
    {
      "name": "Agent Display Name",
      "prompt": "Detailed prompt for this agent",
      "tools": ["TodoWrite", "Bash(npm *)"],
      "capabilities": {
        "allow": ["capability1", "capability2"],
        "deny": ["capability3"]
      },
      "agent_type": "claude",
      "model": ""
    }
  ],
  "flags": ""
}

Hard platform rules:
1. The first agent MUST be named "Orchestrator".
2. Every team MUST contain 3-8 agents. If the user asks for fewer than 3, still produce 3 agents by adding the minimum supporting roles needed. If the user asks for more than 8, consolidate responsibilities to stay within 8.
3. The Orchestrator coordinates work, delegates tasks via the message board, tracks progress, and should not do the implementation work itself.
4. The Orchestrator must include these allowed capabilities: ["file_read", "shell:coral-board *", "agent_spawn", "web_access"].
5. Every worker prompt must say they are automatically joined to the message board, must not run "coral-board join", must not poll or loop on "coral-board read", and must wait for instructions from the Orchestrator before starting work.
6. The Orchestrator prompt must say it is automatically joined to the message board, must not run "coral-board join", must not poll or loop on "coral-board read", and should discuss its plan with the operator before posting assignments.
7. Every agent object MUST include all of these keys: "name", "prompt", "capabilities", "agent_type", and "model".
8. If useful, agent objects MAY also include "tools" (array of strings). Never include "mcpServers": Coral does not configure MCP servers.
9. Every "capabilities" object MUST include both "allow" and "deny" arrays, even if "deny" is empty.
10. Use "claude" for "agent_type" unless the user explicitly requests a different agent type.
11. Use an empty string for "model" unless the user explicitly requests a specific model for that agent.
12. Use an empty string for "flags" unless the user explicitly requests flags.

Capability policy:
Use only these capabilities:
- file_read: Read files (Read, Glob, Grep tools)
- file_write: Write/edit files (Write, Edit tools)
- shell: Execute shell commands (Bash tool)
- web_access: Web browsing (WebFetch, WebSearch tools)
- git_write: Git operations (push, commit, branch, merge, rebase)
- agent_spawn: Launch sub-agents
- notebook: Edit Jupyter notebooks
- shell:<pattern>: Restricted shell (for example "shell:npm *" or "shell:coral-board *")

Assign the minimum capabilities each agent needs. Follow least privilege.
If a task can be done without shell access, do not grant shell.
Prefer restricted shell patterns over unrestricted "shell" when practical.
If the user requests unknown or unsupported capabilities, ignore those requests and use the closest supported safe alternative.

Input interpretation rules:
Treat <directive> as the mission and desired outcome.
Treat <composition> as preferred team structure, role mix, skill emphasis, model preferences, and constraints.
If <composition> is empty or vague, infer a reasonable team structure from <directive>.
If the user's requests conflict with the hard platform rules, obey the hard platform rules.
If the user asks for an overpowered agent, reduce permissions to the minimum needed.
In prompts, make each role specific and concrete. Avoid generic filler.
Only include "tools" when they materially help the role.

Few-shot example:
{
  "name": "Coding Team",
  "agents": [
    {
      "name": "Orchestrator",
      "prompt": "You are the orchestrator for a coding team. You are automatically joined to the message board. Do not run coral-board join. Do not poll or loop on coral-board read. Break the work into steps, discuss your plan with the operator before posting assignments, delegate implementation and verification to the team via the message board, and track progress. Do not do the implementation work yourself.",
      "tools": [],
      "capabilities": {
        "allow": ["file_read", "file_write", "shell", "git_write", "agent_spawn", "web_access"],
        "deny": []
      },
      "agent_type": "claude",
      "model": ""
    },
    {
      "name": "Lead Developer",
      "prompt": "You are the lead developer. You are automatically joined to the message board. Do not run coral-board join. Do not poll or loop on coral-board read. Wait for instructions from the Orchestrator before starting. Implement features, modify code, run necessary development commands, and coordinate status updates through the message board.",
      "tools": [],
      "capabilities": {
        "allow": ["file_read", "file_write", "shell", "git_write", "agent_spawn", "web_access"],
        "deny": []
      },
      "agent_type": "claude",
      "model": ""
    },
    {
      "name": "QA Engineer",
      "prompt": "You are the QA engineer. You are automatically joined to the message board. Do not run coral-board join. Do not poll or loop on coral-board read. Wait for instructions from the Orchestrator before starting. Review changes, write or run tests when needed, verify behavior, and report risks and regressions through the message board.",
      "tools": [],
      "capabilities": {
        "allow": ["file_read", "file_write", "shell", "git_write", "agent_spawn", "web_access"],
        "deny": []
      },
      "agent_type": "claude",
      "model": ""
    }
  ],
  "flags": ""
}

Final validation checklist:
- Output exactly one JSON object and nothing else.
- Ensure the first agent is "Orchestrator".
- Ensure there are 3-8 agents.
- Ensure every agent has "name", "prompt", "capabilities", "agent_type", and "model".
- If present, ensure "tools" is an array of strings, and that no agent has "mcpServers".
- Ensure every capabilities object has both "allow" and "deny".
- Ensure every capability string is from the supported list above.
- Ensure prompts include the message board coordination rules.
- Ensure "flags" is present.

Delimited user inputs:
You will receive:
- <directive>...</directive>
- <composition>...</composition>

Generate the team configuration now.`

type agentFrontmatter struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Tools       []string       `yaml:"tools"`
	MCPServers  map[string]any `yaml:"mcp_servers"` // only to refuse it (#3644)
}

// parseFrontmatterMD extracts YAML frontmatter and body from markdown.
// Returns (frontmatter, body). If no frontmatter, body is the full content.
func parseFrontmatterMD(content string) (agentFrontmatter, string) {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "---") {
		return agentFrontmatter{}, content
	}

	// Find closing ---
	rest := content[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return agentFrontmatter{}, content
	}

	frontmatter := rest[:idx]
	body := strings.TrimSpace(rest[idx+4:])

	var meta agentFrontmatter
	if err := yaml.Unmarshal([]byte(frontmatter), &meta); err != nil {
		return agentFrontmatter{}, content
	}

	return meta, body
}

// tmuxServerIssue is non-empty when the running tmux server cannot be talked
// to by this Coral's tmux (a client/server version mismatch). Coral never
// stops the old server; the message says how to recover.
func tmuxServerIssue() string {
	msg, _ := tmux.IncompatibleServer()
	return msg
}

// validBoolSetting normalises a settings value that should be a boolean. It accepts a JSON bool
// or the strings true/false/1/0/yes/no/on/off (any case); anything else is invalid (#3585).
func validBoolSetting(v any) (on bool, valid bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "1", "true", "yes", "on":
			return true, true
		case "0", "false", "no", "off", "":
			return false, true
		}
	}
	return false, false
}
