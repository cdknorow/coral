package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// cliPathStub writes a file LookPath accepts as a program (.exe on Windows, executable elsewhere) and returns its path.
func cliPathStub(t *testing.T, name string) string {
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
	t.Parallel() // no shared state: these ran one after another (#5622)
	server, _, term, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	const oldID = "restart-cli-path"
	const oldName = "codex-" + oldID
	term.addSession(oldName, t.TempDir())
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: oldID, AgentType: "codex", AgentName: "proj"}))
	custom := cliPathStub(t, "codex")
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
	t.Parallel() // no shared state: these ran one after another (#5622)
	server, _, term, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	const curID = "resume-cli-path"
	const curName = "claude-" + curID
	term.addSession(curName, t.TempDir())
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: curID, AgentType: "claude", AgentName: "proj"}))
	custom := cliPathStub(t, "claude")
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

// harmlessCLI is a real program that does nothing and prints nothing, to stand in as an agent's configured CLI: a configured path that can't
// be run is refused (#115), so the Unix-only /usr/bin/true is not enough on Windows, where it is a .cmd that exits 0.
func harmlessCLI(t testing.TB) string {
	if runtime.GOOS != "windows" {
		return "/usr/bin/true"
	}
	p := filepath.Join(t.TempDir(), "harmless-cli.cmd")
	if err := os.WriteFile(p, []byte("@exit /b 0\r\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// harmlessCLIName is what harmlessCLI's program is called in a terminal's output.
func harmlessCLIName() string {
	if runtime.GOOS == "windows" {
		return "harmless-cli"
	}
	return "/usr/bin/true"
}

// The launch and the wake refuse a broken configured path with the text and a 422, not a 500 or a bare 200 "failed"; through Coral Hub
// the text names the setting only, never this computer's path (#115).
func TestLaunchAndWakeRefuseABrokenConfiguredCLIPath(t *testing.T) {
	server, h, term, ss := setupSessionsTestServer(t)
	wr := chi.NewRouter()
	wr.Post("/api/sessions/live/{sessionID}/wake", h.WakeSession)
	wakeServer := httptest.NewServer(wr)
	t.Cleanup(wakeServer.Close)
	ctx := context.Background()
	const id = "broken-wake"
	const name = "claude-" + id
	term.addSession(name, t.TempDir())
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: id, AgentType: "claude", AgentName: "proj"}))
	require.NoError(t, ss.SetSessionSleeping(ctx, id, true))
	gone := filepath.Join(t.TempDir(), "gone-dir", "claude")
	require.NoError(t, ss.SetSetting(ctx, agent.CLIPathSettingKey("claude"), gone))

	post := func(base, path, body string, hub bool) (int, string) {
		req, err := http.NewRequest(http.MethodPost, base+path, bytes.NewBufferString(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		if hub {
			req.Header.Set(tunnel.ViaHeader, "hub")
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return resp.StatusCode, buf.String()
	}
	launchBody, err := json.Marshal(map[string]string{"working_dir": t.TempDir(), "agent_type": "claude"})
	require.NoError(t, err)
	launch := string(launchBody)
	for label, call := range map[string]func(hub bool) (int, string){
		"launch": func(hub bool) (int, string) { return post(server.URL, "/api/sessions/launch", launch, hub) },
		"wake":   func(hub bool) (int, string) { return post(wakeServer.URL, "/api/sessions/live/"+id+"/wake", "{}", hub) },
	} {
		code, body := call(false)
		assert.Equal(t, http.StatusUnprocessableEntity, code, "%s: %s", label, body)
		assert.Contains(t, body, agent.CLIPathSettingKey("claude"), label)
		assert.Contains(t, body, "gone-dir", label+": a local caller is told the path")

		code, body = call(true)
		assert.Equal(t, http.StatusUnprocessableEntity, code, "%s via the Hub: %s", label, body)
		assert.Contains(t, body, agent.CLIPathSettingKey("claude"), label)
		assert.NotContains(t, body, "gone-dir", label+": the Hub is told the setting only")
	}
}
