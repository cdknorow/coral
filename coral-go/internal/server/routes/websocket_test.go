package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"

	"github.com/cdknorow/coral/internal/ptymanager"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store"
)

func setupTestServer(t *testing.T) (*httptest.Server, *SessionsHandler) {
	t.Helper()

	cfg := &config.Config{
		WSPollIntervalS: 1,
		LogDir:          t.TempDir(),
	}

	dbPath := t.TempDir() + "/test.db"
	db, err := store.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	ptyBackend := ptymanager.NewPTYBackend()
	terminal := ptymanager.NewPTYSessionTerminal(ptyBackend)
	handler := NewSessionsHandler(db, cfg, nil, terminal, nil)

	r := chi.NewRouter()
	r.Get("/ws/coral", handler.WSCoral)
	r.Get("/ws/terminal/{name}", handler.WSTerminal)

	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	return server, handler
}

func TestWSCoral_FirstMessageIsFullUpdate(t *testing.T) {
	server, _ := setupTestServer(t)

	// Convert http:// to ws://
	wsURL := "ws" + server.URL[4:] + "/ws/coral"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.CloseNow()

	// Read first message — should be coral_update
	var msg map[string]json.RawMessage
	err = wsjson.Read(ctx, conn, &msg)
	require.NoError(t, err)

	var msgType string
	json.Unmarshal(msg["type"], &msgType)
	assert.Equal(t, "coral_update", msgType)

	// Should have sessions array (even if empty)
	assert.Contains(t, msg, "sessions")
	assert.Contains(t, msg, "active_runs")

	conn.Close(websocket.StatusNormalClosure, "done")
}

func TestWSCoral_SubsequentDiffsOnlyOnChange(t *testing.T) {
	server, _ := setupTestServer(t)

	wsURL := "ws" + server.URL[4:] + "/ws/coral"
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.CloseNow()

	// Read first message (full update)
	var msg1 map[string]json.RawMessage
	err = wsjson.Read(ctx, conn, &msg1)
	require.NoError(t, err)

	var msgType string
	json.Unmarshal(msg1["type"], &msgType)
	assert.Equal(t, "coral_update", msgType)

	// Wait for second poll — with no agents running, there should be no diff
	// (or a diff with empty changes). Use a short timeout.
	readCtx, readCancel := context.WithTimeout(ctx, 3*time.Second)
	defer readCancel()

	var msg2 map[string]json.RawMessage
	err = wsjson.Read(readCtx, conn, &msg2)
	if err != nil {
		// Timeout is expected — no diff sent when nothing changed
		t.Log("No diff sent (expected — no changes)")
	} else {
		json.Unmarshal(msg2["type"], &msgType)
		assert.Equal(t, "coral_diff", msgType)
	}

	conn.Close(websocket.StatusNormalClosure, "done")
}

func TestWSCoral_NewSessionDiffIncludesFirstPrompt(t *testing.T) {
	server, handler := setupTestServer(t)
	wsURL := "ws" + server.URL[4:] + "/ws/coral"
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.CloseNow()

	var initial map[string]json.RawMessage
	require.NoError(t, wsjson.Read(ctx, conn, &initial))

	projectsDir := t.TempDir()
	projectDir := filepath.Join(projectsDir, "project")
	require.NoError(t, os.MkdirAll(projectDir, 0755))
	t.Setenv("CLAUDE_PROJECTS_DIR", projectsDir)
	sessionID := "00000000-0000-0000-0000-000000000777"
	name := "claude-" + sessionID
	require.NoError(t, os.WriteFile(
		filepath.Join(projectDir, sessionID+".jsonl"),
		[]byte("{\"type\":\"user\",\"message\":{\"content\":\"Investigate the websocket payload\"}}\n"),
		0644,
	))
	terminal := newMockTerminal()
	terminal.addSession(name, t.TempDir())
	handler.terminal = terminal

	var diff struct {
		Type    string           `json:"type"`
		Changed []map[string]any `json:"changed"`
	}
	require.NoError(t, wsjson.Read(ctx, conn, &diff))
	require.Equal(t, "coral_diff", diff.Type)
	require.Len(t, diff.Changed, 1)
	assert.Equal(t, sessionID, diff.Changed[0]["session_id"])
	assert.Equal(t, "", diff.Changed[0]["summary"])
	assert.Equal(t, "Investigate the websocket payload", diff.Changed[0]["first_prompt"])
}

// --- Sleeping sessions in WebSocket ---

func TestWSCoral_SleepingSessionIncluded(t *testing.T) {
	server, handler := setupTestServer(t)

	ctx := context.Background()

	// Register a sleeping session in the DB
	ss := store.NewSessionStore(handler.db)
	boardName := "test-board"
	err := ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID:  "sleep-session-001",
		AgentType:  "claude",
		AgentName:  "sleepy-agent",
		WorkingDir: "/tmp/test",
		IsSleeping: 1,
		BoardName:  &boardName,
		CreatedAt:  "2026-01-01T00:00:00Z",
	})
	require.NoError(t, err)

	// Connect to WebSocket
	wsURL := "ws" + server.URL[4:] + "/ws/coral"
	wsCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(wsCtx, wsURL, nil)
	require.NoError(t, err)
	defer conn.CloseNow()

	// Read first message
	var msg map[string]json.RawMessage
	err = wsjson.Read(wsCtx, conn, &msg)
	require.NoError(t, err)

	var msgType string
	json.Unmarshal(msg["type"], &msgType)
	assert.Equal(t, "coral_update", msgType)

	// Parse sessions
	var sessions []map[string]any
	json.Unmarshal(msg["sessions"], &sessions)

	// Should include the sleeping session
	found := false
	for _, s := range sessions {
		if s["session_id"] == "sleep-session-001" {
			found = true
			assert.Equal(t, true, s["sleeping"])
			assert.Equal(t, "Sleeping", s["status"])
			contextWindow, hasContextWindow := s["context_window"]
			assert.True(t, hasContextWindow)
			assert.Nil(t, contextWindow)
			contextPct, hasContextPct := s["context_pct"]
			assert.True(t, hasContextPct)
			assert.Nil(t, contextPct)
			assert.Equal(t, nil, s["tmux_session"])
			assert.Equal(t, "claude", s["agent_type"])
			assert.Equal(t, "sleepy-agent", s["name"])
			break
		}
	}
	assert.True(t, found, "sleeping session should be included in WS update")

	conn.Close(websocket.StatusNormalClosure, "done")
}

func TestWSCoral_SleepingSessionFields(t *testing.T) {
	server, handler := setupTestServer(t)

	ctx := context.Background()

	// Register sleeping session with display name and board
	ss := store.NewSessionStore(handler.db)
	boardName := "my-project"
	displayName := "My Agent"
	err := ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID:   "sleep-fields-001",
		AgentType:   "gemini",
		AgentName:   "field-agent",
		WorkingDir:  "/tmp/fields",
		IsSleeping:  1,
		BoardName:   &boardName,
		DisplayName: &displayName,
		CreatedAt:   "2026-01-01T00:00:00Z",
	})
	require.NoError(t, err)

	wsURL := "ws" + server.URL[4:] + "/ws/coral"
	wsCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(wsCtx, wsURL, nil)
	require.NoError(t, err)
	defer conn.CloseNow()

	var msg map[string]json.RawMessage
	err = wsjson.Read(wsCtx, conn, &msg)
	require.NoError(t, err)

	var sessions []map[string]any
	json.Unmarshal(msg["sessions"], &sessions)

	for _, s := range sessions {
		if s["session_id"] == "sleep-fields-001" {
			assert.Equal(t, true, s["sleeping"])
			assert.Equal(t, "Sleeping", s["status"])
			assert.Equal(t, "My Agent", s["display_name"])
			assert.Equal(t, "my-project", s["board_project"])
			assert.Equal(t, false, s["waiting_for_input"])
			assert.Equal(t, false, s["done"])
			assert.Equal(t, false, s["working"])
			break
		}
	}

	conn.Close(websocket.StatusNormalClosure, "done")
}

func TestWSCoral_NonSleepingSessionExcludedFromSleepingList(t *testing.T) {
	server, handler := setupTestServer(t)

	ctx := context.Background()

	// Register a NON-sleeping session (IsSleeping=0) — should NOT appear
	// since there's no live tmux/PTY agent either
	ss := store.NewSessionStore(handler.db)
	err := ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID:  "awake-session-001",
		AgentType:  "claude",
		AgentName:  "awake-agent",
		WorkingDir: "/tmp/awake",
		IsSleeping: 0,
		CreatedAt:  "2026-01-01T00:00:00Z",
	})
	require.NoError(t, err)

	wsURL := "ws" + server.URL[4:] + "/ws/coral"
	wsCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(wsCtx, wsURL, nil)
	require.NoError(t, err)
	defer conn.CloseNow()

	var msg map[string]json.RawMessage
	err = wsjson.Read(wsCtx, conn, &msg)
	require.NoError(t, err)

	var sessions []map[string]any
	json.Unmarshal(msg["sessions"], &sessions)

	// Non-sleeping session without a live agent should NOT appear
	for _, s := range sessions {
		if s["session_id"] == "awake-session-001" {
			t.Error("non-sleeping session without live agent should not appear in WS update")
		}
	}

	conn.Close(websocket.StatusNormalClosure, "done")
}

func TestWSCoral_SleepingSessionNotDuplicated(t *testing.T) {
	server, handler := setupTestServer(t)

	ctx := context.Background()

	// Register a sleeping session
	ss := store.NewSessionStore(handler.db)
	err := ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID:  "dedup-session-001",
		AgentType:  "claude",
		AgentName:  "dedup-agent",
		WorkingDir: "/tmp/dedup",
		IsSleeping: 1,
		CreatedAt:  "2026-01-01T00:00:00Z",
	})
	require.NoError(t, err)

	wsURL := "ws" + server.URL[4:] + "/ws/coral"
	wsCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(wsCtx, wsURL, nil)
	require.NoError(t, err)
	defer conn.CloseNow()

	var msg map[string]json.RawMessage
	err = wsjson.Read(wsCtx, conn, &msg)
	require.NoError(t, err)

	var sessions []map[string]any
	json.Unmarshal(msg["sessions"], &sessions)

	// Count how many times this session appears
	count := 0
	for _, s := range sessions {
		if s["session_id"] == "dedup-session-001" {
			count++
		}
	}
	assert.Equal(t, 1, count, "sleeping session should appear exactly once (no duplicates)")

	conn.Close(websocket.StatusNormalClosure, "done")
}

func TestWSTerminal_RejectsMissingPane(t *testing.T) {
	server, _ := setupTestServer(t)

	wsURL := "ws" + server.URL[4:] + "/ws/terminal/nonexistent?session_id=fake-id&agent_type=claude"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		// Expected — server should reject when pane not found
		t.Log("Connection rejected (expected — pane not found)")
		return
	}
	defer conn.CloseNow()

	// If accepted, it should close quickly with an error status
	_, _, err = conn.Read(ctx)
	assert.Error(t, err, "should close when pane not found")
}

func TestWSTerminal_RejectsSessionTupleMismatchWithPolicyViolation(t *testing.T) {
	server, handler := setupTestServer(t)
	sessionID := "33333333-2222-4333-8444-555555555551"
	require.NoError(t, store.NewSessionStore(handler.db).RegisterLiveSession(context.Background(), &store.LiveSession{
		SessionID: sessionID, AgentType: "claude", AgentName: "shared-folder", WorkingDir: "/tmp/shared",
	}))

	wsURL := "ws" + server.URL[4:] + "/ws/terminal/codex-" + sessionID +
		"?agent_type=claude&session_id=" + sessionID
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer conn.CloseNow()

	_, _, err = conn.Read(ctx)
	status := websocket.CloseStatus(err)
	assert.Equal(t, websocket.StatusPolicyViolation, status)
}

// ── WebSocket Origin Validation Tests ────────────────────────────────

// TestWSAcceptOptions_LocalhostPatterns verifies that wsAcceptOptions always
// includes localhost variants so local browser connections are accepted.
func TestWSAcceptOptions_LocalhostPatterns(t *testing.T) {
	_, handler := setupTestServer(t)

	r := &http.Request{
		Host:   "localhost:8450",
		Header: http.Header{},
	}

	opts := handler.wsAcceptOptions(r)
	require.NotNil(t, opts)

	patterns := opts.OriginPatterns
	assert.Contains(t, patterns, "localhost")
	assert.Contains(t, patterns, "localhost:*")
	assert.Contains(t, patterns, "127.0.0.1")
	assert.Contains(t, patterns, "127.0.0.1:*")
	assert.Contains(t, patterns, "[::1]")
	assert.Contains(t, patterns, "[::1]:*")
}

// TestWSAcceptOptions_RemoteHostIncluded verifies that when accessed via
// a remote IP/hostname, that host is added to the allowed origin patterns.
func TestWSAcceptOptions_RemoteHostIncluded(t *testing.T) {
	_, handler := setupTestServer(t)

	r := &http.Request{
		Host:   "192.168.1.5:8450",
		Header: http.Header{},
	}

	opts := handler.wsAcceptOptions(r)
	require.NotNil(t, opts)

	// The request host should be added to patterns for same-origin remote access
	assert.Contains(t, opts.OriginPatterns, "192.168.1.5:8450",
		"remote request host should be in allowed origin patterns")
}

// TestWSAcceptOptions_EvilOriginNotInPatterns verifies that arbitrary external
// origins are NOT included in the allowed patterns.
func TestWSAcceptOptions_EvilOriginNotInPatterns(t *testing.T) {
	_, handler := setupTestServer(t)

	r := &http.Request{
		Host:   "192.168.1.5:8450",
		Header: http.Header{"Origin": {"http://evil.com"}},
	}

	opts := handler.wsAcceptOptions(r)
	require.NotNil(t, opts)

	// evil.com should NOT be in the patterns
	for _, p := range opts.OriginPatterns {
		assert.NotContains(t, p, "evil.com",
			"evil.com should never appear in allowed origin patterns")
	}
}

// TestWSAcceptOptions_DifferentIPNotInPatterns verifies that a different
// IP address (not matching the request Host) is not in allowed patterns.
func TestWSAcceptOptions_DifferentIPNotInPatterns(t *testing.T) {
	_, handler := setupTestServer(t)

	r := &http.Request{
		Host:   "192.168.1.5:8450",
		Header: http.Header{},
	}

	opts := handler.wsAcceptOptions(r)
	require.NotNil(t, opts)

	// A different IP should NOT be in patterns
	for _, p := range opts.OriginPatterns {
		assert.NotContains(t, p, "192.168.1.99",
			"different IP should not be in allowed origin patterns")
	}
}

// TestWSCoral_RejectsCrossOrigin verifies that a WebSocket connection from
// a cross-origin (evil.com) is rejected by the server.
func TestWSCoral_RejectsCrossOrigin(t *testing.T) {
	server, _ := setupTestServer(t)

	wsURL := "ws" + server.URL[4:] + "/ws/coral"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Try to connect with a cross-origin header
	opts := &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Origin": {"http://evil.com"},
		},
	}

	conn, _, err := websocket.Dial(ctx, wsURL, opts)
	if err != nil {
		// Expected: connection rejected due to origin check
		t.Log("Cross-origin WebSocket correctly rejected:", err)
		return
	}
	defer conn.CloseNow()
	t.Error("expected cross-origin WebSocket connection to be rejected")
}

// TestWSCoral_AcceptsLocalhostOrigin verifies that a WebSocket connection
// from localhost origin is accepted.
func TestWSCoral_AcceptsLocalhostOrigin(t *testing.T) {
	server, _ := setupTestServer(t)

	wsURL := "ws" + server.URL[4:] + "/ws/coral"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// httptest.NewServer binds to 127.0.0.1, so same-origin should work
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err, "localhost origin WebSocket should be accepted")
	defer conn.CloseNow()

	// Should receive initial coral_update
	var msg map[string]json.RawMessage
	err = wsjson.Read(ctx, conn, &msg)
	require.NoError(t, err)

	var msgType string
	json.Unmarshal(msg["type"], &msgType)
	assert.Equal(t, "coral_update", msgType)

	conn.Close(websocket.StatusNormalClosure, "done")
}
