package routes

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/store"
)

// stubCLI writes a file LookPath accepts as a program (.exe on Windows, executable elsewhere) and returns its path.
func stubCLI(t *testing.T, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	return p
}

// Restarting an agent runs the CLI from Settings > CLI paths, like its launch
// did, not whatever binary is first on PATH (#3205).
func TestRestartUsesTheConfiguredCLIPath(t *testing.T) {
	server, _, term, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	const oldID = "restart-cli-path"
	const oldName = "codex-" + oldID
	term.addSession(oldName, t.TempDir())
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: oldID, AgentType: "codex", AgentName: "proj"}))
	custom := stubCLI(t, "codex")
	require.NoError(t, ss.SetSetting(ctx, agent.CLIPathSettingKey("codex"), custom))

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
	assert.Contains(t, strings.Join(all, "\n"), filepath.Base(custom), "the restart command: %v", all)
}

// Resuming an agent's conversation runs the configured CLI too (#3205).
func TestResumeUsesTheConfiguredCLIPath(t *testing.T) {
	server, _, term, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	const curID = "resume-cli-path"
	const curName = "claude-" + curID
	term.addSession(curName, t.TempDir())
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: curID, AgentType: "claude", AgentName: "proj"}))
	custom := stubCLI(t, "claude")
	require.NoError(t, ss.SetSetting(ctx, agent.CLIPathSettingKey("claude"), custom))

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
	assert.Contains(t, strings.Join(all, "\n"), filepath.Base(custom), "the resume command: %v", all)
}

// A configured path that can't be run refuses the restart and the resume, naming the setting and the path, and sends nothing to the
// terminal: the claude on PATH is never started in its place (#115).
func TestRestartAndResumeRefuseABrokenConfiguredCLIPath(t *testing.T) {
	server, _, term, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	const id = "broken-cli-path"
	const name = "claude-" + id
	term.addSession(name, t.TempDir())
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: id, AgentType: "claude", AgentName: "proj"}))
	gone := filepath.Join(t.TempDir(), "gone", "claude")
	require.NoError(t, ss.SetSetting(ctx, agent.CLIPathSettingKey("claude"), gone))

	for route, body := range map[string]string{
		"restart": `{"session_id":"` + id + `","agent_type":"claude"}`,
		"resume":  `{"session_id":"11111111-2222-3333-4444-555555555555","agent_type":"claude","current_session_id":"` + id + `"}`,
	} {
		resp, err := http.Post(server.URL+"/api/sessions/live/"+name+"/"+route, "application/json", bytes.NewBufferString(body))
		require.NoError(t, err)
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		resp.Body.Close()
		assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, "%s: %s", route, buf.String())
		assert.Contains(t, buf.String(), agent.CLIPathSettingKey("claude"), route)
		assert.Contains(t, buf.String(), "gone", route)
	}
	term.mu.Lock()
	defer term.mu.Unlock()
	assert.Empty(t, term.sent, "nothing was started")
}

// harmlessCLI is a real program that does nothing to stand in as an agent's configured CLI: a configured path that can't be run is
// refused (#115), so the Unix-only /usr/bin/true is not enough on Windows.
func harmlessCLI() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("SystemRoot"), "System32", "whoami.exe")
	}
	return "/usr/bin/true"
}
