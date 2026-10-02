package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/cdknorow/coral/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRestartPreservesSubscriptionWithoutStoredBoard(t *testing.T) {
	server, handler, term, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	const oldID = "restart-board-worker"
	const oldName = "codex-" + oldID
	term.addSession(oldName, t.TempDir())
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID: oldID, AgentType: "codex", AgentName: "coral-go", DisplayName: strPtr("UI"),
	}))
	_, err := handler.bs.Subscribe(ctx, "coral-task-workflows", "UI", "UI", oldName, nil, nil, "all", true)
	require.NoError(t, err)
	// A same-role subscription on another board must not be migrated.
	_, err = handler.bs.Subscribe(ctx, "other-board", "UI", "UI", "codex-other", nil, nil, "mentions")
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
	t.Cleanup(func() { os.Remove("board_state_" + result.SessionName + ".json") })
	ls, err := ss.GetLiveSession(ctx, result.SessionID)
	require.NoError(t, err)
	require.NotNil(t, ls)
	assert.Equal(t, "coral-task-workflows", derefStrPtr(ls.BoardName))
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
