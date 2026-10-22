package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

var bypassFlags = []string{"--dangerously-skip-permissions", "--yolo=true", "--allowedTools", "Bash(*)", "--settings", "/tmp/x.json", "--verbose"}

// #4032 (Security's #4031 finding 1): an agent's token starts an agent without the permission
// flags, allowed tools or settings it asks for, on a single launch and a team launch; the
// Operator's launch keeps them.
func TestAgentCannotLaunchALessRestrictedAgent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		asAgent  bool
		wantKept bool
	}{{"operator", false, true}, {"agent", true, false}} {
		for _, team := range []bool{false, true} {
			cfg := &config.Config{LogDir: t.TempDir()}
			db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
			require.NoError(t, err)
			t.Cleanup(func() { db.Close() })
			stubCLIPaths(t, store.NewSessionStore(db))
			terminal := newMockTerminal()
			h := NewSessionsHandler(db, cfg, nil, terminal, nil)
			body := map[string]any{"working_dir": t.TempDir(), "agent_type": "claude", "flags": bypassFlags}
			path, handle := "/api/sessions/launch", h.Launch
			if team {
				body = map[string]any{"board_name": "t", "working_dir": t.TempDir(),
					"agents": []map[string]any{{"name": "Dev", "agent_type": "claude", "flags": bypassFlags}}}
				path, handle = "/api/sessions/launch-team", h.LaunchTeam
			}
			b, _ := json.Marshal(body)
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
			if tc.asAgent {
				req = req.WithContext(context.WithValue(req.Context(), verifiedSessionKey{}, &store.SessionTokenOwner{SessionID: "sid-a", SessionName: "claude-a"}))
			}
			rec := httptest.NewRecorder()
			handle(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, "%s team=%v: %s", tc.name, team, rec.Body.String())
			cmds := strings.Join(terminal.sentCommands(), "\n")
			require.NotEmpty(t, cmds)
			for _, f := range []string{"--yolo", "--allowedTools", "Bash(*)", "/tmp/x.json", "--verbose"} {
				assert.Equal(t, tc.wantKept, strings.Contains(cmds, f), "%s team=%v: %s", tc.name, team, f)
			}
			if !tc.wantKept {
				assert.NotContains(t, cmds, "--dangerously-skip-permissions", "%s team=%v", tc.name, team)
			}
		}
	}
}

// The limits themselves: every flag but --model, allow rules, tools and hooks go; deny rules,
// which only narrow, stay.
func TestLimitAgentLaunch(t *testing.T) {
	flags, caps, tools, hooks := limitAgentLaunch("test",
		[]string{"--sandbox", "danger-full-access", "-s=x", "--approval-mode=yolo", "-y", "--add-dir", "/", "--mcp-config", "m.json", "--permission-mode=bypassPermissions", "--model", "opus", "--verbose"},
		&agent.Capabilities{Allow: []string{"Bash(*)"}, Deny: []string{"Bash(rm *)"}},
		[]string{"Bash"}, map[string]interface{}{"PreToolUse": "x"})
	assert.Equal(t, []string{"--model", "opus"}, flags)
	require.NotNil(t, caps)
	assert.Empty(t, caps.Allow)
	assert.Equal(t, []string{"Bash(rm *)"}, caps.Deny)
	assert.Nil(t, tools)
	assert.Nil(t, hooks)
}

// #4032 round 2: the agent's flags are an allowlist, so the spellings Security found past the
// old deny list (Codex -c/--config overrides and --profile, any --dangerously-* or
// --allow-dangerously-*, Claude's --agents and --plugin-dir) and a flag nobody has seen are
// removed, with their values.
func TestKeepAgentLaunchFlags(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want []string
	}{
		{[]string{"-c", "approval_policy=never"}, nil},
		{[]string{"--config", "sandbox_mode=danger-full-access"}, nil},
		{[]string{"--config=sandbox_mode=danger-full-access"}, nil},
		{[]string{"--profile", "wide"}, nil},
		{[]string{"--dangerously-anything"}, nil},
		{[]string{"--allow-dangerously-skip-permissions"}, nil},
		{[]string{"--agents", `{"x":{"tools":["Bash"]}}`}, nil},
		{[]string{"--plugin-dir", "/tmp/p"}, nil},
		{[]string{"--frobnicate", "--frobnicate=1"}, nil},
		{[]string{"--model", "opus"}, []string{"--model", "opus"}},
		{[]string{"--model=opus", "--verbose"}, []string{"--model", "opus"}},
		{[]string{"--model", "--dangerously-skip-permissions"}, nil},
		{[]string{"--model"}, nil},
	} {
		assert.Equal(t, tc.want, keepAgentLaunchFlags(tc.in), "%q", tc.in)
	}
}

// #4032 round 2: a member that is exactly an entry of a team template the Operator saved keeps
// its flags and allow rules when an agent launches the team; a copy with one extra tool or flag
// is limited like any other agent's choice.
func TestAgentLaunchOfASavedTemplateKeepsItsChoices(t *testing.T) {
	saved := `[{"name":"Wide","agents":[{"name":"Dev","prompt":"build","agent_type":"claude","flags":"--verbose --add-dir /srv","capabilities":{"deny":["Bash(rm *)"],"allow":["Bash(make *)"]}}]}]`
	member := map[string]any{"name": "Dev", "prompt": "build", "agent_type": "claude",
		"flags": []string{"--verbose", "--add-dir", "/srv"}, "capabilities": map[string]any{"allow": []string{"Bash(make *)"}, "deny": []string{"Bash(rm *)"}}}
	extraTool := map[string]any{}
	extraFlag := map[string]any{}
	for k, v := range member {
		extraTool[k], extraFlag[k] = v, v
	}
	extraTool["tools"] = []string{"Bash"}
	extraFlag["flags"] = []string{"--verbose", "--add-dir", "/srv", "--plugin-dir", "/tmp/p"}
	for _, tc := range []struct {
		name     string
		member   map[string]any
		wantKept bool
	}{{"the saved entry", member, true}, {"one extra tool", extraTool, false}, {"one extra flag", extraFlag, false}} {
		cfg := &config.Config{LogDir: t.TempDir()}
		db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
		require.NoError(t, err)
		t.Cleanup(func() { db.Close() })
		stubCLIPaths(t, store.NewSessionStore(db))
		require.NoError(t, store.NewSessionStore(db).SetSetting(context.Background(), "saved_team_templates", saved))
		terminal := newMockTerminal()
		h := NewSessionsHandler(db, cfg, nil, terminal, nil)
		b, _ := json.Marshal(map[string]any{"board_name": "t", "working_dir": t.TempDir(), "agents": []map[string]any{tc.member}})
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/launch-team", bytes.NewReader(b))
		req = req.WithContext(context.WithValue(req.Context(), verifiedSessionKey{}, &store.SessionTokenOwner{SessionID: "sid-a", SessionName: "claude-a"}))
		rec := httptest.NewRecorder()
		h.LaunchTeam(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, "%s: %s", tc.name, rec.Body.String())
		cmds := strings.Join(terminal.sentCommands(), "\n")
		require.NotEmpty(t, cmds)
		for _, f := range []string{"--verbose", "--add-dir", "/srv"} {
			assert.Equal(t, tc.wantKept, strings.Contains(cmds, f), "%s: %s", tc.name, f)
		}
		assert.NotContains(t, cmds, "/tmp/p", tc.name)
	}
	assert.True(t, inSavedTeamTemplate(map[string]string{"saved_team_templates": saved}, launchTeamAgent{Name: "Dev", Prompt: "build", AgentType: "claude",
		Flags: []string{"--verbose --add-dir", "/srv"}, Capabilities: &agent.Capabilities{Allow: []string{"Bash(make *)"}, Deny: []string{"Bash(rm *)"}}}, "claude"), "flags compare as words")
	assert.False(t, inSavedTeamTemplate(map[string]string{"saved_team_templates": saved}, launchTeamAgent{Name: "Dev", Prompt: "build", AgentType: "claude",
		Flags: []string{"--verbose", "--add-dir", "/srv"}, Capabilities: &agent.Capabilities{Allow: []string{"Bash(*)"}, Deny: []string{"Bash(rm *)"}}}, "claude"), "a wider allow rule")
	assert.False(t, inSavedTeamTemplate(map[string]string{}, launchTeamAgent{Name: "Dev"}, "claude"), "no saved templates")
}
