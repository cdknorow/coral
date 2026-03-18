package routes

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"

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

	handler := NewSessionsHandler(db, cfg)

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
