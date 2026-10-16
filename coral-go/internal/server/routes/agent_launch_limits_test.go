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

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
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
			assert.Contains(t, cmds, "--verbose", "%s team=%v: other flags pass", tc.name, team)
			for _, f := range []string{"--yolo", "--allowedTools", "Bash(*)", "/tmp/x.json"} {
				assert.Equal(t, tc.wantKept, strings.Contains(cmds, f), "%s team=%v: %s", tc.name, team, f)
			}
			if !tc.wantKept {
				assert.NotContains(t, cmds, "--dangerously-skip-permissions", "%s team=%v", tc.name, team)
			}
		}
	}
}

// The limits themselves: flags in every spelling, allow rules, tools and hooks go; deny rules,
// which only narrow, and harmless flags stay.
func TestLimitAgentLaunch(t *testing.T) {
	flags, caps, tools, hooks := limitAgentLaunch("test",
		[]string{"--sandbox", "danger-full-access", "-s=x", "--approval-mode=yolo", "-y", "--add-dir", "/", "--mcp-config", "m.json", "--permission-mode=bypassPermissions", "--model", "opus", "--verbose"},
		&agent.Capabilities{Allow: []string{"Bash(*)"}, Deny: []string{"Bash(rm *)"}},
		[]string{"Bash"}, map[string]interface{}{"PreToolUse": "x"})
	assert.Equal(t, []string{"--model", "opus", "--verbose"}, flags)
	require.NotNil(t, caps)
	assert.Empty(t, caps.Allow)
	assert.Equal(t, []string{"Bash(rm *)"}, caps.Deny)
	assert.Nil(t, tools)
	assert.Nil(t, hooks)
}
