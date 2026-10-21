package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/internal/ptr"
	"github.com/subgentic/coral/internal/store"
)

func TestRestartPreservesSubscriptionWithoutStoredBoard(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	server, handler, term, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	const oldID = "restart-board-worker"
	const oldName = "codex-" + oldID
	term.addSession(oldName, t.TempDir())
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID: oldID, AgentType: "codex", AgentName: "coral-go", DisplayName: ptr.NonEmpty("UI"),
	}))
	_, err := handler.bs.Subscribe(ctx, "coral-task-workflows", "UI", "UI", oldName, nil, "all", true)
	require.NoError(t, err)
	// A same-role subscription on another board must not be migrated.
	_, err = handler.bs.Subscribe(ctx, "other-board", "UI", "UI", "codex-other", nil, "mentions")
	require.NoError(t, err)
	resp, err := http.Post(server.URL+"/api/sessions/live/"+oldName+"/restart", "application/json",
		bytes.NewBufferString(`{"session_id":"restart-board-worker","agent_type":"codex"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var result struct {
		SessionID   string `json:"session_id"`
		SessionName string `json:"session_name"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	ls, err := ss.GetLiveSession(ctx, result.SessionID)
	require.NoError(t, err)
	require.NotNil(t, ls)
	assert.Equal(t, "coral-task-workflows", ptr.Value(ls.BoardName))
	sub, err := handler.bs.GetSubscriptionBySessionName(ctx, result.SessionName)
	require.NoError(t, err)
	require.NotNil(t, sub)
	assert.Equal(t, "UI", sub.SubscriberID)
	assert.Equal(t, "all", sub.ReceiveMode)
	assert.Equal(t, 1, sub.CanPeek)
	old, err := handler.bs.GetSubscriptionBySessionName(ctx, oldName)
	require.NoError(t, err)
	assert.Nil(t, old)
	other, err := handler.bs.GetSubscriptionBySessionName(ctx, "codex-other")
	require.NoError(t, err)
	require.NotNil(t, other)
	assert.Equal(t, "other-board", other.Project)
}

// The membership part of the removed tests/stress/test_team_restart.py (#3811): a board-only
// membership (joined through the board API, not at launch) survives two restarts in a row: one
// subscription, the same row, role and receive mode, targeting the newest terminal, and the
// session keeps its board and display name.
func TestRestartTwiceKeepsBoardMembership(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	server, handler, term, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	id, name := "restart-twice-ui", "claude-restart-twice-ui"
	term.addSession(name, t.TempDir())
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID: id, AgentType: "claude", AgentName: "coral-go", DisplayName: ptr.NonEmpty("UI"),
	}))
	original, err := handler.bs.Subscribe(ctx, "restart-membership", "UI", "UI", name, nil, "all")
	require.NoError(t, err)
	for attempt := 1; attempt <= 2; attempt++ {
		resp, err := http.Post(server.URL+"/api/sessions/live/"+name+"/restart", "application/json",
			bytes.NewBufferString(`{"session_id":"`+id+`","agent_type":"claude"}`))
		require.NoError(t, err)
		var result struct {
			SessionID   string `json:"session_id"`
			SessionName string `json:"session_name"`
		}
		require.Equal(t, http.StatusOK, resp.StatusCode, "restart %d", attempt)
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
		resp.Body.Close()
		require.NotEqual(t, id, result.SessionID, "restart %d: a new session", attempt)
		id, name = result.SessionID, result.SessionName
		term.addSession(name, t.TempDir()) // the new terminal, for the next restart

		ls, err := ss.GetLiveSession(ctx, id)
		require.NoError(t, err)
		require.NotNil(t, ls)
		assert.Equal(t, "restart-membership", ptr.Value(ls.BoardName), "restart %d keeps the board", attempt)
		assert.Equal(t, "UI", ptr.Value(ls.DisplayName), "restart %d keeps the display name", attempt)
		subs, err := handler.bs.ListSubscribers(ctx, "restart-membership")
		require.NoError(t, err)
		require.Len(t, subs, 1, "restart %d: one subscription", attempt)
		assert.Equal(t, original.ID, subs[0].ID, "restart %d keeps the same membership row", attempt)
		assert.Equal(t, "UI", subs[0].SubscriberID)
		assert.Equal(t, "UI", subs[0].JobTitle)
		assert.Equal(t, "all", subs[0].ReceiveMode)
		assert.Equal(t, name, subs[0].SessionName, "restart %d: it targets the new terminal", attempt)
	}
}
