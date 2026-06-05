package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/store"
)

// mockSessionTerminal implements ptymanager.SessionTerminal for testing.
type mockSessionTerminal struct {
	mu               sync.Mutex
	sessions         map[string]*ptymanager.PaneInfo
	outputs          map[string]string
	sent             map[string][]string
	killSessionCalls []string
}

func newMockTerminal() *mockSessionTerminal {
	return &mockSessionTerminal{
		sessions: make(map[string]*ptymanager.PaneInfo),
		outputs:  make(map[string]string),
		sent:     make(map[string][]string),
	}
}

func (m *mockSessionTerminal) ListSessions(_ context.Context) ([]ptymanager.PaneInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]ptymanager.PaneInfo, 0, len(m.sessions))
	for _, p := range m.sessions {
		result = append(result, *p)
	}
	return result, nil
}

func (m *mockSessionTerminal) FindSession(_ context.Context, name, _, _ string) (*ptymanager.PaneInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.sessions[name]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("session %q not found", name)
}

func (m *mockSessionTerminal) CaptureOutput(_ context.Context, name string, _ int, _, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if out, ok := m.outputs[name]; ok {
		return out, nil
	}
	return "", fmt.Errorf("session %q not found", name)
}

func (m *mockSessionTerminal) SendInput(_ context.Context, name, command, _, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[name]; !ok {
		return fmt.Errorf("session %q not found", name)
	}
	m.sent[name] = append(m.sent[name], command)
	return nil
}

func (m *mockSessionTerminal) SendRawInput(_ context.Context, name string, _ []string, _, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[name]; !ok {
		return fmt.Errorf("session %q not found", name)
	}
	return nil
}

func (m *mockSessionTerminal) SendToTarget(_ context.Context, target, command string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent[target] = append(m.sent[target], command)
	return nil
}
func (m *mockSessionTerminal) SendTerminalInput(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockSessionTerminal) CreateSession(_ context.Context, name, workDir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[name] = &ptymanager.PaneInfo{
		SessionName: name,
		PaneTitle:   name,
		Target:      name + ":0.0",
		CurrentPath: workDir,
	}
	return nil
}

func (m *mockSessionTerminal) KillSession(_ context.Context, name, _, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.killSessionCalls = append(m.killSessionCalls, name)
	delete(m.sessions, name)
	return nil
}

func (m *mockSessionTerminal) KillSessionOnly(_ context.Context, name, _, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, name)
	return nil
}

func (m *mockSessionTerminal) RestartPane(_ context.Context, _, _ string) error { return nil }
func (m *mockSessionTerminal) RenameSession(_ context.Context, oldName, newName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.sessions[oldName]; ok {
		p.SessionName = newName
		p.PaneTitle = newName
		m.sessions[newName] = p
		delete(m.sessions, oldName)
	}
	return nil
}

func (m *mockSessionTerminal) ResizeSession(_ context.Context, _ string, _ int, _, _ string) error {
	return nil
}
func (m *mockSessionTerminal) ResizeTarget(_ context.Context, _ string, _, _ int) error { return nil }
func (m *mockSessionTerminal) StartLogging(_ context.Context, _, _ string) error        { return nil }
func (m *mockSessionTerminal) StopLogging(_ context.Context, _ string) error            { return nil }
func (m *mockSessionTerminal) ClearHistory(_ context.Context, _ string) error           { return nil }

func (m *mockSessionTerminal) HasSession(_ context.Context, name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.sessions[name]
	return ok
}

func (m *mockSessionTerminal) FindTarget(_ context.Context, name, _, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.sessions[name]; ok {
		return p.Target, nil
	}
	return "", fmt.Errorf("session %q not found", name)
}

func (m *mockSessionTerminal) SetPaneTitle(_ context.Context, _, _ string) {}

func (m *mockSessionTerminal) AttachCommand(_ string) string { return "" }

// addSession adds a mock session for testing.
func (m *mockSessionTerminal) addSession(name, workDir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[name] = &ptymanager.PaneInfo{
		SessionName: name,
		PaneTitle:   name,
		Target:      name + ":0.0",
		CurrentPath: workDir,
	}
}

// setOutput sets mock capture output for a session.
func (m *mockSessionTerminal) setOutput(name, output string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outputs[name] = output
}

func (m *mockSessionTerminal) sentCommands() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var commands []string
	for _, cmds := range m.sent {
		commands = append(commands, cmds...)
	}
	return commands
}

// setupSessionsTestServer creates a test HTTP server with a SessionsHandler and all routes.
func setupSessionsTestServer(t *testing.T) (*httptest.Server, *SessionsHandler, *mockSessionTerminal, *store.SessionStore) {
	t.Helper()
	return setupSessionsTestServerWithConfig(t, &config.Config{})
}

func TestSessionsList_Empty(t *testing.T) {
	server, _, _, _ := setupSessionsTestServer(t)

	resp, err := http.Get(server.URL + "/api/sessions/live")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var sessions []interface{}
	err = json.NewDecoder(resp.Body).Decode(&sessions)
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestSessionsList_WithSessions(t *testing.T) {
	server, _, terminal, _ := setupSessionsTestServer(t)

	// Add mock sessions
	// Session names must match ParseSessionName regex: {type}-{uuid}
	terminal.addSession("claude-00000000-0000-0000-0000-000000000001", "/tmp/test")
	terminal.addSession("claude-00000000-0000-0000-0000-000000000002", "/tmp/test2")

	resp, err := http.Get(server.URL + "/api/sessions/live")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var sessions []map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&sessions)
	require.NoError(t, err)
	assert.Len(t, sessions, 2)
}

func TestSessionsCapture_NotFound(t *testing.T) {
	server, _, _, _ := setupSessionsTestServer(t)

	resp, err := http.Get(server.URL + "/api/sessions/live/nonexistent/capture")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(t, err)
	assert.Nil(t, result["capture"], "capture should be nil for nonexistent session")
	assert.NotEmpty(t, result["error"], "should include an error message")
}

func TestSessionsCapture_WithOutput(t *testing.T) {
	server, _, terminal, _ := setupSessionsTestServer(t)

	terminal.addSession("claude-00000000-0000-0000-0000-000000000003", "/tmp/test")
	terminal.setOutput("claude-00000000-0000-0000-0000-000000000003", "Hello from agent\n$ doing work")

	resp, err := http.Get(server.URL + "/api/sessions/live/claude-00000000-0000-0000-0000-000000000003/capture")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(t, err)
	assert.Contains(t, result, "capture")
}

func TestSessionsSend_NotFound(t *testing.T) {
	server, _, _, _ := setupSessionsTestServer(t)

	body := bytes.NewBufferString(`{"command": "echo hello"}`)
	resp, err := http.Post(server.URL+"/api/sessions/live/nonexistent/send", "application/json", body)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Should return error since session doesn't exist
	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	if errMsg, ok := result["error"]; ok {
		assert.NotEmpty(t, errMsg)
	}
}

func TestSessionsSend_Success(t *testing.T) {
	server, _, terminal, _ := setupSessionsTestServer(t)

	terminal.addSession("claude-00000000-0000-0000-0000-000000000004", "/tmp/test")

	body := bytes.NewBufferString(`{"command": "echo hello"}`)
	resp, err := http.Post(server.URL+"/api/sessions/live/claude-00000000-0000-0000-0000-000000000004/send", "application/json", body)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestSessionsResize(t *testing.T) {
	server, _, terminal, _ := setupSessionsTestServer(t)

	terminal.addSession("claude-00000000-0000-0000-0000-000000000005", "/tmp/test")

	body := bytes.NewBufferString(`{"columns": 120}`)
	resp, err := http.Post(server.URL+"/api/sessions/live/claude-00000000-0000-0000-0000-000000000005/resize", "application/json", body)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestSessionsKill(t *testing.T) {
	server, _, terminal, _ := setupSessionsTestServer(t)

	terminal.addSession("claude-00000000-0000-0000-0000-000000000006", "/tmp/test")

	resp, err := http.Post(server.URL+"/api/sessions/live/claude-00000000-0000-0000-0000-000000000006/kill", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify session was removed
	assert.False(t, terminal.HasSession(context.Background(), "claude-00000000-0000-0000-0000-000000000006"))
}

func TestSessionsLaunch_MissingWorkDir(t *testing.T) {
	server, _, _, _ := setupSessionsTestServer(t)

	body := bytes.NewBufferString(`{"agent_type": "claude"}`)
	resp, err := http.Post(server.URL+"/api/sessions/launch", "application/json", body)
	require.NoError(t, err)
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	assert.Contains(t, result, "error")
}

func TestSessionsLaunch_Success(t *testing.T) {
	server, _, terminal, _ := setupSessionsTestServer(t)

	workDir := t.TempDir()
	body, _ := json.Marshal(map[string]interface{}{
		"working_dir": workDir,
		"agent_type":  "claude",
	})
	resp, err := http.Post(server.URL+"/api/sessions/launch", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	sessionName, ok := result["session_name"]
	assert.True(t, ok, "response should contain session_name")
	assert.NotEmpty(t, sessionName)

	// Verify session was created in the terminal
	sessions, _ := terminal.ListSessions(context.Background())
	assert.NotEmpty(t, sessions)
}

func TestSessionsLaunchTeam_MissingBoardName(t *testing.T) {
	server, _, _, _ := setupSessionsTestServer(t)

	body := bytes.NewBufferString(`{"working_dir": "/tmp", "agents": [{"name": "dev"}]}`)
	resp, err := http.Post(server.URL+"/api/sessions/launch-team", "application/json", body)
	require.NoError(t, err)
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	assert.Contains(t, result, "error")
}

func TestSessionsLaunchTeam_PreservesToolsAndMCPServers(t *testing.T) {
	cfg := &config.Config{LogDir: t.TempDir()}
	dbPath := t.TempDir() + "/test.db"
	db, err := store.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	boardDBPath := t.TempDir() + "/board.db"
	bs, err := board.NewStore(boardDBPath)
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })

	terminal := newMockTerminal()
	handler := NewSessionsHandler(db, cfg, nil, terminal, bs)
	ss := store.NewSessionStore(db)

	workDir := t.TempDir()
	body, _ := json.Marshal(map[string]any{
		"board_name":  "tool-team",
		"working_dir": workDir,
		"agents": []map[string]any{
			{
				"name":   "Backend Dev",
				"prompt": "Build APIs",
				"tools":  []string{"TodoWrite", "Bash(npm test)"},
				"mcpServers": map[string]any{
					"github": map[string]any{
						"command": "npx",
						"args":    []any{"-y", "@modelcontextprotocol/server-github"},
					},
				},
			},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/launch-team", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.LaunchTeam(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var result map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&result))
	agents := result["agents"].([]any)
	sessionID := agents[0].(map[string]any)["session_id"].(string)

	ls, err := ss.GetLiveSession(context.Background(), sessionID)
	require.NoError(t, err)
	require.NotNil(t, ls)
	require.NotNil(t, ls.Tools)
	require.NotNil(t, ls.MCPServers)
	assert.JSONEq(t, `["TodoWrite","Bash(npm test)"]`, *ls.Tools)
	assert.JSONEq(t, `{"github":{"args":["-y","@modelcontextprotocol/server-github"],"command":"npx"}}`, *ls.MCPServers)

	commands := strings.Join(terminal.sentCommands(), "\n")
	require.NotEmpty(t, commands)
	parts := strings.Fields(commands)
	settingsPath := ""
	for i := 0; i < len(parts)-1; i++ {
		if parts[i] == "--settings" {
			settingsPath = parts[i+1]
			break
		}
	}
	require.NotEmpty(t, settingsPath)
	settingsJSON, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	assert.Contains(t, string(settingsJSON), `"allowedTools"`)
	assert.Contains(t, string(settingsJSON), `"mcpServers"`)
}

func TestSessionsSetDisplayName(t *testing.T) {
	server, _, terminal, ss := setupSessionsTestServer(t)

	terminal.addSession("claude-test-rename", "/tmp/test")
	// Register session in DB
	ctx := context.Background()
	ss.RegisterLiveSession(ctx, &store.LiveSession{AgentName: "claude-test-rename", AgentType: "claude", WorkingDir: "/tmp/test", SessionID: "test-123"})

	body := bytes.NewBufferString(`{"display_name": "My Agent", "session_id": "test-123"}`)
	resp, err := http.Post(server.URL+"/api/sessions/live/claude-test-rename/set-display-name", "application/json", body)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestSessionsSetIcon(t *testing.T) {
	server, _, terminal, ss := setupSessionsTestServer(t)

	terminal.addSession("claude-test-icon", "/tmp/test")
	ctx := context.Background()
	ss.RegisterLiveSession(ctx, &store.LiveSession{AgentName: "claude-test-icon", AgentType: "claude", WorkingDir: "/tmp/test", SessionID: "test-icon-123"})

	body := bytes.NewBufferString(`{"icon": "🚀", "session_id": "test-icon-123"}`)
	resp, err := http.Post(server.URL+"/api/sessions/live/claude-test-icon/set-icon", "application/json", body)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestSessionsTasks_CRUD(t *testing.T) {
	server, _, terminal, ss := setupSessionsTestServer(t)

	terminal.addSession("claude-test-tasks", "/tmp/test")
	ctx := context.Background()
	ss.RegisterLiveSession(ctx, &store.LiveSession{AgentName: "claude-test-tasks", AgentType: "claude", WorkingDir: "/tmp/test", SessionID: "test-task-123"})

	// List tasks (should be empty)
	resp, err := http.Get(server.URL + "/api/sessions/live/claude-test-tasks/tasks?session_id=test-task-123")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Create task
	body := bytes.NewBufferString(`{"title": "Test task", "session_id": "test-task-123"}`)
	resp, err = http.Post(server.URL+"/api/sessions/live/claude-test-tasks/tasks", "application/json", body)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestSessionsNotes_Create(t *testing.T) {
	server, _, terminal, ss := setupSessionsTestServer(t)

	terminal.addSession("claude-test-notes", "/tmp/test")
	ctx := context.Background()
	ss.RegisterLiveSession(ctx, &store.LiveSession{AgentName: "claude-test-notes", AgentType: "claude", WorkingDir: "/tmp/test", SessionID: "test-note-123"})

	body := bytes.NewBufferString(`{"content": "Test note", "session_id": "test-note-123"}`)
	resp, err := http.Post(server.URL+"/api/sessions/live/claude-test-notes/notes", "application/json", body)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestSessionsEvents_CreateAndList(t *testing.T) {
	server, _, terminal, ss := setupSessionsTestServer(t)

	terminal.addSession("claude-test-events", "/tmp/test")
	ctx := context.Background()
	ss.RegisterLiveSession(ctx, &store.LiveSession{AgentName: "claude-test-events", AgentType: "claude", WorkingDir: "/tmp/test", SessionID: "test-evt-123"})

	// Create event
	body := bytes.NewBufferString(`{"event_type": "tool_use", "summary": "Read file", "session_id": "test-evt-123"}`)
	resp, err := http.Post(server.URL+"/api/sessions/live/claude-test-events/events", "application/json", body)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// List events
	resp, err = http.Get(server.URL + "/api/sessions/live/claude-test-events/events?session_id=test-evt-123")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Event counts
	resp, err = http.Get(server.URL + "/api/sessions/live/claude-test-events/event-counts?session_id=test-evt-123")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestSessionsKeys(t *testing.T) {
	server, _, terminal, _ := setupSessionsTestServer(t)

	terminal.addSession("claude-test-keys", "/tmp/test")

	body := bytes.NewBufferString(`{"keys": ["Enter"]}`)
	resp, err := http.Post(server.URL+"/api/sessions/live/claude-test-keys/keys", "application/json", body)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// setupSessionsTestServerWithConfig creates a test server with custom config and all session routes.
func setupSessionsTestServerWithConfig(t *testing.T, cfg *config.Config) (*httptest.Server, *SessionsHandler, *mockSessionTerminal, *store.SessionStore) {
	t.Helper()

	if cfg.LogDir == "" {
		cfg.LogDir = t.TempDir()
	}

	dbPath := t.TempDir() + "/test.db"
	db, err := store.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	boardDBPath := t.TempDir() + "/board.db"
	bs, err := board.NewStore(boardDBPath)
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })

	terminal := newMockTerminal()
	handler := NewSessionsHandler(db, cfg, nil, terminal, bs)
	ss := store.NewSessionStore(db)

	r := chi.NewRouter()

	// Session routes
	r.Get("/api/sessions/live", handler.List)
	r.Get("/api/sessions/live/{name}", handler.Detail)
	r.Get("/api/sessions/live/{name}/capture", handler.Capture)
	r.Post("/api/sessions/live/{name}/send", handler.Send)
	r.Post("/api/sessions/live/{name}/keys", handler.Keys)
	r.Post("/api/sessions/live/{name}/resize", handler.Resize)
	r.Post("/api/sessions/live/{name}/kill", handler.Kill)
	r.Post("/api/sessions/live/{name}/set-display-name", handler.SetDisplayName)
	r.Post("/api/sessions/live/{name}/set-icon", handler.SetIcon)
	r.Post("/api/sessions/launch", handler.Launch)
	r.Post("/api/sessions/launch-team", handler.LaunchTeam)

	// Task routes
	r.Get("/api/sessions/live/{name}/tasks", handler.ListTasks)
	r.Post("/api/sessions/live/{name}/tasks", handler.CreateTask)
	r.Put("/api/sessions/live/{name}/tasks/{taskID}", handler.UpdateTask)
	r.Delete("/api/sessions/live/{name}/tasks/{taskID}", handler.DeleteTask)

	// Note routes
	r.Get("/api/sessions/live/{name}/notes", handler.ListNotes)
	r.Post("/api/sessions/live/{name}/notes", handler.CreateNote)

	// Event routes
	r.Get("/api/sessions/live/{name}/events", handler.ListEvents)
	r.Post("/api/sessions/live/{name}/events", handler.CreateEvent)
	r.Get("/api/sessions/live/{name}/event-counts", handler.EventCounts)

	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	return server, handler, terminal, ss
}

// ── Edition Limits Tests ────────────────────────────────────────────────

func TestEditionLimits_LaunchTeam_TeamLimitExceeded(t *testing.T) {
	cfg := &config.Config{
		MaxLiveTeams:  1,
		MaxLiveAgents: 5,
	}
	server, _, _, ss := setupSessionsTestServerWithConfig(t, cfg)
	ctx := context.Background()

	// Pre-register a team (live session with a board_name)
	board := "existing-team"
	ss.RegisterLiveSession(ctx, &store.LiveSession{
		AgentName: "agent-1", AgentType: "claude", WorkingDir: "/tmp",
		SessionID: "sid-1", BoardName: &board,
	})

	// Try to launch a second team — should be rejected
	body, _ := json.Marshal(map[string]interface{}{
		"board_name":  "new-team",
		"working_dir": t.TempDir(),
		"agents":      []map[string]string{{"name": "dev"}},
	})
	resp, err := http.Post(server.URL+"/api/sessions/launch-team", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	assert.Contains(t, result["error"], "Demo limit")
	assert.Contains(t, result["error"], "team")
}

func TestEditionLimits_Launch_AgentLimitExceeded(t *testing.T) {
	cfg := &config.Config{
		MaxLiveTeams:  1,
		MaxLiveAgents: 2,
	}
	server, _, _, ss := setupSessionsTestServerWithConfig(t, cfg)
	ctx := context.Background()

	// Pre-register 2 agents (at the limit)
	ss.RegisterLiveSession(ctx, &store.LiveSession{
		AgentName: "agent-1", AgentType: "claude", WorkingDir: "/tmp", SessionID: "sid-1",
	})
	ss.RegisterLiveSession(ctx, &store.LiveSession{
		AgentName: "agent-2", AgentType: "claude", WorkingDir: "/tmp", SessionID: "sid-2",
	})

	// Try to launch another agent — should be rejected
	body, _ := json.Marshal(map[string]interface{}{
		"working_dir": t.TempDir(),
		"agent_type":  "claude",
	})
	resp, err := http.Post(server.URL+"/api/sessions/launch", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	assert.Contains(t, result["error"], "Demo limit")
	assert.Contains(t, result["error"], "agent")
}

func TestEditionLimits_LaunchTeam_AgentLimitExceeded(t *testing.T) {
	cfg := &config.Config{
		MaxLiveTeams:  10,
		MaxLiveAgents: 3,
	}
	server, _, _, ss := setupSessionsTestServerWithConfig(t, cfg)
	ctx := context.Background()

	// Pre-register 2 agents
	ss.RegisterLiveSession(ctx, &store.LiveSession{
		AgentName: "agent-1", AgentType: "claude", WorkingDir: "/tmp", SessionID: "sid-1",
	})
	ss.RegisterLiveSession(ctx, &store.LiveSession{
		AgentName: "agent-2", AgentType: "claude", WorkingDir: "/tmp", SessionID: "sid-2",
	})

	// Try to launch a team with 2 agents (would exceed limit of 3)
	body, _ := json.Marshal(map[string]interface{}{
		"board_name":  "new-team",
		"working_dir": t.TempDir(),
		"agents":      []map[string]string{{"name": "dev1"}, {"name": "dev2"}},
	})
	resp, err := http.Post(server.URL+"/api/sessions/launch-team", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	assert.Contains(t, result["error"], "Demo limit")
}

func TestEditionLimits_NoLimits_UnlimitedLaunches(t *testing.T) {
	cfg := &config.Config{
		MaxLiveTeams:  0, // unlimited
		MaxLiveAgents: 0, // unlimited
	}
	server, _, _, ss := setupSessionsTestServerWithConfig(t, cfg)
	ctx := context.Background()

	// Pre-register many agents
	for i := 0; i < 10; i++ {
		ss.RegisterLiveSession(ctx, &store.LiveSession{
			AgentName: fmt.Sprintf("agent-%d", i), AgentType: "claude",
			WorkingDir: "/tmp", SessionID: fmt.Sprintf("sid-%d", i),
		})
	}

	// Launch should succeed (no limits)
	body, _ := json.Marshal(map[string]interface{}{
		"working_dir": t.TempDir(),
		"agent_type":  "claude",
	})
	resp, err := http.Post(server.URL+"/api/sessions/launch", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestEditionLimits_Launch_BelowLimit_Succeeds(t *testing.T) {
	cfg := &config.Config{
		MaxLiveTeams:  1,
		MaxLiveAgents: 5,
	}
	server, _, _, _ := setupSessionsTestServerWithConfig(t, cfg)

	// Launch with no existing sessions — should succeed
	body, _ := json.Marshal(map[string]interface{}{
		"working_dir": t.TempDir(),
		"agent_type":  "claude",
	})
	resp, err := http.Post(server.URL+"/api/sessions/launch", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestEditionLimits_SleepingAgents_NotCounted(t *testing.T) {
	cfg := &config.Config{
		MaxLiveTeams:  1,
		MaxLiveAgents: 2,
	}
	server, _, _, ss := setupSessionsTestServerWithConfig(t, cfg)
	ctx := context.Background()

	// Register 2 agents, but one is sleeping
	ss.RegisterLiveSession(ctx, &store.LiveSession{
		AgentName: "agent-1", AgentType: "claude", WorkingDir: "/tmp", SessionID: "sid-1",
	})
	ss.RegisterLiveSession(ctx, &store.LiveSession{
		AgentName: "agent-2", AgentType: "claude", WorkingDir: "/tmp", SessionID: "sid-2", IsSleeping: 1,
	})

	// Launch should succeed since only 1 non-sleeping agent
	body, _ := json.Marshal(map[string]interface{}{
		"working_dir": t.TempDir(),
		"agent_type":  "claude",
	})
	resp, err := http.Post(server.URL+"/api/sessions/launch", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// ── Kill Sleeping Sessions Tests ────────────────────────────────────────

func TestKill_SleepingSession_RemovedFromDB(t *testing.T) {
	server, handler, terminal, ss := setupSessionsTestServer(t)
	ctx := context.Background()

	// Set up board handler so board pause clearing works
	bh := NewBoardHandler(handler.bs)
	handler.SetBoardHandler(bh)

	board := "test-board"
	terminal.addSession("claude-sleeping-1", "/tmp/test")
	ss.RegisterLiveSession(ctx, &store.LiveSession{
		AgentName: "claude-sleeping-1", AgentType: "claude", WorkingDir: "/tmp",
		SessionID: "sleep-sid-1", BoardName: &board, IsSleeping: 1,
	})

	// Verify session exists in DB
	count, err := ss.CountBoardSessions(ctx, board)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Kill the sleeping session
	body := bytes.NewBufferString(`{"agent_type": "claude", "session_id": "sleep-sid-1"}`)
	resp, err := http.Post(server.URL+"/api/sessions/live/claude-sleeping-1/kill", "application/json", body)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify session is removed from DB
	count, err = ss.CountBoardSessions(ctx, board)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestKill_LastSessionOnBoard_ClearsPauseState(t *testing.T) {
	server, handler, terminal, ss := setupSessionsTestServer(t)
	ctx := context.Background()

	bh := NewBoardHandler(handler.bs)
	handler.SetBoardHandler(bh)

	// Pause the board (simulates sleeping state)
	boardName := "paused-board"
	bh.SetPaused(boardName, true)
	assert.True(t, bh.IsPaused(boardName))

	terminal.addSession("claude-paused-1", "/tmp/test")
	ss.RegisterLiveSession(ctx, &store.LiveSession{
		AgentName: "claude-paused-1", AgentType: "claude", WorkingDir: "/tmp",
		SessionID: "paused-sid-1", BoardName: &boardName, IsSleeping: 1,
	})

	// Kill the last (only) session on the board
	body := bytes.NewBufferString(`{"agent_type": "claude", "session_id": "paused-sid-1"}`)
	resp, err := http.Post(server.URL+"/api/sessions/live/claude-paused-1/kill", "application/json", body)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Board pause state should be cleared
	assert.False(t, bh.IsPaused(boardName))
}

func TestKill_NotLastSessionOnBoard_KeepsPauseState(t *testing.T) {
	server, handler, terminal, ss := setupSessionsTestServer(t)
	ctx := context.Background()

	bh := NewBoardHandler(handler.bs)
	handler.SetBoardHandler(bh)

	boardName := "multi-board"
	bh.SetPaused(boardName, true)

	// Register 2 sessions on the same board
	terminal.addSession("claude-multi-1", "/tmp/test")
	ss.RegisterLiveSession(ctx, &store.LiveSession{
		AgentName: "claude-multi-1", AgentType: "claude", WorkingDir: "/tmp",
		SessionID: "multi-sid-1", BoardName: &boardName, IsSleeping: 1,
	})
	terminal.addSession("claude-multi-2", "/tmp/test")
	ss.RegisterLiveSession(ctx, &store.LiveSession{
		AgentName: "claude-multi-2", AgentType: "claude", WorkingDir: "/tmp",
		SessionID: "multi-sid-2", BoardName: &boardName, IsSleeping: 1,
	})

	// Kill one session (not the last)
	body := bytes.NewBufferString(`{"agent_type": "claude", "session_id": "multi-sid-1"}`)
	resp, err := http.Post(server.URL+"/api/sessions/live/claude-multi-1/kill", "application/json", body)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Board should still be paused (one sleeping session remains)
	assert.True(t, bh.IsPaused(boardName))

	// Verify one session remains
	count, err := ss.CountBoardSessions(ctx, boardName)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestKill_SleepingSession_AwakeRemains_UnpausesBoard(t *testing.T) {
	server, handler, terminal, ss := setupSessionsTestServer(t)
	ctx := context.Background()

	bh := NewBoardHandler(handler.bs)
	handler.SetBoardHandler(bh)

	boardName := "mixed-board"
	bh.SetPaused(boardName, true)

	// Register one sleeping session and one awake session on the same board
	terminal.addSession("claude-sleeping-1", "/tmp/test")
	ss.RegisterLiveSession(ctx, &store.LiveSession{
		AgentName: "claude-sleeping-1", AgentType: "claude", WorkingDir: "/tmp",
		SessionID: "sleeping-sid-1", BoardName: &boardName, IsSleeping: 1,
	})
	terminal.addSession("claude-awake-1", "/tmp/test")
	ss.RegisterLiveSession(ctx, &store.LiveSession{
		AgentName: "claude-awake-1", AgentType: "claude", WorkingDir: "/tmp",
		SessionID: "awake-sid-1", BoardName: &boardName, IsSleeping: 0,
	})

	// Board starts paused
	assert.True(t, bh.IsPaused(boardName))

	// Kill the sleeping session
	body := bytes.NewBufferString(`{"agent_type": "claude", "session_id": "sleeping-sid-1"}`)
	resp, err := http.Post(server.URL+"/api/sessions/live/claude-sleeping-1/kill", "application/json", body)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Board should be UNPAUSED — the only remaining session is awake
	assert.False(t, bh.IsPaused(boardName), "board should be unpaused when no sleeping sessions remain")

	// Verify the awake session still exists
	count, err := ss.CountBoardSessions(ctx, boardName)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestKill_SleepingSession_SkipsTmuxKill(t *testing.T) {
	server, handler, terminal, ss := setupSessionsTestServer(t)
	ctx := context.Background()

	bh := NewBoardHandler(handler.bs)
	handler.SetBoardHandler(bh)

	boardName := "tmux-skip-board"

	// Register a sleeping session (no tmux session exists for sleeping agents)
	ss.RegisterLiveSession(ctx, &store.LiveSession{
		AgentName: "claude-sleeping-1", AgentType: "claude", WorkingDir: "/tmp",
		SessionID: "sleeping-sid-1", BoardName: &boardName, IsSleeping: 1,
	})

	// Kill the sleeping session
	body := bytes.NewBufferString(`{"agent_type": "claude", "session_id": "sleeping-sid-1"}`)
	resp, err := http.Post(server.URL+"/api/sessions/live/claude-sleeping-1/kill", "application/json", body)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// KillSession should NOT have been called on the terminal — sleeping agents
	// have no tmux session, and calling KillSession risks the fuzzy FindPane
	// fallback matching a different agent's session.
	terminal.mu.Lock()
	calls := len(terminal.killSessionCalls)
	terminal.mu.Unlock()
	assert.Equal(t, 0, calls, "KillSession should not be called for sleeping agents")

	// Verify the session was removed from DB
	count, err := ss.CountBoardSessions(ctx, boardName)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestKill_SleepingBoard_AllSessionsRemoved_NoPTY(t *testing.T) {
	server, handler, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()

	bh := NewBoardHandler(handler.bs)
	handler.SetBoardHandler(bh)

	boardName := "sleeping-board"
	bh.SetPaused(boardName, true)

	// Register 2 sleeping sessions — no terminal sessions exist (sleeping team)
	ss.RegisterLiveSession(ctx, &store.LiveSession{
		AgentName: "agent-1", AgentType: "claude", WorkingDir: "/tmp",
		SessionID: "sleep-1", BoardName: &boardName, IsSleeping: 1,
	})
	ss.RegisterLiveSession(ctx, &store.LiveSession{
		AgentName: "agent-2", AgentType: "claude", WorkingDir: "/tmp",
		SessionID: "sleep-2", BoardName: &boardName, IsSleeping: 1,
	})

	// Simulate frontend killBoard: kill each session sequentially
	for _, sid := range []string{"sleep-1", "sleep-2"} {
		body := bytes.NewBufferString(fmt.Sprintf(`{"agent_type": "claude", "session_id": "%s"}`, sid))
		name := "agent-" + sid[len(sid)-1:]
		resp, err := http.Post(server.URL+"/api/sessions/live/"+name+"/kill", "application/json", body)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	}

	// All sessions should be gone from DB
	count, err := ss.CountBoardSessions(ctx, boardName)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "all sleeping sessions should be removed from DB")

	// Board pause state should be cleared
	assert.False(t, bh.IsPaused(boardName), "board pause should be cleared after all sessions killed")

	// GetSleepingBoardNames should not return this board
	sleepingBoards, err := ss.GetSleepingBoardNames(ctx)
	require.NoError(t, err)
	for _, b := range sleepingBoards {
		assert.NotEqual(t, boardName, b, "sleeping board should not appear in GetSleepingBoardNames after kill")
	}
}

// ── Demo Edition: All-or-Nothing Team Launch Tests ──────────────────────

func TestEditionLimits_TeamLaunch_ExceedsAgentCap_NoPartialLaunch(t *testing.T) {
	cfg := &config.Config{
		MaxLiveTeams:  10,
		MaxLiveAgents: 8,
	}
	server, _, _, ss := setupSessionsTestServerWithConfig(t, cfg)
	ctx := context.Background()

	// Pre-register 4 agents
	for i := 0; i < 4; i++ {
		ss.RegisterLiveSession(ctx, &store.LiveSession{
			AgentName: fmt.Sprintf("existing-%d", i), AgentType: "claude",
			WorkingDir: "/tmp", SessionID: fmt.Sprintf("existing-sid-%d", i),
		})
	}

	// Attempt to launch a 5-agent team (4+5=9 > 8, should be rejected)
	agents := make([]map[string]string, 5)
	for i := 0; i < 5; i++ {
		agents[i] = map[string]string{"name": fmt.Sprintf("new-agent-%d", i)}
	}
	body, _ := json.Marshal(map[string]interface{}{
		"board_name":  "test-team",
		"working_dir": t.TempDir(),
		"agents":      agents,
	})
	resp, err := http.Post(server.URL+"/api/sessions/launch-team", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "should reject team that would exceed agent cap")

	// Verify no partial launches: still only 4 sessions in DB
	count, err := ss.CountLiveSessions(ctx)
	require.NoError(t, err)
	assert.Equal(t, 4, count, "no new sessions should be created on rejection")
}

func TestEditionLimits_TeamLaunch_ExactlyAtCap_Succeeds(t *testing.T) {
	cfg := &config.Config{
		MaxLiveTeams:  10,
		MaxLiveAgents: 8,
	}
	server, _, _, ss := setupSessionsTestServerWithConfig(t, cfg)
	ctx := context.Background()

	// Pre-register 4 agents
	for i := 0; i < 4; i++ {
		ss.RegisterLiveSession(ctx, &store.LiveSession{
			AgentName: fmt.Sprintf("existing-%d", i), AgentType: "claude",
			WorkingDir: "/tmp", SessionID: fmt.Sprintf("existing-sid-%d", i),
		})
	}

	// Launch a 4-agent team (4+4=8, exactly at cap, should succeed)
	agents := make([]map[string]string, 4)
	for i := 0; i < 4; i++ {
		agents[i] = map[string]string{"name": fmt.Sprintf("team-agent-%d", i)}
	}
	body, _ := json.Marshal(map[string]interface{}{
		"board_name":  "ok-team",
		"working_dir": t.TempDir(),
		"agents":      agents,
	})
	resp, err := http.Post(server.URL+"/api/sessions/launch-team", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "team launch at exact cap should succeed")

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	launched, ok := result["agents"].([]interface{})
	assert.True(t, ok, "response should contain agents array")
	assert.Equal(t, 4, len(launched), "all 4 agents should be launched")
}

func TestEditionLimits_SingleLaunch_AtExactCap_Blocked(t *testing.T) {
	cfg := &config.Config{
		MaxLiveTeams:  10,
		MaxLiveAgents: 8,
	}
	server, _, _, ss := setupSessionsTestServerWithConfig(t, cfg)
	ctx := context.Background()

	// Pre-register exactly 8 agents (at the cap)
	for i := 0; i < 8; i++ {
		ss.RegisterLiveSession(ctx, &store.LiveSession{
			AgentName: fmt.Sprintf("agent-%d", i), AgentType: "claude",
			WorkingDir: "/tmp", SessionID: fmt.Sprintf("sid-%d", i),
		})
	}

	// Attempt to launch 1 more agent — should be blocked
	body, _ := json.Marshal(map[string]interface{}{
		"working_dir": t.TempDir(),
		"agent_type":  "claude",
	})
	resp, err := http.Post(server.URL+"/api/sessions/launch", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "single launch at cap should be blocked")

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	assert.Contains(t, result["error"], "Demo limit", "error message should mention demo limit")
	assert.Contains(t, result["error"], "8", "error message should mention the limit number")
}

func TestEditedFileCount_InLiveResponse(t *testing.T) {
	server, handler, terminal, _ := setupSessionsTestServer(t)
	ctx := context.Background()

	// Session IDs are parsed from tmux session names: "claude-{uuid}" → session_id = "{uuid}"
	agentName := "claude-00000000-0000-0000-0000-000000000099"
	sessionID := "00000000-0000-0000-0000-000000000099" // must match parsed UUID

	agentName2 := "claude-00000000-0000-0000-0000-000000000098"
	sessionID2 := "00000000-0000-0000-0000-000000000098"

	terminal.addSession(agentName, "/tmp/test")
	terminal.addSession(agentName2, "/tmp/test2")

	// Insert Write/Edit events with detail_json for agent 1
	writeTool := "Write"
	editTool := "Edit"
	detail1 := `{"file_path":"/tmp/test/main.go"}`
	detail2 := `{"file_path":"/tmp/test/utils.go"}`
	detail3 := `{"file_path":"/tmp/test/main.go"}` // duplicate — should not increase count

	for _, ev := range []struct {
		tool   string
		detail string
	}{
		{writeTool, detail1},
		{editTool, detail2},
		{writeTool, detail3},
	} {
		tool := ev.tool
		det := ev.detail
		_, err := handler.ts.InsertAgentEvent(ctx, &store.AgentEvent{
			AgentName:  agentName,
			SessionID:  &sessionID,
			EventType:  "tool_use",
			ToolName:   &tool,
			DetailJSON: &det,
		})
		require.NoError(t, err)
	}

	// Agent 2 has no Write/Edit events (no events at all)

	// GET /api/sessions/live
	resp, err := http.Get(server.URL + "/api/sessions/live")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var sessions []map[string]any
	err = json.NewDecoder(resp.Body).Decode(&sessions)
	require.NoError(t, err)

	// Find our agents by session_id
	var agent1Count, agent2Count float64
	var foundAgent1, foundAgent2 bool
	for _, s := range sessions {
		sid, _ := s["session_id"].(string)
		fc, _ := s["changed_file_count"].(float64)
		if sid == sessionID {
			agent1Count = fc
			foundAgent1 = true
		}
		if sid == sessionID2 {
			agent2Count = fc
			foundAgent2 = true
		}
	}

	assert.True(t, foundAgent1, "agent with edits should appear in response")
	assert.True(t, foundAgent2, "agent without edits should appear in response")
	assert.Equal(t, float64(2), agent1Count, "agent with 2 distinct edited files should show count 2")
	assert.Equal(t, float64(0), agent2Count, "agent with no edits should show count 0")
}
