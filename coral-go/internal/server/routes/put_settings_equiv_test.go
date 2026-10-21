package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/agentprio"
	"github.com/subgentic/coral/internal/auth"
	"github.com/subgentic/coral/internal/background"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/inputlog"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/internal/tracking"
)

// PutSettings was split for #4599 item 5 (ordered checks and a table of value rules in place of one long function).
// Security reviews it: the old handler is kept below, verbatim, and the two answer the same random requests on two
// databases that must stay equal.

func (h *SystemHandler) putSettingsBefore4599(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	// A setting is a string, a boolean or a number (Security's #4431 SHOULD 1). null is empty, as the
	// request-pool sizes already took it: it was stored as the text "<nil>", which as a CLI path broke
	// every launch. An array or an object is refused, with nothing saved.
	for k, v := range body {
		switch x := v.(type) {
		case nil:
			body[k] = ""
		case float64:
			// A whole number, stored with all its digits: %v wrote 1e6 and over in exponent form, which the
			// artifact store's cap then read as 0, no cap (Security's #4431 SHOULD 2).
			if x != math.Trunc(x) || math.Abs(x) > 1<<53 {
				errBadRequest(w, k+" must be a whole number")
				return
			}
			body[k] = strconv.FormatInt(int64(x), 10)
		case string, bool:
		default:
			errBadRequest(w, k+" must be a string, true or false, or a number")
			return
		}
	}
	// The terminal backend is written only by its own route, which needs a person (#3634).
	for k := range body {
		if isTerminalBackendKey(k) {
			errBadRequest(w, "terminal_backend is set with Settings > Terminal backend (PUT /api/system/terminal-backend)")
			return
		}
	}
	// cli_path_<type> (Settings > CLI paths) is the program Coral runs for that agent
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
	// The identity mode is the Operator's switch, written only by its own route (#4255).
	for k := range body {
		if strings.EqualFold(strings.TrimSpace(k), IdentityActorSetting) {
			errBadRequest(w, k+" is set with PUT /api/system/identity-actor")
			return
		}
	}
	// Run as a hub has its own route, on this computer only (#3460).
	if _, ok := body[HubModeSetting]; ok {
		errBadRequest(w, HubModeSetting+" is set with PUT /api/system/hub-mode")
		return
	}
	// Every key must be one the registry knows, with a value it accepts (Security's #4431 SHOULD 3).
	if err := checkSettingKeys(body); err != nil {
		errBadRequest(w, err.Error())
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
	// The artifact store's cap: a whole number of bytes, 0 for none.
	if v, ok := body[ArtifactStoreMaxBytesSetting]; ok {
		s, isString := v.(string) // a number is a string by now
		n, valid := parseStoreCap(s)
		if !isString || !valid {
			errBadRequest(w, ArtifactStoreMaxBytesSetting+" must be a whole number of bytes (0 for no cap)")
			return
		}
		body[ArtifactStoreMaxBytesSetting] = strconv.FormatInt(n, 10)
	}
	// The cap on received files (#4441): whole bytes, 0 for none; and the notice's acknowledgement, the cap it was shown for.
	if v, ok := body[ReceivedArtifactsMaxBytesSetting]; ok {
		s, isString := v.(string)
		n, valid := parseStoreCap(s)
		if !isString || !valid || s == "" {
			errBadRequest(w, ReceivedArtifactsMaxBytesSetting+" must be a whole number of bytes (0 for no cap)")
			return
		}
		body[ReceivedArtifactsMaxBytesSetting] = strconv.FormatInt(n, 10)
	}
	if v, ok := body[ReceivedCapNoticeAckSetting]; ok {
		s, isString := v.(string)
		if n, err := strconv.ParseInt(s, 10, 64); !isString || err != nil || n < 0 {
			errBadRequest(w, ReceivedCapNoticeAckSetting+" must be a whole number of bytes")
			return
		}
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
	// auto_goals (#4204): a boolean, normalised to "true"/"false".
	if v, ok := body[background.GoalSettingKey]; ok {
		on, valid := validBoolSetting(v)
		if !valid {
			errBadRequest(w, background.GoalSettingKey+" must be true or false")
			return
		}
		body[background.GoalSettingKey] = strconv.FormatBool(on)
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

// putSettingsLiveKeys are applied to process-wide state on a successful PUT with no way to read it back; they stay out
// of the random requests so this test leaves nothing behind for the package's other tests (neither has a value rule of
// its own in PutSettings). The other live settings are in the requests and put back afterwards.
var putSettingsLiveKeys = map[string]bool{"telemetry_enabled": true, inputlog.SettingKey: true}

var rfc3339Like = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$`)

func TestPutSettingsAgreesWithBefore4599(t *testing.T) {
	// serial: 30000 random PUTs set process-wide state (auth.RequireAuthOnLoopback, agent priority, replay bytes), which every parallel test would see (#5667)
	prio, loopback, replay := agentprio.Level(), auth.RequireAuthOnLoopback(), ptymanager.ReplayBytes()
	t.Cleanup(func() {
		agentprio.SetSetting(prio)
		auth.SetRequireAuthOnLoopback(loopback)
		ptymanager.SetReplayBytes(replay)
	})
	newH := func() (*SystemHandler, func() map[string]string) {
		db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
		require.NoError(t, err)
		t.Cleanup(func() { db.Close() })
		h := NewSystemHandler(db, &config.Config{LogDir: t.TempDir()})
		dump := func() map[string]string {
			rows, err := db.Reader().QueryxContext(context.Background(), "SELECT key, value FROM user_settings")
			require.NoError(t, err)
			defer rows.Close()
			out := map[string]string{}
			for rows.Next() {
				var k, v string
				require.NoError(t, rows.Scan(&k, &v))
				if rfc3339Like.MatchString(v) {
					v = "<time>" // retention's wait starts at the request's second
				}
				out[k] = v
			}
			return out
		}
		return h, dump
	}
	hNew, dumpNew := newH()
	hOld, dumpOld := newH()
	put := func(handler http.HandlerFunc, body []byte) (int, string) {
		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(body)))
		return rec.Code, rec.Body.String()
	}

	var keys []string
	for k := range settingRegistry {
		if !putSettingsLiveKeys[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	keys = append(keys, "terminal_backend", HubOperatorDisabledSetting, legacyHubOperatorSetting, "hub_signin_enabled",
		MaxModeSetting, AllowHubAutoAcceptSetting, RedactTokensSetting, IdentityActorSetting, HubModeSetting,
		retentionEnabledAtKey, retentionSeenDaysKey, RetentionAckAtKey, RetentionLastKey, "cli_path_claude", "cli_path_codex",
		"cli_path_nope", "not_a_setting")
	cli := filepath.Join(t.TempDir(), "my-cli")
	require.NoError(t, writeExecutable(cli))
	values := []any{"", "0", "1", "7", "50", "51", "3650", "3651", "-1", "100000", "1048576", "abc", "true", "false", "True",
		"low", "normal", "all", "diffs", "objects,diffs", "diff-objects", "nope", cli, "relative/x", "/bin/sh; rm -rf /",
		true, false, float64(0), float64(30), float64(1e7), 1.5, -3.0, math.Pow(2, 60), nil, []any{"a"}, map[string]any{"a": 1}}
	rng := rand.New(rand.NewSource(4599))
	codes := map[int]int{}
	n := 30000
	if testing.Short() {
		n = 3000
	}
	for i := 0; i < n; i++ {
		body := map[string]any{}
		nkeys := 1 // mostly one key: its reply is compared too
		if rng.Intn(10) >= 7 {
			nkeys = 2 + rng.Intn(2)
		}
		for k := nkeys; k > 0; k-- {
			body[keys[rng.Intn(len(keys))]] = values[rng.Intn(len(values))]
		}
		b, err := json.Marshal(body)
		require.NoError(t, err)
		codeNew, replyNew := put(hNew.PutSettings, b)
		codeOld, replyOld := put(hOld.putSettingsBefore4599, b)
		require.Equal(t, codeOld, codeNew, "request %d %s: status", i, b)
		codes[codeNew]++
		if len(body) == 1 { // with more keys, which failing key is named depends on map order, in both
			require.Equal(t, replyOld, replyNew, "request %d %s: reply", i, b)
		}
		require.Equal(t, dumpOld(), dumpNew(), "request %d %s: stored settings", i, b)
	}
	t.Logf("%d requests, by status: %v; %d settings stored", n, codes, len(dumpNew()))
	require.Greater(t, codes[http.StatusOK], n/20, "enough requests are saved for the stored state to be compared")
}

func writeExecutable(path string) error { return os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755) }
