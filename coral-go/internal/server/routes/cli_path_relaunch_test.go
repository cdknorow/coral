package routes

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/store"
)

// Restarting an agent runs the CLI from Settings > CLI paths, like its launch
// did, not whatever binary is first on PATH (#3205).
func TestRestartUsesTheConfiguredCLIPath(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	server, _, term, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	const oldID = "restart-cli-path"
	const oldName = "codex-" + oldID
	term.addSession(oldName, t.TempDir())
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: oldID, AgentType: "codex", AgentName: "proj"}))
	require.NoError(t, ss.SetSetting(ctx, agent.CLIPathSettingKey("codex"), "/opt/custom/bin/codex"))

	resp, err := http.Post(server.URL+"/api/sessions/live/"+oldName+"/restart", "application/json",
		bytes.NewBufferString(`{"session_id":"`+oldID+`","agent_type":"codex"}`))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	term.mu.Lock()
	defer term.mu.Unlock()
	var all []string
	for _, cmds := range term.sent {
		all = append(all, cmds...)
	}
	assert.Contains(t, strings.Join(all, "\n"), "/opt/custom/bin/codex", "the restart command: %v", all)
}

// Resuming an agent's conversation runs the configured CLI too (#3205).
func TestResumeUsesTheConfiguredCLIPath(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	server, _, term, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	const curID = "resume-cli-path"
	const curName = "claude-" + curID
	term.addSession(curName, t.TempDir())
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: curID, AgentType: "claude", AgentName: "proj"}))
	require.NoError(t, ss.SetSetting(ctx, agent.CLIPathSettingKey("claude"), "/opt/custom/bin/claude"))

	resp, err := http.Post(server.URL+"/api/sessions/live/"+curName+"/resume", "application/json",
		bytes.NewBufferString(`{"session_id":"11111111-2222-3333-4444-555555555555","agent_type":"claude","current_session_id":"`+curID+`"}`))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	term.mu.Lock()
	defer term.mu.Unlock()
	var all []string
	for _, cmds := range term.sent {
		all = append(all, cmds...)
	}
	assert.Contains(t, strings.Join(all, "\n"), "/opt/custom/bin/claude", "the resume command: %v", all)
}
