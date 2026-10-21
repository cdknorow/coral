package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

var mcpGithub = map[string]any{"github": map[string]any{"command": "npx", "env": map[string]any{"GITHUB_TOKEN": "ghp_x"}}}

// #3644: Coral doesn't configure MCP servers. Every entry point that took them refuses them with
// a message naming the field, so nobody thinks the setting applied.
func TestMCPServersAreRefusedAtEveryEntryPoint(t *testing.T) {
	server, _, term, _ := setupSessionsTestServer(t)
	post := func(path string, body any) (int, string) {
		t.Helper()
		b, _ := json.Marshal(body)
		resp, err := http.Post(server.URL+path, "application/json", bytes.NewReader(b))
		require.NoError(t, err)
		defer resp.Body.Close()
		msg, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(msg)
	}

	code, msg := post("/api/sessions/launch", map[string]any{"working_dir": t.TempDir(), "agent_type": "claude", "mcpServers": mcpGithub})
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, msg, "mcpServers is not supported")
	code, msg = post("/api/sessions/launch", map[string]any{"working_dir": t.TempDir(), "agent_type": "claude", "mcpServers": map[string]any{}})
	assert.Equal(t, http.StatusBadRequest, code, "even an empty object: %s", msg)

	code, msg = post("/api/sessions/launch-team", map[string]any{"board_name": "t", "working_dir": t.TempDir(),
		"agents": []map[string]any{{"name": "Dev", "prompt": "x", "mcpServers": mcpGithub}}})
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, msg, `agent \"Dev\"`)
	assert.Contains(t, msg, "mcpServers is not supported")

	sessions, _ := term.ListSessions(context.Background())
	assert.Empty(t, sessions, "nothing was launched")

	// null is the same as leaving it out.
	code, msg = post("/api/sessions/launch", map[string]any{"working_dir": t.TempDir(), "agent_type": "claude", "mcpServers": nil})
	assert.Equal(t, http.StatusOK, code, msg)
}

func TestWorkflowsNamingMCPServersAreRefused(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "wf.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h := NewWorkflowHandler(db, &config.Config{})
	r := chi.NewRouter()
	r.Post("/api/workflows", h.CreateWorkflow)
	r.Put("/api/workflows/{workflowID}", h.UpdateWorkflow)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/workflows", map[string]any{"name": "wf",
		"steps": []map[string]any{{"name": "one", "type": "agent", "prompt": "x", "agent": map[string]any{"agent_type": "claude", "mcpServers": mcpGithub}}}})
	assert.Equal(t, http.StatusBadRequest, code, body)
	assert.Contains(t, body["error"], "step 0: agent: mcpServers is not supported")

	code, body = sendJSON(t, http.MethodPost, srv.URL+"/api/workflows", map[string]any{"name": "wf2",
		"default_agent": map[string]any{"agent_type": "claude", "mcpServers": mcpGithub},
		"steps":         []map[string]any{{"name": "one", "type": "shell", "command": "true"}}})
	assert.Equal(t, http.StatusBadRequest, code, body)
	assert.Contains(t, body["error"], "default_agent: mcpServers is not supported")

	code, body = sendJSON(t, http.MethodPost, srv.URL+"/api/workflows", map[string]any{"name": "wf3",
		"steps": []map[string]any{{"name": "one", "type": "shell", "command": "true"}}})
	require.Equal(t, http.StatusCreated, code, body)
	id := jsonNum(body["id"])
	code, body = sendJSON(t, http.MethodPut, srv.URL+"/api/workflows/"+id, map[string]any{
		"steps": []map[string]any{{"name": "one", "type": "agent", "prompt": "x", "agent": map[string]any{"mcpServers": mcpGithub}}}})
	assert.Equal(t, http.StatusBadRequest, code, "an edit too: %v", body)
}

func jsonNum(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestTeamImportRefusesMCPServersInFrontmatter(t *testing.T) {
	h := newSystemHandlerForTest(t)
	dir := newTeamDir(t)
	writeFile(t, filepath.Join(dir, "agents", "tools.md"), "---\nname: Tooling\nmcp_servers:\n  github:\n    command: npx\n---\nBody")
	code, out := importTeam(t, h, dir, asOperator)
	assert.Equal(t, http.StatusBadRequest, code, out)
	assert.Contains(t, out["error"], "agents/tools.md: mcp_servers is not supported")
}

// A session stored with MCP servers before #3644 (the startup wipe clears them; this shows the
// stored value is ignored anyway) restarts without them in its settings.
func TestRestartOfAnOldSessionLaunchesWithoutMCP(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	server, _, term, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	const oldID = "11111111-aaaa-4bbb-8ccc-0000000003f4"
	const oldName = "claude-" + oldID
	tools := `["TodoWrite"]`
	term.addSession(oldName, t.TempDir())
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: oldID, AgentType: "claude", AgentName: "proj", WorkingDir: t.TempDir(), Tools: &tools}))
	_, err := ss.DB().ExecContext(ctx, "UPDATE live_sessions SET mcp_servers = ? WHERE session_id = ?", `{"github":{"command":"npx","env":{"GITHUB_TOKEN":"ghp_old"}}}`, oldID)
	require.NoError(t, err)

	b, _ := json.Marshal(map[string]any{"session_id": oldID})
	resp, err := http.Post(server.URL+"/api/sessions/live/"+oldName+"/restart", "application/json", bytes.NewReader(b))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	parts := strings.Fields(strings.Join(term.sentCommands(), "\n"))
	settingsPath := ""
	for i := 0; i < len(parts)-1; i++ {
		if parts[i] == "--settings" {
			settingsPath = strings.Trim(parts[i+1], `'"`)
		}
	}
	require.NotEmpty(t, settingsPath, "the restart launched with settings")
	settings, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	assert.Contains(t, string(settings), `"allowedTools"`)
	assert.NotContains(t, string(settings), "mcpServers")
	assert.NotContains(t, string(settings), "ghp_old")
}
