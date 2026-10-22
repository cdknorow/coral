// Reading and saving Coral's settings, and the rules a saved value must meet (moved out of system.go, #5003; no behaviour change).

package routes

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/agentprio"
	"github.com/subgentic/coral/internal/auth"
	"github.com/subgentic/coral/internal/background"
	"github.com/subgentic/coral/internal/inputlog"
	"github.com/subgentic/coral/internal/ptymanager"
	setreg "github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/internal/tracking"
)

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
// (all of them are denied to a Coral Hub sign-in, and the dashboard writes them there).
var hubTrustSettingRoutes = map[string]string{
	MaxModeSetting:            "PUT /api/cloud/max-mode",
	AllowHubAutoAcceptSetting: "PUT /api/cloud/allow-auto-accept",
	RedactTokensSetting:       "PUT /api/cloud/redact-tokens",
	HubRelaunchSetting:        "PUT /api/cloud/hub-relaunch",       // #5280
	CodexTrustHooksSetting:    "PUT /api/system/codex-trust-hooks", // #153
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
	// Every check comes before anything is saved, in this order: the first one a request fails is its answer.
	if msg := normalizeSettingTypes(body); msg != "" {
		errBadRequest(w, msg)
		return
	}
	if msg := settingWithItsOwnRoute(body); msg != "" {
		errBadRequest(w, msg)
		return
	}
	// Every key must be one the registry knows, with a value it accepts (Security's #4431 SHOULD 3).
	for _, check := range []func(map[string]any) error{
		checkSettingKeys,
		validateHubLimitSettings,  // the Coral Hub request pools' sizes (#3642): whole numbers in range, or empty for the default
		validateAutoWakeSettings,  // the auto-wake setting and cap (#3636): a boolean, and a whole number from 1 to 50
		validateAutoResumeSetting, // resume agents stopped by an API error (#3613): a boolean
	} {
		if err := check(body); err != nil {
			errBadRequest(w, err.Error())
			return
		}
	}
	if msg := h.normalizeSettingValues(r.Context(), body); msg != "" {
		errBadRequest(w, msg)
		return
	}
	if msg := h.checkCLIPaths(r.Context(), body); msg != "" {
		errBadRequest(w, msg)
		return
	}
	values := settingStrings(body)
	// One request's settings are written together (one transaction): a later PUT can never
	// land between two of its keys and be half overwritten by the rest (#3547: the dashboard
	// saved welcome_choice and welcome_pending, and a PUT in between lost its welcome_pending).
	if err := h.saveSettings(r.Context(), values); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	applyLiveSettings(values)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// normalizeSettingTypes: a setting is a string, a boolean or a number (Security's #4431 SHOULD 1). null is empty, as
// the request-pool sizes already took it: it was stored as the text "<nil>", which as a CLI path broke every launch.
// A number is stored as a whole number with all its digits. An array or an object is refused, with nothing saved.
func normalizeSettingTypes(body map[string]any) string {
	for k, v := range body {
		switch x := v.(type) {
		case nil:
			body[k] = ""
		case float64:
			// A whole number, stored with all its digits: %v wrote 1e6 and over in exponent form, which the
			// artifact store's cap then read as 0, no cap (Security's #4431 SHOULD 2).
			if x != math.Trunc(x) || math.Abs(x) > 1<<53 {
				return k + " must be a whole number"
			}
			body[k] = strconv.FormatInt(int64(x), 10)
		case string, bool:
		default:
			return k + " must be a string, true or false, or a number"
		}
	}
	return ""
}

// settingWithItsOwnRoute refuses a setting that only its own route writes, naming that route.
func settingWithItsOwnRoute(body map[string]any) string {
	// The terminal backend is written only by its own route, which needs a person (#3634).
	for k := range body {
		if isTerminalBackendKey(k) {
			return "terminal_backend is set with Settings > Terminal backend (PUT /api/system/terminal-backend)"
		}
	}
	// The Hub-as-Operator opt-out has its own route, which logs every change (#3377, #3404).
	for _, k := range []string{HubOperatorDisabledSetting, legacyHubOperatorSetting} {
		if _, ok := body[k]; ok {
			return k + " is set with PUT /api/system/hub-operator"
		}
	}
	for k := range body {
		// Sign in with Coral Hub's settings have their own routes (#3966, Security condition 4):
		// only a local Operator may change them, and turning it off ends Hub sessions.
		if hubSigninSettingKey(k) {
			return k + " is set in Settings > Browser sign-in (PUT /api/auth/hub-signin or /api/auth/hub-origins)"
		}
		// The Coral Hub link's trust settings have their own routes, which a Coral Hub sign-in
		// can't reach (#3966 round 1): never through the generic PUT, for any caller.
		if route, ok := hubTrustSettingRoutes[strings.ToLower(strings.TrimSpace(k))]; ok {
			return k + " is set with " + route
		}
	}
	// The identity mode is the Operator's switch, written only by its own route (#4255).
	for k := range body {
		if strings.EqualFold(strings.TrimSpace(k), IdentityActorSetting) {
			return k + " is set with PUT /api/system/identity-actor"
		}
	}
	// Run as a hub has its own route, on this computer only (#3460).
	if _, ok := body[HubModeSetting]; ok {
		return HubModeSetting + " is set with PUT /api/system/hub-mode"
	}
	return ""
}

// settingRule is one setting's check: its normalised value, or false with msg the reason. v is a string, bool or
// whole-number string by now (normalizeSettingTypes).
type settingRule struct {
	key, msg string
	norm     func(v any) (string, bool)
}

// capSetting: a whole number of bytes, 0 for none (and, unless emptyOK, not empty).
func capSetting(emptyOK bool) func(v any) (string, bool) {
	return func(v any) (string, bool) {
		s, isString := v.(string)
		n, valid := parseStoreCap(s)
		if !isString || !valid || (!emptyOK && s == "") {
			return "", false
		}
		return strconv.FormatInt(n, 10), true
	}
}

// boolRule: a boolean, normalised to "true"/"false".
func boolRule(v any) (string, bool) {
	on, valid := validBoolSetting(v)
	return strconv.FormatBool(on), valid
}

// settingRulesBeforeRetention and settingRulesAfterRetention, in the order they are checked.
var (
	settingRulesBeforeRetention = []settingRule{
		// agent_priority is "low" or "normal" (#3437); anything else was stored and silently taken as low (#3470).
		{agentprio.SettingKey, agentprio.SettingKey + " must be \"" + agentprio.Low + "\" or \"" + agentprio.Normal + "\"", func(v any) (string, bool) {
			s, isString := v.(string)
			lvl, valid := agentprio.Parse(s)
			return lvl, isString && valid
		}},
		// The artifact store's cap.
		{ArtifactStoreMaxBytesSetting, ArtifactStoreMaxBytesSetting + " must be a whole number of bytes (0 for no cap)", capSetting(true)},
		// The cap on received files (#4441); and the notice's acknowledgement, the cap it was shown for.
		{ReceivedArtifactsMaxBytesSetting, ReceivedArtifactsMaxBytesSetting + " must be a whole number of bytes (0 for no cap)", capSetting(false)},
		{ReceivedCapNoticeAckSetting, ReceivedCapNoticeAckSetting + " must be a whole number of bytes", func(v any) (string, bool) {
			s, isString := v.(string)
			n, err := strconv.ParseInt(s, 10, 64)
			return s, isString && err == nil && n >= 0
		}},
	}
	settingRulesAfterRetention = []settingRule{
		{RetentionKindsSetting, RetentionKindsSetting + " must list diffs, objects, diff-objects or all", validRetentionKinds},
		// require_auth_on_loopback (#3585). This route is Operator-only and not reachable through the Coral Hub
		// (operatorOnlyRoutes), so the setting is too.
		{auth.RequireAuthOnLoopbackSetting, auth.RequireAuthOnLoopbackSetting + " must be true or false", boolRule},
		{background.GoalSettingKey, background.GoalSettingKey + " must be true or false", boolRule}, // auto_goals (#4204)
	}
)

// applySettingRules checks and normalises the body's settings that have a rule, in the rules' order.
func applySettingRules(body map[string]any, rules []settingRule) string {
	for _, rule := range rules {
		if v, ok := body[rule.key]; ok {
			s, valid := rule.norm(v)
			if !valid {
				return rule.msg
			}
			body[rule.key] = s
		}
	}
	return ""
}

// normalizeSettingValues checks and normalises the settings with a value rule, artifact retention among them:
// whole days from 0 (off) to 3650, and a list of kinds (#3554). Retention's bookkeeping keys are Coral's own; a
// changed number of days starts its wait now, so Settings can offer the review at once (an acknowledgement has to
// come after this moment to count).
func (h *SystemHandler) normalizeSettingValues(ctx context.Context, body map[string]any) string {
	if msg := applySettingRules(body, settingRulesBeforeRetention); msg != "" {
		return msg
	}
	// Retention's bookkeeping keys (retentionEnabledAtKey, retentionSeenDaysKey, RetentionAckAtKey, RetentionLastKey) are Coral's own and are
	// refused by checkSettingKeys before this runs, so a body that reaches here never holds one; this function only adds the first two below.
	if v, ok := body[RetentionDaysSetting]; ok {
		s, valid := validRetentionDays(v)
		if !valid {
			return RetentionDaysSetting + " must be a whole number of days from 0 (off) to 3650"
		}
		body[RetentionDaysSetting] = s
		if cur, _ := h.ss.GetSettings(ctx); cur == nil || setreg.Raw(cur, RetentionDaysSetting) != s {
			if s == "0" {
				body[retentionEnabledAtKey], body[retentionSeenDaysKey] = "", ""
			} else {
				body[retentionEnabledAtKey], body[retentionSeenDaysKey] = time.Now().UTC().Format(time.RFC3339), s
			}
		}
	}
	return applySettingRules(body, settingRulesAfterRetention)
}

// checkCLIPaths: cli_path_<type> (Settings > CLI paths) is the program Coral runs for that agent type: only a known
// type, and only an absolute path to an executable file with no shell metacharacters (#3364). A value that is
// unchanged is not re-checked, so a CLI that was uninstalled since doesn't block saving other settings.
func (h *SystemHandler) checkCLIPaths(ctx context.Context, body map[string]any) string {
	var current map[string]string
	for k, v := range body {
		agentType, isCLIPath := agent.IsCLIPathSetting(k)
		if !isCLIPath {
			continue
		}
		path, isString := v.(string)
		if !isString && v != nil {
			return k + " must be a string"
		}
		if !agent.KnownCLIAgentType(agentType) {
			return "unknown agent type in " + k
		}
		if current == nil {
			if current, _ = h.ss.GetSettings(ctx); current == nil {
				current = map[string]string{}
			}
		}
		path = strings.TrimSpace(path)
		if path == current[k] {
			continue
		}
		if err := agent.ValidateCLIPath(path); err != nil {
			return k + ": " + err.Error()
		}
		body[k] = path
	}
	return ""
}

// settingStrings is each setting as stored: a boolean as Python's str() wrote it ("True", "False"), anything else
// as its text.
func settingStrings(body map[string]any) map[string]string {
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
	return values
}

// saveSettings writes the settings in one transaction.
func (h *SystemHandler) saveSettings(ctx context.Context, values map[string]string) error {
	tx, err := h.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, s := range values {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO user_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
			k, s); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// applyLiveSettings applies the settings that take effect at once (new WebSocket attaches, agents started from now
// on, the next request) without a server restart.
func applyLiveSettings(values map[string]string) {
	for k, s := range values {
		switch k {
		case "terminal_replay_bytes":
			if n, err := strconv.Atoi(s); err == nil {
				ptymanager.SetReplayBytes(n)
			}
		case "telemetry_enabled":
			tracking.SetTelemetryEnabled(setreg.Bool(values, k))
		case agentprio.SettingKey:
			agentprio.SetSetting(s) // agents started from now on (#3437)
		case inputlog.SettingKey:
			inputlog.SetSetting(setreg.Bool(values, k))
		case auth.RequireAuthOnLoopbackSetting:
			auth.SetRequireAuthOnLoopback(setreg.Bool(values, k)) // #3585: takes effect for the next request
		}
	}
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
		errForbidden(w, "Delete the terminal input log from the computer running Coral.")
		return
	}
	n, err := inputlog.Delete()
	if errors.Is(err, inputlog.ErrOn) {
		errConflict(w, err.Error())
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
