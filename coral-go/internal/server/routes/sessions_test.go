package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/store"
)

func TestKnownContextWindow_UnknownMetadataIsSuppressed(t *testing.T) {
	blank := "  "
	unknown := "claude-future-unknown"
	fable := "claude-fable-5-1"
	assert.Zero(t, knownContextWindow(nil))
	assert.Zero(t, knownContextWindow(&blank))
	assert.Zero(t, knownContextWindow(&unknown))
	assert.Equal(t, 1_000_000, knownContextWindow(&fable))
}

func TestAddContextUsage_NullsUnknownAndComputesKnown(t *testing.T) {
	stale := map[string]any{"context_window": 200_000, "context_pct": 100}
	addContextUsage(stale, nil, 293_050)
	value, exists := stale["context_window"]
	assert.True(t, exists)
	assert.Nil(t, value)
	value, exists = stale["context_pct"]
	assert.True(t, exists)
	assert.Nil(t, value)

	fable := "claude-fable-5-1"
	known := map[string]any{}
	addContextUsage(known, &fable, 293_050)
	assert.Equal(t, 1_000_000, known["context_window"])
	assert.Equal(t, 29, known["context_pct"])
}

// mockSessionTerminal implements ptymanager.SessionTerminal for testing.
type mockSessionTerminal struct {
	mu               sync.Mutex
	sessions         map[string]*ptymanager.PaneInfo
	outputs          map[string]string
	sent             map[string][]string
	raw              map[string][]string // keys sent with SendRawInput
	killSessionCalls []string
}

func newMockTerminal() *mockSessionTerminal {
	return &mockSessionTerminal{
		sessions: make(map[string]*ptymanager.PaneInfo),
		outputs:  make(map[string]string),
		sent:     make(map[string][]string),
	}
}

func TestStripAgentPermissionFlags_RemovesPermissionModeForms(t *testing.T) {
	got := stripAgentPermissionFlags([]string{
		"--permission-mode", "bypassPermissions",
		"--model", "gpt-5",
		"--permission-mode=auto",
		"--sandbox", "workspace-write",
		"-a", "on-request",
		"--verbose",
	})

	require.Equal(t, []string{"--model", "gpt-5", "--verbose"}, got)
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

func (m *mockSessionTerminal) SendRawInput(_ context.Context, name string, keys []string, _, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[name]; !ok {
		return fmt.Errorf("session %q not found", name)
	}
	if m.raw == nil {
		m.raw = map[string][]string{}
	}
	m.raw[name] = append(m.raw[name], keys...)
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

func TestSessionsList_SleepingSessionContextIsExplicitNull(t *testing.T) {
	server, _, _, ss := setupSessionsTestServer(t)

	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{
		SessionID:  "00000000-0000-0000-0000-000000000010",
		AgentType:  "claude",
		AgentName:  "sleepy-agent",
		WorkingDir: "/tmp/test",
		IsSleeping: 1,
		CreatedAt:  "2026-01-01T00:00:00Z",
	}))

	resp, err := http.Get(server.URL + "/api/sessions/live")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var sessions []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&sessions))
	require.Len(t, sessions, 1)
	assert.Equal(t, true, sessions[0]["sleeping"])
	contextWindow, hasContextWindow := sessions[0]["context_window"]
	assert.True(t, hasContextWindow)
	assert.Nil(t, contextWindow)
	contextPct, hasContextPct := sessions[0]["context_pct"]
	assert.True(t, hasContextPct)
	assert.Nil(t, contextPct)
}

// A runtime session whose DB row is already marked stopped is an orphan and
// must not be listed as a live agent (it would otherwise appear with a stale
// display name in whatever group its working directory happens to match).
func TestSessionsList_HidesStoppedOrphans(t *testing.T) {
	server, _, terminal, ss := setupSessionsTestServer(t)

	liveID := "00000000-0000-0000-0000-000000000011"
	orphanID := "00000000-0000-0000-0000-000000000012"
	terminal.addSession("claude-"+liveID, "/tmp/test")
	terminal.addSession("codex-"+orphanID, "/tmp/test")

	ctx := context.Background()
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: liveID, AgentType: "claude", AgentName: "test", WorkingDir: "/tmp/test"}))
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: orphanID, AgentType: "codex", AgentName: "test", WorkingDir: "/tmp/test"}))
	require.NoError(t, ss.UnregisterLiveSession(ctx, orphanID))

	resp, err := http.Get(server.URL + "/api/sessions/live")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var sessions []map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&sessions))
	require.Len(t, sessions, 1)
	assert.Equal(t, liveID, sessions[0]["session_id"])
}

func TestSessionsList_SerializesFirstPrompt(t *testing.T) {
	server, _, terminal, _ := setupSessionsTestServer(t)
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "project")
	require.NoError(t, os.MkdirAll(projectDir, 0755))
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)

	withPromptID := "00000000-0000-0000-0000-000000000101"
	emptyID := "00000000-0000-0000-0000-000000000102"
	terminal.addSession("claude-"+withPromptID, "/tmp/with-prompt")
	terminal.addSession("claude-"+emptyID, "/tmp/empty")
	require.NoError(t, os.WriteFile(
		filepath.Join(projectDir, withPromptID+".jsonl"),
		[]byte("{\"type\":\"user\",\"message\":{\"content\":\"Name the agent from this\"}}\n"),
		0644,
	))

	resp, err := http.Get(server.URL + "/api/sessions/live")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var sessions []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&sessions))
	require.Len(t, sessions, 2)
	prompts := make(map[string]any, len(sessions))
	for _, session := range sessions {
		prompts[session["session_id"].(string)] = session["first_prompt"]
	}
	assert.Equal(t, "Name the agent from this", prompts[withPromptID])
	assert.Equal(t, "", prompts[emptyID])
}

// Two windows polling the same session's chat must each receive every new
// message; the transcript reader's cache is shared, so the server has to slice
// by the client's own `after` count rather than "new since the last read".
func TestSessionsChat_MultipleClientsEachSeeNewMessages(t *testing.T) {
	server, _, _, _ := setupSessionsTestServer(t)
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "project")
	require.NoError(t, os.MkdirAll(projectDir, 0755))
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)

	sid := "00000000-0000-0000-0000-000000000201"
	transcript := filepath.Join(projectDir, sid+".jsonl")
	userLine := func(text string) string {
		return fmt.Sprintf("{\"type\":\"user\",\"message\":{\"content\":%q}}\n", text)
	}
	require.NoError(t, os.WriteFile(transcript, []byte(userLine("one")+userLine("two")), 0644))

	chat := func(after int) ([]string, int) {
		t.Helper()
		resp, err := http.Get(fmt.Sprintf("%s/api/sessions/live/claude-%s/chat?session_id=%s&after=%d", server.URL, sid, sid, after))
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body struct {
			Messages []map[string]any `json:"messages"`
			Total    int              `json:"total"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		texts := make([]string, 0, len(body.Messages))
		for _, m := range body.Messages {
			texts = append(texts, fmt.Sprint(m["content"]))
		}
		return texts, body.Total
	}

	a, totalA := chat(0)
	b, totalB := chat(0)
	assert.Equal(t, []string{"one", "two"}, a)
	assert.Equal(t, []string{"one", "two"}, b)

	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0644)
	require.NoError(t, err)
	_, err = f.WriteString(userLine("three"))
	require.NoError(t, err)
	require.NoError(t, f.Close())

	a, totalA = chat(totalA)
	b, totalB = chat(totalB)
	assert.Equal(t, []string{"three"}, a, "first poller sees the new message")
	assert.Equal(t, []string{"three"}, b, "second poller sees it too")
	assert.Equal(t, 3, totalA)
	assert.Equal(t, 3, totalB)

	a, _ = chat(totalA)
	assert.Empty(t, a, "nothing new on the next poll")
	a, _ = chat(99)
	assert.Empty(t, a, "after beyond the end is clamped")
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

func TestResolveSession_ExactIdentityAndLifecycle(t *testing.T) {
	server, _, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	displayName := "Backend Dev"
	boardName := "popout-team"
	firstID := "11111111-2222-4333-8444-555555555551"
	secondID := "11111111-2222-4333-8444-555555555552"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID: firstID, AgentType: "claude", AgentName: "shared-folder",
		WorkingDir: "/tmp/shared", DisplayName: &displayName, BoardName: &boardName,
	}))
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID: secondID, AgentType: "codex", AgentName: "shared-folder",
		WorkingDir: "/tmp/shared",
	}))

	resp, err := http.Get(server.URL + "/api/sessions/" + firstID + "/resolve")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var got map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, firstID, got["session_id"])
	assert.Equal(t, "claude", got["agent_type"])
	assert.Equal(t, "shared-folder", got["name"])
	assert.Equal(t, "claude-"+firstID, got["tmux_session"])
	assert.Equal(t, "Backend Dev", got["display_name"])
	assert.Equal(t, "popout-team", got["board_project"])
	assert.Equal(t, "active", got["state"])
	for _, key := range []string{"auto_name", "status", "waiting_for_input", "stuck", "not_started"} {
		assert.Contains(t, got, key)
	}
}

func TestResolveSession_UnknownAndMalformed(t *testing.T) {
	server, _, _, _ := setupSessionsTestServer(t)
	unknown := "11111111-2222-4333-8444-555555555559"
	resp, err := http.Get(server.URL + "/api/sessions/" + unknown + "/resolve")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var got map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, "not_found", got["state"])
	for _, key := range []string{"agent_type", "name", "tmux_session", "display_name", "auto_name", "board_job_title", "board_project"} {
		assert.Contains(t, got, key)
		assert.Nil(t, got[key])
	}

	badResp, err := http.Get(server.URL + "/api/sessions/not-a-uuid/resolve")
	require.NoError(t, err)
	defer badResp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, badResp.StatusCode)
}

func TestExactTargetingRejectsDuplicateNameMismatchBeforeActing(t *testing.T) {
	server, _, terminal, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	firstID := "22222222-2222-4333-8444-555555555551"
	secondID := "22222222-2222-4333-8444-555555555552"
	for _, session := range []*store.LiveSession{
		{SessionID: firstID, AgentType: "claude", AgentName: "shared-folder", WorkingDir: "/tmp/shared"},
		{SessionID: secondID, AgentType: "codex", AgentName: "shared-folder", WorkingDir: "/tmp/shared"},
	} {
		require.NoError(t, ss.RegisterLiveSession(ctx, session))
	}
	terminal.addSession("shared-folder", "/tmp/shared")
	terminal.addSession("claude-"+firstID, "/tmp/shared")
	terminal.setOutput("shared-folder", "secret output")

	post := func(path, body string) *http.Response {
		t.Helper()
		resp, err := http.Post(server.URL+path, "application/json", strings.NewReader(body))
		require.NoError(t, err)
		return resp
	}

	resp := post("/api/sessions/live/shared-folder/send",
		`{"command":"wrong","agent_type":"claude","session_id":"`+secondID+`"}`)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()
	terminal.mu.Lock()
	assert.Empty(t, terminal.sent["shared-folder"])
	terminal.mu.Unlock()

	resp = post("/api/sessions/live/shared-folder/send",
		`{"command":"right","agent_type":"claude","session_id":"`+firstID+`"}`)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
	resp = post("/api/sessions/live/claude-"+firstID+"/send",
		`{"command":"canonical","agent_type":"claude","session_id":"`+firstID+`"}`)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	for _, endpoint := range []string{"capture", "poll"} {
		mismatch, err := http.Get(server.URL + "/api/sessions/live/shared-folder/" + endpoint +
			"?agent_type=claude&session_id=" + secondID)
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadRequest, mismatch.StatusCode)
		mismatch.Body.Close()
	}
	detail, err := http.Get(server.URL + "/api/sessions/live/shared-folder" +
		"?agent_type=claude&session_id=" + secondID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, detail.StatusCode)
	detail.Body.Close()

	keys := post("/api/sessions/live/shared-folder/keys",
		`{"keys":["Enter"],"agent_type":"claude","session_id":"`+secondID+`"}`)
	assert.Equal(t, http.StatusBadRequest, keys.StatusCode)
	keys.Body.Close()

	unknown := post("/api/sessions/live/shared-folder/resize",
		`{"columns":120,"agent_type":"claude","session_id":"22222222-2222-4333-8444-555555555559"}`)
	assert.Equal(t, http.StatusNotFound, unknown.StatusCode)
	unknown.Body.Close()
}

func TestSessionsChangesDiff_ReturnsTrackedAndUntrackedChanges(t *testing.T) {
	server, _, terminal, ss := setupSessionsTestServer(t)
	repo := t.TempDir()

	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
	}
	runGit("init")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Coral Test")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("before\n"), 0644))
	runGit("add", "tracked.txt")
	runGit("commit", "-m", "initial")

	require.NoError(t, os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("after\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("new file\n"), 0644))

	sessionID := "00000000-0000-0000-0000-000000000123"
	name := "codex-" + sessionID
	terminal.addSession(name, repo)
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{
		SessionID: sessionID, AgentType: "codex", AgentName: name, WorkingDir: repo,
	}))
	resp, err := http.Get(server.URL + "/api/sessions/" + sessionID + "/changes")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/x-diff; charset=utf-8", resp.Header.Get("Content-Type"))
	assert.Equal(t, `attachment; filename="changes.diff"`, resp.Header.Get("Content-Disposition"))
	realRepo, err := filepath.EvalSymlinks(repo)
	require.NoError(t, err)
	assert.Equal(t, realRepo, resp.Header.Get("X-Coral-Working-Directory"))
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "diff --git a/tracked.txt b/tracked.txt")
	assert.Contains(t, string(body), "-before")
	assert.Contains(t, string(body), "+after")
	assert.Contains(t, string(body), "untracked.txt")
	assert.Contains(t, string(body), "+new file")
}

func TestSessionStatus_WaitingThenFinished(t *testing.T) {
	server, _, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	sessionID := "00000000-0000-0000-0000-000000000125"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID: sessionID, AgentType: "codex", AgentName: "status-test", WorkingDir: t.TempDir(),
	}))

	ts := store.NewTaskStore(ss.DB())
	_, err := ts.InsertAgentEvent(ctx, &store.AgentEvent{
		// A real request string: only a request made directly to the user
		// is waiting_for_input, not any notification.
		AgentName: "status-test", SessionID: &sessionID, EventType: "notification", Summary: permissionNote,
	})
	require.NoError(t, err)

	resp, err := http.Get(server.URL + "/api/sessions/" + sessionID + "/status")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var waiting map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&waiting))
	assert.Equal(t, "waiting_for_input", waiting["state"])
	assert.Equal(t, true, waiting["waiting_for_input"])
	assert.Equal(t, false, waiting["finished"])

	require.NoError(t, ss.UnregisterLiveSession(ctx, sessionID))
	finishedResp, err := http.Get(server.URL + "/api/sessions/" + sessionID + "/status")
	require.NoError(t, err)
	defer finishedResp.Body.Close()
	require.Equal(t, http.StatusOK, finishedResp.StatusCode)
	var finished map[string]any
	require.NoError(t, json.NewDecoder(finishedResp.Body).Decode(&finished))
	assert.Equal(t, "finished", finished["state"])
	assert.Equal(t, true, finished["finished"])
	assert.Equal(t, false, finished["waiting_for_input"])
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

func TestSessionsKill_PersistsChangesBeforeTermination(t *testing.T) {
	coralDir := t.TempDir()
	server, _, terminal, ss := setupSessionsTestServerWithConfig(t, config.Load(coralDir))
	repo := t.TempDir()

	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
	}
	runGit("init")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Coral Test")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "file.txt"), []byte("before\n"), 0644))
	runGit("add", "file.txt")
	runGit("commit", "-m", "initial")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "file.txt"), []byte("after\n"), 0644))

	sessionID := "00000000-0000-0000-0000-000000000124"
	name := "codex-" + sessionID
	terminal.addSession(name, repo)
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{
		SessionID: sessionID, AgentType: "codex", AgentName: name, WorkingDir: repo,
	}))

	resp := postJSON(t, server.URL+"/api/sessions/live/"+name+"/kill", map[string]string{
		"agent_type": "codex", "session_id": sessionID,
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var result map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Empty(t, result["changes_artifact_error"])
	assert.Equal(t, filepath.Join(coralDir, "artifacts", "sessions", sessionID, "changes.diff"), result["changes_artifact"])
	assert.False(t, terminal.HasSession(context.Background(), name))

	download, err := http.Get(server.URL + "/api/sessions/" + sessionID + "/changes")
	require.NoError(t, err)
	defer download.Body.Close()
	assert.Equal(t, http.StatusOK, download.StatusCode)
	data, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	assert.Contains(t, string(data), "+after")
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

func TestSessionsLaunchTeam_StripsForeignPermissionFlags(t *testing.T) {
	cfg := &config.Config{LogDir: t.TempDir()}
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	terminal := newMockTerminal()
	handler := NewSessionsHandler(db, cfg, nil, terminal, nil)
	body, err := json.Marshal(map[string]any{
		"board_name":  "mixed-team",
		"working_dir": t.TempDir(),
		"agent_type":  "codex",
		"flags":       []string{"--dangerously-bypass-approvals-and-sandbox", "--verbose"},
		"agents": []map[string]any{
			{"name": "Claude Worker", "agent_type": "claude"},
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/launch-team", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.LaunchTeam(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	commands := strings.Join(terminal.sentCommands(), "\n")
	require.NotEmpty(t, commands)
	assert.NotContains(t, commands, "--dangerously-bypass-approvals-and-sandbox")
	assert.Contains(t, commands, "--verbose")
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

func TestSessionsSetNameColor(t *testing.T) {
	server, _, terminal, ss := setupSessionsTestServer(t)

	terminal.addSession("claude-test-color", "/tmp/test")
	ctx := context.Background()
	ss.RegisterLiveSession(ctx, &store.LiveSession{AgentName: "claude-test-color", AgentType: "claude", WorkingDir: "/tmp/test", SessionID: "test-color-123"})

	put := func(body string) int {
		req, err := http.NewRequest(http.MethodPut, server.URL+"/api/sessions/live/claude-test-color/name-color", bytes.NewBufferString(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	colors := func() map[string]string {
		m, err := ss.GetNameColors(ctx, []string{"test-color-123"})
		require.NoError(t, err)
		return m
	}

	assert.Equal(t, http.StatusOK, put(`{"color": "#A3BE8C", "session_id": "test-color-123"}`))
	assert.Equal(t, "#a3be8c", colors()["test-color-123"], "stored lower-cased")

	// Anything but #rrggbb is rejected: the value lands in a style attribute.
	for _, bad := range []string{"red", "#abc", "#a3be8c;background:url(x)", "url(x)"} {
		assert.Equal(t, http.StatusBadRequest, put(`{"color": "`+bad+`", "session_id": "test-color-123"}`), bad)
	}
	assert.Equal(t, "#a3be8c", colors()["test-color-123"], "rejected values leave the colour unchanged")

	assert.Equal(t, http.StatusOK, put(`{"color": "", "session_id": "test-color-123"}`))
	assert.Empty(t, colors(), "empty colour clears the override")

	assert.Equal(t, http.StatusBadRequest, put(`{"color": "#a3be8c"}`), "session_id required")
}

func TestSessionsResolvePath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	server, _, _, ss := setupSessionsTestServer(t)

	// repo/            <- git root
	//   README.md
	//   app/           <- agent working directory
	//     main.go
	// outside.txt      <- outside the repo
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	app := filepath.Join(root, "app")
	require.NoError(t, os.MkdirAll(app, 0o755))
	require.NoError(t, exec.Command("git", "init", "-q", root).Run())
	require.NoError(t, os.WriteFile(filepath.Join(root, "README.md"), []byte("hi"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(app, "main.go"), []byte("package main"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(base, "outside.txt"), []byte("secret"), 0o644))

	ctx := context.Background()
	ss.RegisterLiveSession(ctx, &store.LiveSession{AgentName: "claude-resolve", AgentType: "claude", WorkingDir: app, SessionID: "resolve-1"})

	resolve := func(ref string) (int, map[string]any) {
		q := url.Values{"filepath": {ref}, "session_id": {"resolve-1"}}
		resp, err := http.Get(server.URL + "/api/sessions/live/claude-resolve/resolve-path?" + q.Encode())
		require.NoError(t, err)
		defer resp.Body.Close()
		var body map[string]any
		json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}

	for ref, want := range map[string]string{
		"app/main.go":                 "app/main.go", // repo-relative
		"main.go":                     "app/main.go", // relative to the agent's folder
		"app/main.go:12":              "app/main.go", // with a line
		"main.go:3-9":                 "app/main.go", // with a range
		filepath.Join(app, "main.go"): "app/main.go", // absolute
		"README.md":                   "README.md",
	} {
		code, body := resolve(ref)
		assert.Equal(t, http.StatusOK, code, ref)
		assert.Equal(t, want, body["filepath"], ref)
	}
	_, body := resolve("app/main.go:12")
	assert.Equal(t, float64(12), body["line"])

	// Nothing outside the repo, and nothing that does not exist
	for _, ref := range []string{"../outside.txt", "../../outside.txt", filepath.Join(base, "outside.txt"), "missing.go", "app"} {
		code, _ := resolve(ref)
		assert.Equal(t, http.StatusNotFound, code, ref)
	}
}

func TestSessionsPendingTool(t *testing.T) {
	server, _, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	ss.RegisterLiveSession(ctx, &store.LiveSession{AgentName: "claude-pending", AgentType: "claude", WorkingDir: "/tmp/test", SessionID: "pend-1"})
	base := server.URL + "/api/sessions/live/claude-pending"

	post := func(path, body string) int {
		resp, err := http.Post(base+path, "application/json", bytes.NewBufferString(body))
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	get := func() map[string]any {
		resp, err := http.Get(base + "/pending-tool?session_id=pend-1")
		require.NoError(t, err)
		defer resp.Body.Close()
		var body map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		p, _ := body["pending"].(map[string]any)
		return p
	}

	assert.Nil(t, get(), "nothing pending initially")

	// A question is recorded with its options; bulky fields are never kept
	assert.Equal(t, http.StatusOK, post("/pending-tool", `{"session_id":"pend-1","tool_use_id":"t1","tool_name":"AskUserQuestion",
		"tool_input":{"questions":[{"question":"Which?","options":[{"label":"A"},{"label":"B"}]}],"content":"x"}}`))
	p := get()
	require.NotNil(t, p)
	assert.Equal(t, "AskUserQuestion", p["tool_name"])
	in := p["input"].(map[string]any)
	assert.NotNil(t, in["questions"])
	assert.Nil(t, in["content"], "fields the chat does not show are dropped")

	// Another tool's completion does not clear it; its own does
	post("/events", `{"event_type":"tool_use","summary":"Ran: ls","session_id":"pend-1","tool_use_id":"other"}`)
	assert.NotNil(t, get())
	post("/events", `{"event_type":"tool_use","summary":"Asked","session_id":"pend-1","tool_use_id":"t1"}`)
	assert.Nil(t, get(), "the tool's own PostToolUse clears it")

	// A turn ending clears whatever is pending
	post("/pending-tool", `{"session_id":"pend-1","tool_use_id":"t2","tool_name":"Bash","tool_input":{"command":"rm -rf build"}}`)
	assert.Equal(t, "rm -rf build", get()["input"].(map[string]any)["command"])
	post("/events", `{"event_type":"stop","summary":"Agent stopped: unknown","session_id":"pend-1"}`)
	assert.Nil(t, get())

	assert.Equal(t, http.StatusBadRequest, post("/pending-tool", `{"tool_name":"Bash"}`), "session_id required")
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

func TestSessionsTasks_SameTitleReusesOpenTask(t *testing.T) {
	server, _, terminal, ss := setupSessionsTestServer(t)
	terminal.addSession("claude-task-dedupe", "/tmp/test")
	ctx := context.Background()
	ss.RegisterLiveSession(ctx, &store.LiveSession{AgentName: "claude-task-dedupe", AgentType: "claude", WorkingDir: "/tmp/test", SessionID: "dedupe-1"})
	base := server.URL + "/api/sessions/live/claude-task-dedupe/tasks"

	create := func(title string) map[string]any {
		resp, err := http.Post(base, "application/json", bytes.NewBufferString(`{"title": "`+title+`", "session_id": "dedupe-1"}`))
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var task map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&task))
		return task
	}

	// The operator creates a task; the agent's TaskCreate for it (same title) reuses it
	first := create("Add a battle log")
	again := create("Add a battle log")
	assert.Equal(t, first["id"], again["id"], "an open task with the same title is reused, not duplicated")
	other := create("Write the engine tests")
	assert.NotEqual(t, first["id"], other["id"])

	// Once completed, the same title starts a new task
	req, _ := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/%v", base, first["id"]), bytes.NewBufferString(`{"completed": 1}`)) // the test router maps UpdateTask to PUT
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	fresh := create("Add a battle log")
	assert.NotEqual(t, first["id"], fresh["id"], "a completed task is not reused")
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
	server, handler, terminal, ss := setupSessionsTestServer(t)

	terminal.addSession("claude-test-events", "/tmp/test")
	ctx := context.Background()
	ss.RegisterLiveSession(ctx, &store.LiveSession{AgentName: "claude-test-events", AgentType: "claude", WorkingDir: "/tmp/test", SessionID: "test-evt-123"})
	handler.pending.set("test-evt-123", pendingTool{ToolUseID: "tool-1", ToolName: "Read", At: time.Now().Add(-1500 * time.Millisecond)})

	// Create event
	body := bytes.NewBufferString(`{"event_type": "tool_use", "summary": "Read file", "session_id": "test-evt-123", "tool_use_id": "tool-1"}`)
	resp, err := http.Post(server.URL+"/api/sessions/live/claude-test-events/events", "application/json", body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var created store.AgentEvent
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	resp.Body.Close()
	require.NotNil(t, created.DurationMs)
	assert.GreaterOrEqual(t, *created.DurationMs, int64(1400))

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
	r.Get("/api/sessions/{sessionID}/resolve", handler.ResolveSession)
	r.Get("/api/sessions/{sessionID}/status", handler.SessionStatus)
	r.Get("/api/sessions/{sessionID}/changes", handler.SessionChanges)
	r.Get("/api/sessions/{sessionID}/changes.diff", handler.SessionChangesArtifact)
	r.Get("/api/sessions/live/{name}", handler.Detail)
	r.Get("/api/sessions/live/{name}/capture", handler.Capture)
	r.Get("/api/sessions/live/{name}/chat", handler.Chat)
	r.Get("/api/sessions/live/{name}/poll", handler.Poll)
	r.Get("/api/sessions/live/{name}/changes.diff", handler.ChangesDiff)
	r.Post("/api/sessions/live/{name}/send", handler.Send)
	r.Post("/api/sessions/live/{name}/goal", handler.RequestGoal)
	r.Get("/api/sessions/live/{name}/files", handler.Files)
	r.Post("/api/sessions/live/{name}/keys", handler.Keys)
	r.Post("/api/sessions/live/{name}/resize", handler.Resize)
	r.Post("/api/sessions/live/{name}/kill", handler.Kill)
	r.Post("/api/sessions/live/{name}/set-display-name", handler.SetDisplayName)
	r.Post("/api/sessions/live/{name}/set-icon", handler.SetIcon)
	r.Put("/api/sessions/live/{name}/name-color", handler.SetNameColor)
	r.Get("/api/sessions/live/{name}/resolve-path", handler.ResolvePath)
	r.Get("/api/sessions/live/{name}/pending-tool", handler.GetPendingTool)
	r.Post("/api/sessions/live/{name}/pending-tool", handler.SetPendingTool)
	r.Post("/api/sessions/live/{name}/events", handler.CreateEvent)
	r.Get("/api/sessions/live/{name}/prompt-options", handler.PromptOptions)
	r.Post("/api/sessions/live/{name}/answer-prompt", handler.AnswerPrompt)
	r.Post("/api/sessions/launch", handler.Launch)
	r.Post("/api/sessions/launch-team", handler.LaunchTeam)

	// Task routes
	r.Get("/api/sessions/live/{name}/tasks", handler.ListTasks)
	r.Get("/api/agent/tasks", handler.ListAgentTasksForAgent)
	r.Post("/api/agent/tasks", handler.AddAgentTaskForAgent)
	r.Post("/api/agent/tasks/claim", handler.ClaimAgentTaskForAgent)
	r.Post("/api/agent/tasks/current", handler.CurrentAgentTaskForAgent)
	r.Post("/api/agent/tasks/{taskID}/complete", handler.CompleteAgentTaskForAgent)
	r.Post("/api/agent/tasks/{taskID}/cancel", handler.CancelAgentTaskForAgent)
	r.Post("/api/sessions/live/{name}/tasks", handler.CreateTask)
	r.Get("/api/sessions/live/{name}/subagents", handler.ListSubagents)
	r.Get("/api/sessions/live/{name}/subagents/{subagentID}", handler.GetSubagent)
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

// ── Goal generation ─────────────────────────────────────────────────────

type fakeGoalRequester struct{ requested []string }

func (f *fakeGoalRequester) RequestGoal(sessionID string) {
	f.requested = append(f.requested, sessionID)
}

func TestRequestGoal_AsksTheGeneratorAndNeverTypesIntoTheAgent(t *testing.T) {
	server, handler, terminal, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	sid := "33333333-3333-4333-8444-555555555551"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "coral-go", WorkingDir: "/tmp/g"}))
	terminal.addSession("claude-"+sid, "/tmp/g")
	body := `{"agent_type":"claude","session_id":"` + sid + `"}`
	post := func() *http.Response {
		t.Helper()
		resp, err := http.Post(server.URL+"/api/sessions/live/coral-go/goal", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}

	assert.Equal(t, http.StatusConflict, post().StatusCode, "no generator running")

	gen := &fakeGoalRequester{}
	handler.SetGoalRequester(gen)
	resp := post()
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	var got map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, true, got["goal_pending"])
	assert.Equal(t, []string{sid}, gen.requested)

	require.NoError(t, ss.SetSetting(ctx, "auto_goals", "false"))
	assert.Equal(t, http.StatusConflict, post().StatusCode, "turned off")

	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	for name, sent := range terminal.sent {
		assert.Empty(t, sent, "nothing typed into %s", name)
	}
}

func TestCreateEvent_TypedGoalIsMarkedAsTheOperators(t *testing.T) {
	server, handler, _, _ := setupSessionsTestServer(t)
	sid := "33333333-3333-4333-8444-555555555552"
	resp, err := http.Post(server.URL+"/api/sessions/live/coral-go/events", "application/json",
		strings.NewReader(`{"event_type":"goal","summary":"My goal","session_id":"`+sid+`"}`))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	ev, err := handler.ts.GetLatestGoalEvent(context.Background(), sid)
	require.NoError(t, err)
	require.NotNil(t, ev)
	require.NotNil(t, ev.DetailJSON)
	assert.JSONEq(t, `{"source":"user"}`, *ev.DetailJSON)
}

func TestResolveGoal_NewestGoalEventWinsOverThePulseLine(t *testing.T) {
	assert.Equal(t, "Short auto goal", resolveGoal("Long PULSE sentence from the start", "Short auto goal"))
	assert.Equal(t, "PULSE only", resolveGoal("PULSE only", ""))
	assert.Equal(t, "", resolveGoal("", ""))
}

func TestTrackStatusSummary_DoesNotReplayAKnownPulseLineAfterRestart(t *testing.T) {
	_, handler, _, _ := setupSessionsTestServer(t)
	ctx := context.Background()
	sid := "33333333-3333-4333-8444-555555555553"
	s := sid
	_, err := handler.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "coral-go", SessionID: &s, EventType: "goal", Summary: "Old PULSE line"})
	require.NoError(t, err)
	auto := `{"source":"auto"}`
	_, err = handler.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "coral-go", SessionID: &s, EventType: "goal", Summary: "Newer auto goal", DetailJSON: &auto})
	require.NoError(t, err)

	// A fresh process sees the old PULSE line in the log for the first time.
	handler.trackStatusSummary(ctx, "coral-go", "", "Old PULSE line", sid)
	ev, err := handler.ts.GetLatestGoalEvent(ctx, sid)
	require.NoError(t, err)
	assert.Equal(t, "Newer auto goal", ev.Summary)

	// A genuinely new PULSE line is recorded and becomes the goal.
	handler.trackStatusSummary(ctx, "coral-go", "", "New PULSE line", sid)
	ev, err = handler.ts.GetLatestGoalEvent(ctx, sid)
	require.NoError(t, err)
	assert.Equal(t, "New PULSE line", ev.Summary)
}

// ── Changed files cache ─────────────────────────────────────────────────

func gitRepoWithOneChange(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run("init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644))
	run("add", "a.txt")
	run("commit", "-q", "-m", "init")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644))
	return dir
}

func TestFiles_RecomputesAStaleCacheInsteadOfServingIt(t *testing.T) {
	server, handler, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	sid := "44444444-4444-4444-8444-555555555551"
	repo := gitRepoWithOneChange(t)
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "coral-go", WorkingDir: repo}))

	// A cached list from before a rebase: 812 files.
	stale := make([]store.ChangedFile, 812)
	for i := range stale {
		stale[i] = store.ChangedFile{Filepath: fmt.Sprintf("old/%d.go", i), Status: "M"}
	}
	s := sid
	require.NoError(t, handler.gs.ReplaceChangedFiles(ctx, "coral-go", repo, stale, &s, "branch_point"))
	get := func() []any {
		t.Helper()
		resp, err := http.Get(server.URL + "/api/sessions/live/coral-go/files?session_id=" + sid)
		require.NoError(t, err)
		defer resp.Body.Close()
		var body map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		files, _ := body["files"].([]any)
		return files
	}

	assert.Len(t, get(), 812, "a fresh cache is served as is")

	old := time.Now().Add(-time.Hour).UTC().Format(store.ISOFormat)
	_, err := handler.db.ExecContext(ctx, `UPDATE git_changed_files SET recorded_at = ? WHERE session_id = ?`, old, sid)
	require.NoError(t, err)
	files := get()
	require.Len(t, files, 1, "an hour-old cache is recomputed from git")
	assert.Equal(t, "a.txt", files[0].(map[string]any)["filepath"])

	cached, found, err := handler.gs.GetChangedFiles(ctx, "coral-go", &s, "branch_point")
	require.NoError(t, err)
	require.True(t, found)
	assert.Len(t, cached, 1, "and the cache is replaced")
}

func TestFilesCacheMaxAge(t *testing.T) {
	_, handler, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	handler.cfg.GitPollerIntervalS = 120
	assert.Equal(t, 4*time.Minute, handler.filesCacheMaxAge(ctx, "branch_point"), "two default poll intervals")
	assert.Equal(t, filesCacheFloor, handler.filesCacheMaxAge(ctx, "previous_commit"), "the poller never refreshes other modes")

	require.NoError(t, ss.SetSetting(ctx, "git_poll_interval_s", "5"))
	assert.Equal(t, filesCacheFloor, handler.filesCacheMaxAge(ctx, "branch_point"), "2 x the 15 s minimum")
	require.NoError(t, ss.SetSetting(ctx, "git_poll_interval_s", "600"))
	assert.Equal(t, 20*time.Minute, handler.filesCacheMaxAge(ctx, "branch_point"))
	require.NoError(t, ss.SetSetting(ctx, "git_poll_interval_s", "0"))
	assert.Equal(t, filesCacheFloor, handler.filesCacheMaxAge(ctx, "branch_point"), "polling off")
}

func TestFilesCacheFresh(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	at := func(ago time.Duration) []store.ChangedFile {
		return []store.ChangedFile{{RecordedAt: now.Add(-ago).Format(store.ISOFormat)}}
	}
	assert.True(t, filesCacheFresh(at(time.Minute), 2*time.Minute, now))
	assert.False(t, filesCacheFresh(at(3*time.Minute), 2*time.Minute, now))
	assert.False(t, filesCacheFresh(nil, time.Hour, now))
	assert.False(t, filesCacheFresh([]store.ChangedFile{{RecordedAt: "garbage"}}, time.Hour, now))
}
