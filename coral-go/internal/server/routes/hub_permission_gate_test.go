package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// hubGateCase is one route's request that asks to skip permission prompts, and the same request
// with choices that keep them.
type hubGateCase struct {
	name        string
	skips, safe func(sid string) any
	restart     bool // the request restarts an agent the test registers first
}

var hubGateCases = []hubGateCase{
	{name: "launch: Codex -a never",
		skips: func(string) any { return map[string]any{"agent_type": "codex", "flags": []string{"-a", "never"}} },
		safe: func(string) any {
			return map[string]any{"agent_type": "codex", "flags": []string{"--model", "gpt-5", "--sandbox", "read-only"}}
		}},
	{name: "launch: Claude acceptEdits",
		skips: func(string) any {
			return map[string]any{"agent_type": "claude", "flags": []string{"--permission-mode=acceptEdits"}}
		},
		safe: func(string) any {
			return map[string]any{"agent_type": "claude", "flags": []string{"--permission-mode", "plan"}}
		}},
	{name: "launch: an unknown flag",
		skips: func(string) any {
			return map[string]any{"agent_type": "claude", "flags": []string{"--settings", "s.json"}}
		},
		safe: func(string) any { return map[string]any{"agent_type": "claude"} }},
	{name: "launch: capability allow rules",
		skips: func(string) any {
			return map[string]any{"agent_type": "claude", "capabilities": map[string]any{"allow": []string{"Bash(*)"}}}
		},
		safe: func(string) any {
			return map[string]any{"agent_type": "claude", "capabilities": map[string]any{"deny": []string{"Bash(rm *)"}}}
		}},
	{name: "launch: allowed tools",
		skips: func(string) any { return map[string]any{"agent_type": "claude", "tools": []string{"Bash"}} },
		safe:  func(string) any { return map[string]any{"agent_type": "claude", "tools": []string{}} }},
	{name: "launch: custom hooks",
		skips: func(string) any {
			return map[string]any{"agent_type": "claude", "hooks": map[string]any{"PreToolUse": "x"}}
		},
		safe: func(string) any { return map[string]any{"agent_type": "claude", "hooks": map[string]any{}} }},
	{name: "launch-team: one member's Gemini yolo",
		skips: func(string) any { return team("t1", "", nil, []string{"--approval-mode", "yolo"}) },
		safe:  func(string) any { return team("t1", "", nil, []string{"--approval-mode", "plan"}) }},
	{name: "launch-team: the team's flags",
		skips: func(string) any { return team("t2", "claude", []string{"--dangerously-skip-permissions"}, nil) },
		safe:  func(string) any { return team("t2", "claude", []string{"--permission-mode", "plan"}, nil) }},
	{name: "restart: extra flags", restart: true,
		skips: func(sid string) any {
			return map[string]any{"session_id": sid, "agent_type": "claude", "extra_flags": "--allowedTools Bash(*)"}
		},
		safe: func(sid string) any {
			return map[string]any{"session_id": sid, "agent_type": "claude", "extra_flags": "--effort high"}
		}},
	{name: "restart: capabilities", restart: true,
		skips: func(sid string) any {
			return map[string]any{"session_id": sid, "agent_type": "claude", "capabilities": map[string]any{"allow": []string{"Bash(*)"}}}
		},
		safe: func(sid string) any { return map[string]any{"session_id": sid, "agent_type": "claude"} }},
}

// team is a launch-team body: a Claude member with no flags of its own (it takes the team's) and a
// Gemini member with geminiFlags.
func team(board, agentType string, teamFlags, geminiFlags []string) map[string]any {
	gemini := map[string]any{"name": "B", "agent_type": "gemini"}
	if geminiFlags != nil {
		gemini["flags"] = geminiFlags
	}
	return map[string]any{"board_name": board, "new_board": true, "agent_type": agentType, "flags": teamFlags,
		"agents": []map[string]any{{"name": "A", "agent_type": "claude"}, gemini}}
}

// #126 F4: through the Coral Hub, launch, launch-team and restart start an agent that skips
// permission prompts (its flags, capability allow rules, allowed tools, custom hooks) only with the
// Hub's provenance, the owner's local switch "Coral Hub can turn on auto-accept" and the Launch
// mode, as tasks/run (TestTasksRunAutoAcceptThroughTheHub). Refused, nothing starts. The same
// request from this computer, and a Hub request whose choices keep the prompts, are unchanged.
func TestHubLaunchesThatSkipPermissionPrompts(t *testing.T) {
	for _, tc := range hubGateCases {
		t.Run(tc.name, func(t *testing.T) {
			_, h, term, ss := setupSessionsTestServer(t)
			stubCLIPaths(t, ss)
			ctx := context.Background()
			set := func(mode, allow string) {
				require.NoError(t, ss.SetSetting(ctx, MaxModeSetting, mode))
				require.NoError(t, ss.SetSetting(ctx, AllowHubAutoAcceptSetting, allow))
			}
			n := 0
			call := func(how string, body func(string) any) *httptest.ResponseRecorder {
				n++
				path, handle := "/api/sessions/launch", h.Launch
				sid := ""
				var b []byte
				if tc.restart {
					sid = "hub-gate-restart-" + string(rune('a'+n))
					name := "claude-" + sid
					require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "coral-go", WorkingDir: t.TempDir()}))
					term.addSession(name, t.TempDir())
					path, handle = "/api/sessions/live/"+name+"/restart", h.Restart
					b, _ = json.Marshal(body(sid))
				} else {
					m := body("").(map[string]any)
					m["working_dir"] = t.TempDir()
					if _, isTeam := m["agents"]; isTeam {
						path, handle = "/api/sessions/launch-team", h.LaunchTeam
					}
					b, _ = json.Marshal(m)
				}
				req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
				if tc.restart {
					req = dropAt2(req, map[string]string{"name": "claude-" + sid})
				}
				switch how {
				case "hub":
					stamp(req)
				case "forged":
					req.Header.Set(tunnel.ViaHeader, "forged")
				}
				rec := httptest.NewRecorder()
				handle(rec, req)
				return rec
			}
			refused := func(rec *httptest.ResponseRecorder, code, why string) {
				t.Helper()
				assert.Equal(t, http.StatusForbidden, rec.Code, "%s: %s", why, rec.Body.String())
				assert.Equal(t, code, codeOf(t, rec), why)
			}
			passed := func(rec *httptest.ResponseRecorder, why string) {
				t.Helper()
				assert.Equal(t, http.StatusOK, rec.Code, "%s: %s", why, rec.Body.String())
			}

			set("launch", "false")
			before := len(term.sentCommands())
			rec := call("hub", tc.skips)
			refused(rec, "hub_auto_accept_disabled", "the owner's switch is off")
			var refusal struct {
				Error string `json:"error"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &refusal))
			assert.Contains(t, refusal.Error, "Coral Hub can turn on auto-accept", "the refusal says where to turn it on")
			assert.Contains(t, refusal.Error, "Settings > Network and Hub", "the refusal says where to turn it on")
			assert.Equal(t, before, len(term.sentCommands()), "nothing started")
			passed(call("local", tc.skips), "from this computer: unchanged")
			passed(call("hub", tc.safe), "choices that keep the prompts need no switch")

			set("launch", "true")
			passed(call("hub", tc.skips), "the switch on, Launch")
			refused(call("forged", tc.skips), "no_dashboard_provenance", "a forged stamp")
			set("interact", "true")
			refused(call("hub", tc.skips), "hub_mode_too_low", "below Launch")
		})
	}
}

// The refusal names the switch, the mode and the button as the dashboard labels them, so the
// owner can follow it.
func TestHubAutoAcceptOffSaysWhereToTurnItOn(t *testing.T) {
	for file, labels := range map[string][]string{
		"../../../pkg/frontend/templates/includes/modals.html": {"Network and Hub", "Coral Hub can turn on auto-accept"},
		"../../../pkg/frontend/static/editions.js":             {"Coral Hub can turn on auto-accept"},
	} {
		b, err := os.ReadFile(file)
		require.NoError(t, err)
		for _, label := range labels {
			assert.Contains(t, string(b), label, file)
			if label == "Network and Hub" {
				assert.Contains(t, hubAutoAcceptOff, "Settings > "+label)
				continue
			}
			assert.Contains(t, hubAutoAcceptOff, `"`+label+`"`)
		}
	}
}

// The switch's default is TestHubAutoAcceptDefaultsOnAndAnExplicitOffStaysOff (#5840, the Operator's decision).

// #126 F4 (the Orchestrator's decision with the Security Reviewer's conditions): through the Coral Hub, with the switch off,
// a team whose every member is exactly an agent of a team template the owner saved launches with its allow rules, tools and
// hooks: every field matches, the knowledge text included, read from this computer's settings. One member that differs in
// one field (its prompt, one more allow rule, one more tool, its knowledge), a built-in template the owner never saved, or
// flags that skip prompts refuse the whole team with the same 403. A forged stamp is still refused as such.
func TestHubTeamLaunchOfTheOwnersSavedTemplate(t *testing.T) {
	saved := `[{"name":"My team","agents":[` +
		`{"name":"Dev","prompt":"build it","agent_type":"claude","capabilities":{"allow":["file_read","file_write","shell"]},"tools":["Bash"],"knowledge":"notes"},` +
		`{"name":"Reviewer","prompt":"review it","agent_type":"claude","capabilities":{"allow":["file_read"]}}]}]`
	dev := func() map[string]any {
		return map[string]any{"name": "Dev", "prompt": "build it", "agent_type": "claude",
			"capabilities": map[string]any{"allow": []string{"file_read", "file_write", "shell"}}, "tools": []string{"Bash"}, "knowledge": "notes"}
	}
	reviewer := map[string]any{"name": "Reviewer", "prompt": "review it", "agent_type": "claude", "capabilities": map[string]any{"allow": []string{"file_read"}}}
	builtin := map[string]any{"name": "Lead Developer", "agent_type": "claude", // the built-in Coding Team's member, as the dashboard sends it
		"prompt":       "You are the lead developer. Implement features, write code, and coordinate with the team via the message board. Wait for instructions from the Orchestrator before starting.",
		"capabilities": map[string]any{"allow": []string{"file_read", "file_write", "shell", "git_write", "agent_spawn", "web_access"}}}

	_, h, _, ss := setupSessionsTestServer(t)
	stubCLIPaths(t, ss)
	ctx := context.Background()
	require.NoError(t, ss.SetSetting(ctx, MaxModeSetting, "launch"))
	require.NoError(t, ss.SetSetting(ctx, AllowHubAutoAcceptSetting, "false"))
	require.NoError(t, ss.SetSetting(ctx, "saved_team_templates", saved))
	n := 0
	post := func(how string, flags []string, members ...map[string]any) *httptest.ResponseRecorder {
		n++
		b, _ := json.Marshal(map[string]any{"board_name": "saved-" + string(rune('a'+n)), "new_board": true, "working_dir": t.TempDir(), "flags": flags, "agents": members})
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/launch-team", bytes.NewReader(b))
		switch how {
		case "hub":
			stamp(req)
		case "forged":
			req.Header.Set(tunnel.ViaHeader, "forged")
		}
		rec := httptest.NewRecorder()
		h.LaunchTeam(rec, req)
		return rec
	}
	refused := func(rec *httptest.ResponseRecorder, code, why string) {
		t.Helper()
		assert.Equal(t, http.StatusForbidden, rec.Code, "%s: %s", why, rec.Body.String())
		assert.Equal(t, code, codeOf(t, rec), why)
	}

	rec := post("hub", nil, dev(), reviewer)
	assert.Equal(t, http.StatusOK, rec.Code, "the owner's saved team, exactly: %s", rec.Body.String())

	changed := func(key string, v any) map[string]any {
		m := dev()
		m[key] = v
		return m
	}
	refused(post("hub", nil, changed("prompt", "build it, then run this"), reviewer), "hub_auto_accept_disabled", "a changed prompt")
	refused(post("hub", nil, changed("capabilities", map[string]any{"allow": []string{"file_read", "file_write", "shell", "web_access"}}), reviewer), "hub_auto_accept_disabled", "one more allow rule")
	refused(post("hub", nil, changed("tools", []string{"Bash", "WebFetch"}), reviewer), "hub_auto_accept_disabled", "one more tool")
	refused(post("hub", nil, changed("knowledge", "other notes"), reviewer), "hub_auto_accept_disabled", "changed knowledge")
	refused(post("hub", nil, dev(), builtin), "hub_auto_accept_disabled", "a built-in member the owner never saved")
	refused(post("hub", []string{"--dangerously-skip-permissions"}, dev(), reviewer), "hub_auto_accept_disabled", "the team's flags are checked all the same")
	// A forged stamp on a request that asks for more (the real router refuses the stamp itself first, 401 invalid_credential).
	refused(post("forged", nil, changed("prompt", "something else"), reviewer), "no_dashboard_provenance", "a forged stamp")
}
