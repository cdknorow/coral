package routes

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settingRows is, for every generic key the registry checks itself, a value it accepts and one it refuses (raw JSON;
// a number is text by the time it is checked, so a text key's refused value is a boolean or a list).
// Keys PutSettings' own validators check (check nil) have their own tests; they are listed in validatedElsewhere.
var settingRows = map[string]struct{ good, bad string }{
	"theme":                       {`"light"`, `"neon"`},
	"custom_theme":                {`"Mine"`, `true`},
	"default_renderer":            {`"xterm"`, `true`},
	"default_agent_type":          {`"codex"`, `"bash"`},
	"default_permission_mode":     {`"plan"`, `"yolo"`},
	"default_working_dir":         {`"/tmp/w"`, `false`},
	"fit_pane_width":              {`true`, `"maybe"`},
	"notify_needs_input":          {`false`, `"maybe"`},
	"show_scrollbars":             {`"True"`, `"maybe"`},
	RedactKeywordsSetting:         {`false`, `"maybe"`},
	"terminal_font_size":          {`"14"`, `"200"`},
	"terminal_scrollback":         {`20000`, `-1`},
	"terminal_replay_bytes":       {`262144`, `"lots"`},
	"file_search_mode":            {`"fuzzy"`, `"grep"`},
	"file_search_limit":           {`500`, `0`},
	"git_diff_mode":               {`"main_head"`, `"HEAD~2"`},
	"git_poll_interval_s":         {`"60"`, `"abc"`},
	"telemetry_enabled":           {`"false"`, `"nope"`},
	"board_auto_attach":           {`true`, `"nope"`},
	"cross_team_allowlist":        {`true`, `"nope"`},
	"debug_input_log":             {`false`, `"nope"`},
	"remote_access_enabled":       {`"true"`, `"nope"`},
	"refresh_files_on_switch":     {`true`, `"nope"`},
	"show_session_server":         {`"False"`, `"nope"`},
	"show_session_summary":        {`true`, `"nope"`},
	"board_health_monitor":        {`"true"`, `"nope"`},
	"artifact_comments_hub_write": {`"true"`, `"nope"`},
	"auto_summarize":              {`"1"`, `"nope"`},
	"gate_signals_claude":         {`"off"`, `"nope"`},
	"nudge_claude_artifacts":      {`"off"`, `"nope"`},
	"turn_end_handoff":            {`"off"`, `"later"`},
	"update_channel":              {`"agentic"`, `"nightly"`},
	"welcome_choice":              {`"connected"`, `"maybe"`},
	"welcome_pending":             {`"False"`, `"nope"`},
	"home_editions_hidden":        {`"True"`, `"nope"`},
	"uploads_retention_days":      {`30`, `400`},
	"uploads_max_mb":              {`"500"`, `10`},
	"call_metrics_retention_days": {`14`, `91`},
	"knowledge_timeout_s":         {`120`, `0`},
	"agent_launch_limit":          {`"16"`, `-1`},
	"default_prompt_orchestrator": {`"Lead the team."`, `["x"]`},
	"default_prompt_worker":       {`""`, `{"a":1}`},
	"team_reminder_orchestrator":  {`"Check the board."`, `false`},
	"team_reminder_worker":        {`"Post when done."`, `true`},
	"macros":                      {`"[{\"label\":\"x\",\"command\":\"ls\"}]"`, `"not a list"`},
	"saved_personas":              {`"[]"`, `"{}"`},
	"saved_team_templates":        {`"[]"`, `"{}"`},
	"session_order":               {`"[\"a\",\"b\"]"`, `"a,b"`},
	"group_order":                 {`"[]"`, `"x"`},
}

var validatedElsewhere = []string{"agent_priority", "auto_goals", "auto_resume_after_api_error", "auto_wake_after_restart", "auto_wake_cap",
	"require_auth_on_loopback", "artifact_store_max_bytes", "artifact_retention_days", "artifact_retention_kinds",
	"received_artifacts_max_bytes", "received_cap_notice_ack",
	"hub_limit_global", "hub_limit_long", "hub_limit_search"}

// #4432 (Security's #4431 SHOULD 3, INFO 2 and 3): every key PUT /api/settings knows has a row here, a value it
// accepts is stored, one it refuses is a 400 that saves nothing of the request; unknown keys, keys in another case,
// own-route and Coral's own keys are refused; values over the size cap or with a NUL are refused.
func TestSettingsRegistry(t *testing.T) {
	put, stored := settingsClient(t)
	others := map[string]bool{}
	for _, k := range validatedElsewhere {
		others[k] = true
	}
	for k, spec := range settingRegistry {
		_, row := settingRows[k]
		switch {
		case spec.class != settingGeneric:
		case spec.check == nil:
			assert.True(t, others[k], "%s: checked by PutSettings, list it in validatedElsewhere", k)
		default:
			assert.True(t, row, "%s: the registry checks it, add a row", k)
		}
	}

	for k, row := range settingRows {
		require.Equal(t, http.StatusOK, put(`{"default_working_dir": "/tmp/base", "`+k+`": `+row.good+`}`), "%s good", k)
		before := stored()[k]
		assert.Equal(t, http.StatusBadRequest, put(`{"default_working_dir": "/tmp/sentinel", "`+k+`": `+row.bad+`}`), "%s bad %s", k, row.bad)
		assert.Equal(t, before, stored()[k], "%s: a refused value changes nothing", k)
		assert.NotEqual(t, "/tmp/sentinel", stored()["default_working_dir"], "%s: nothing of a refused request is saved", k)
	}

	// The terminal replay limit is brought into its range, not refused.
	require.Equal(t, http.StatusOK, put(`{"terminal_replay_bytes": 1}`))
	assert.Equal(t, "4096", stored()["terminal_replay_bytes"])
	require.Equal(t, http.StatusOK, put(`{"terminal_replay_bytes": 1073741824}`))
	assert.Equal(t, "16777216", stored()["terminal_replay_bytes"])

	// Per-type keys: a known type only.
	require.Equal(t, http.StatusOK, put(`{"default_model_claude": "opus", "default_model_terminal": ""}`))
	assert.Equal(t, http.StatusBadRequest, put(`{"default_model_bash": "x"}`))

	for _, raw := range []string{
		`{"no_such_setting": "1"}`, `{"Theme": "dark"}`, `{"THEME": "dark"}`, `{"cli_path_Claude": "/bin/sh"}`, `{"Default_Model_claude": "x"}`,
		`{"cloud_bootstrap_status": "{}"}`, `{"demo_workflows_seeded": "1"}`, `{"self_test": "ok"}`, `{"artifact_retention_last": "{}"}`,
		`{"terminal_backend": "pty"}`, `{"cloud_max_mode": "launch"}`, `{"hub_redact_tokens": "false"}`,
		`{"default_prompt_worker": "a\u0000b"}`,
		`{"default_prompt_worker": "` + strings.Repeat("x", settingMaxLen+1) + `"}`,
	} {
		short := raw
		if len(short) > 60 {
			short = short[:60]
		}
		assert.Equal(t, http.StatusBadRequest, put(raw), short)
	}
	// A list may be larger than a plain value, up to its own cap (2 MiB), and no larger.
	listBody := func(n int) string {
		list, _ := json.Marshal(strings.Split(strings.Repeat("xxxxxxxxxx,", n)+"x", ","))
		body, _ := json.Marshal(map[string]string{"saved_personas": string(list)})
		return string(body)
	}
	assert.Equal(t, http.StatusOK, put(listBody(settingMaxLen/12)), "over 256 KiB")
	assert.Equal(t, http.StatusBadRequest, put(listBody(settingListMaxLen/12)), "over 2 MiB")
}

// The dashboard's Settings payload and the Coral Hub's settings list name only keys the registry knows: a key the
// dashboard sends that the registry missed would make its Save fail.
func TestDashboardSettingKeysAreRegistered(t *testing.T) {
	static := filepath.Join("..", "..", "..", "pkg", "frontend", "static")
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(static, name))
		require.NoError(t, err)
		return string(b)
	}
	modals := read("settings_dialog.js")
	start := strings.Index(modals, "let payload = {")
	require.Positive(t, start, "the Settings dialog's payload")
	end := strings.Index(modals[start:], "};")
	keys := regexp.MustCompile(`(?m)^\s+([a-z_]+):`).FindAllStringSubmatch(modals[start:start+end], -1)
	require.NotEmpty(t, keys)
	hub := read("cloud_settings.js")
	hs := strings.Index(hub, "HUB_SETTING_KEYS = new Set([")
	require.Positive(t, hs)
	he := strings.Index(hub[hs:], "]);")
	keys = append(keys, regexp.MustCompile(`'([a-z_]+)'`).FindAllStringSubmatch(hub[hs:hs+he], -1)...)
	for _, m := range keys {
		_, ok := lookupSetting(m[1])
		assert.True(t, ok, "%s is sent by the dashboard but not in the registry", m[1])
	}
}
