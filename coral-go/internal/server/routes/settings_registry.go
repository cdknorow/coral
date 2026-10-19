package routes

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/agentmodels"
	"github.com/cdknorow/coral/internal/agentprio"
	"github.com/cdknorow/coral/internal/auth"
	"github.com/cdknorow/coral/internal/background"
	"github.com/cdknorow/coral/internal/inputlog"
	"github.com/cdknorow/coral/internal/store"
)

// The settings registry (#4432, Security's #4431 SHOULD 3): every key PUT /api/settings knows, who may write it and
// what its value may be. A key it doesn't list, or one in another case, is refused, so a typo or a key a newer
// dashboard invented can't be stored where nothing reads it. The whole route is never reachable through Coral Hub
// (the tunnel's table) and refuses agent tokens, so there is no Hub-only class here: every generic key is the local
// Operator's.

// settingClass says who writes a setting.
type settingClass int

const (
	settingGeneric  settingClass = iota // PUT /api/settings
	settingOwnRoute                     // only its own route; the generic PUT refuses it (see PutSettings' messages)
	settingCoral                        // Coral's own bookkeeping; no client writes it
)

// Size caps (INFO 3): 256 KiB for a value, 2 MiB for the JSON list settings.
const (
	settingMaxLen     = 256 << 10
	settingListMaxLen = 2 << 20
)

// settingSpec is one key's entry. check, when set, validates a generic key's value (a string, a bool, or a whole
// number as a string by then) and returns what is stored; nil leaves it as it is (PutSettings' own validators, which
// run after the registry, check those keys).
type settingSpec struct {
	class  settingClass
	check  func(v any) (any, error)
	maxLen int
}

func generic(check func(v any) (any, error)) settingSpec {
	return settingSpec{class: settingGeneric, check: check}
}

// boolSetting is true or false: a JSON boolean, or the words config.Truthy reads (stored as sent).
func boolSetting(v any) (any, error) {
	if _, ok := validBoolSetting(v); !ok {
		return nil, errors.New("must be true or false")
	}
	return v, nil
}

// textSetting is any text (the size cap and the NUL check apply to every key).
func textSetting(v any) (any, error) {
	if _, ok := v.(string); !ok {
		return nil, errors.New("must be text")
	}
	return v, nil
}

// enumSetting is one of values, or empty for the default.
func enumSetting(values ...string) func(any) (any, error) {
	return func(v any) (any, error) {
		s, ok := v.(string)
		if ok && s == "" {
			return v, nil
		}
		for _, want := range values {
			if ok && s == want {
				return v, nil
			}
		}
		return nil, fmt.Errorf("must be one of %s", strings.Join(values, ", "))
	}
}

// intSetting is a whole number from lo to hi, or empty for the default; clamp brings one outside the range into it
// instead of refusing it.
func intSetting(lo, hi int64, clamp bool) func(any) (any, error) {
	return func(v any) (any, error) {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("must be a whole number from %d to %d", lo, hi)
		}
		if strings.TrimSpace(s) == "" {
			return "", nil
		}
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		switch {
		case err != nil:
			return nil, fmt.Errorf("must be a whole number from %d to %d", lo, hi)
		case clamp:
			n = max(lo, min(hi, n))
		case n < lo || n > hi:
			return nil, fmt.Errorf("must be a whole number from %d to %d", lo, hi)
		}
		return strconv.FormatInt(n, 10), nil
	}
}

// listSetting is a JSON list kept as text (the dashboard's saved personas, templates, macros and list orders).
func listSetting(v any) (any, error) {
	s, ok := v.(string)
	if !ok {
		return nil, errors.New("must be a JSON list, as text")
	}
	if strings.TrimSpace(s) == "" {
		return v, nil
	}
	var list []json.RawMessage
	if json.Unmarshal([]byte(s), &list) != nil {
		return nil, errors.New("must be a JSON list, as text")
	}
	return v, nil
}

// agentTypes are the agent types a default or a per-type model may name.
var agentTypes = []string{"claude", "codex", "agy", "antigravity", "gemini", "pi", "terminal"}

var settingRegistry = map[string]settingSpec{
	// Settings > General and the terminal.
	"theme":                   generic(enumSetting("dark", "light", "system")),
	"custom_theme":            generic(textSetting),
	"default_renderer":        generic(textSetting),
	"default_agent_type":      generic(enumSetting(agentTypes...)),
	"default_permission_mode": generic(enumSetting("default", "plan", "acceptEdits", "auto", "bypassPermissions", "dontAsk")),
	"default_working_dir":     generic(textSetting),
	"fit_pane_width":          generic(boolSetting),
	"notify_needs_input":      generic(boolSetting),
	"show_scrollbars":         generic(boolSetting),
	RedactKeywordsSetting:     generic(boolSetting),
	"terminal_font_size":      generic(intSetting(6, 72, false)),
	"terminal_scrollback":     generic(intSetting(0, 1_000_000, false)),
	// The startup reader's range (4 KiB to 16 MiB); the live value was never clamped (INFO 2).
	"terminal_replay_bytes":           generic(intSetting(4<<10, 16<<20, true)),
	"file_search_mode":                generic(enumSetting("directory", "fuzzy")),
	"file_search_limit":               generic(intSetting(1, 100_000, false)),
	"git_diff_mode":                   generic(enumSetting("branch_point", "previous_commit", "main_head")),
	"git_poll_interval_s":             generic(intSetting(0, 86_400, false)),
	agentprio.SettingKey:              generic(nil),
	AgentLaunchLimitSetting:           generic(intSetting(0, 100_000, false)), // the live agents an agent token may add (#4602); 0 is none
	"telemetry_enabled":               generic(boolSetting),
	"board_auto_attach":               generic(boolSetting),
	inputlog.SettingKey:               generic(boolSetting),
	"remote_access_enabled":           generic(boolSetting),
	"refresh_files_on_switch":         generic(boolSetting),
	"show_session_server":             generic(boolSetting),
	"show_session_summary":            generic(boolSetting),
	"board_health_monitor":            generic(boolSetting),
	"auto_summarize":                  generic(boolSetting),
	"gate_signals_claude":             generic(boolSetting),
	TurnEndHandoffSetting:             generic(enumSetting("off", "on")),
	background.GoalSettingKey:         generic(nil),
	AutoResumeSetting:                 generic(nil),
	AutoWakeSetting:                   generic(nil),
	AutoWakeCapSetting:                generic(nil),
	auth.RequireAuthOnLoopbackSetting: generic(nil),
	// The first-launch screen (#3221).
	"welcome_choice":       generic(enumSetting("skipped", "connected", "own-hub")),
	"welcome_pending":      generic(boolSetting),
	"home_editions_hidden": generic(boolSetting),
	// Storage: artifacts, uploads, call metrics, knowledge.
	ArtifactStoreMaxBytesSetting:          generic(nil),
	ReceivedArtifactsMaxBytesSetting:      generic(nil),
	ReceivedCapNoticeAckSetting:           generic(nil),
	RetentionDaysSetting:                  generic(nil),
	RetentionKindsSetting:                 generic(nil),
	"uploads_retention_days":              generic(intSetting(1, 365, false)),
	"uploads_max_mb":                      generic(intSetting(50, 102_400, false)),
	store.CallMetricsRetentionSetting:     generic(intSetting(1, 90, false)),
	background.KnowledgeTimeoutSettingKey: generic(intSetting(1, 86_400, false)),
	// Coral Hub request pools (#3642).
	"hub_limit_global": generic(nil),
	"hub_limit_long":   generic(nil),
	"hub_limit_search": generic(nil),
	// Prompts.
	"default_prompt_orchestrator": generic(textSetting),
	"default_prompt_worker":       generic(textSetting),
	"team_reminder_orchestrator":  generic(textSetting),
	"team_reminder_worker":        generic(textSetting),
	// JSON lists kept as text.
	"macros":               {class: settingGeneric, check: listSetting, maxLen: settingListMaxLen},
	"saved_personas":       {class: settingGeneric, check: listSetting, maxLen: settingListMaxLen},
	"saved_team_templates": {class: settingGeneric, check: listSetting, maxLen: settingListMaxLen},
	"session_order":        {class: settingGeneric, check: listSetting, maxLen: settingListMaxLen},
	"group_order":          {class: settingGeneric, check: listSetting, maxLen: settingListMaxLen},

	// Set only with their own routes.
	TerminalBackendSetting:       {class: settingOwnRoute},
	HubOperatorDisabledSetting:   {class: settingOwnRoute},
	legacyHubOperatorSetting:     {class: settingOwnRoute},
	HubModeSetting:               {class: settingOwnRoute},
	IdentityActorSetting:         {class: settingOwnRoute},
	MaxModeSetting:               {class: settingOwnRoute},
	AllowHubAutoAcceptSetting:    {class: settingOwnRoute},
	RedactTokensSetting:          {class: settingOwnRoute},
	"hub_signin_enabled":         {class: settingOwnRoute},
	"hub_signin_origins":         {class: settingOwnRoute},
	"hub_signin_offer_dismissed": {class: settingOwnRoute},
	RetentionAckAtKey:            {class: settingOwnRoute},

	// Coral's own.
	retentionEnabledAtKey:          {class: settingCoral},
	ArtifactStoreAutoRaisedSetting: {class: settingCoral},
	retentionSeenDaysKey:           {class: settingCoral},
	RetentionLastKey:               {class: settingCoral},
	"cloud_bootstrap_status":       {class: settingCoral},
	"demo_workflows_seeded":        {class: settingCoral},
	"self_test":                    {class: settingCoral},
}

// lookupSetting is key's entry: an exact key of the registry, or cli_path_<type> and default_model_<type> for a
// known type.
func lookupSetting(key string) (settingSpec, bool) {
	if spec, ok := settingRegistry[key]; ok {
		return spec, true
	}
	if t, ok := agent.IsCLIPathSetting(key); ok && key == strings.ToLower(key) {
		return settingSpec{class: settingGeneric}, agent.KnownCLIAgentType(t) // PutSettings checks the path
	}
	if t, ok := strings.CutPrefix(key, "default_model_"); ok {
		for _, known := range agentTypes {
			if t == known {
				return generic(textSetting), true
			}
		}
	}
	if t, ok := strings.CutPrefix(key, "default_thinking_"); ok {
		for _, known := range agentTypes {
			if t == known {
				return generic(thinkingSetting(t)), true
			}
		}
	}
	return settingSpec{}, false
}

// checkSettingKeys applies the registry to a PUT's body: a key it doesn't know, or Coral's own, is refused; a generic
// key's value is checked (no NUL, at most its size) and normalised. PutSettings refuses the own-route keys with the
// route to use before this runs; one that gets here is refused too.
func checkSettingKeys(body map[string]any) error {
	for k, v := range body {
		spec, ok := lookupSetting(k)
		if !ok {
			if _, isCLIPath := agent.IsCLIPathSetting(k); isCLIPath {
				return errors.New("unknown agent type in " + k)
			}
			return fmt.Errorf("%q is not a Coral setting", k)
		}
		switch spec.class {
		case settingCoral:
			return fmt.Errorf("%s is set by Coral", k)
		case settingOwnRoute:
			return fmt.Errorf("%s is set with its own route, not PUT /api/settings", k)
		}
		if s, isString := v.(string); isString {
			limit := spec.maxLen
			if limit == 0 {
				limit = settingMaxLen
			}
			if len(s) > limit {
				return fmt.Errorf("%s is too long (at most %d KiB)", k, limit>>10)
			}
			if strings.ContainsRune(s, 0) {
				return fmt.Errorf("%s must not contain a NUL character", k)
			}
		}
		if spec.check != nil {
			out, err := spec.check(v)
			if err != nil {
				return fmt.Errorf("%s %v", k, err)
			}
			body[k] = out
		}
	}
	return nil
}

// thinkingSetting checks a default_thinking_<type> value (#4969): "" (the CLI's own default) or one of the levels Coral passes to that
// type's CLI.
func thinkingSetting(agentType string) func(v any) (any, error) {
	return func(v any) (any, error) {
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("must be a string")
		}
		s = strings.TrimSpace(s)
		if !agentmodels.ValidThinking(agentType, s) {
			return nil, errors.New(errInvalidThinking(agentType))
		}
		return s, nil
	}
}
