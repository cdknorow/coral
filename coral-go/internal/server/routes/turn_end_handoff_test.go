package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/cdknorow/coral/internal/store"
)

// #3481 phase 1B (#3475 3A): when a Claude team agent's turn ends with unread board
// messages, its Stop hook is told to keep it going with them, and the nudge Coral held
// for the same messages is retired, never typed. Only the agent's own hook may ask, once
// per stop chain; an agent with no board, another CLI, or the setting off get nothing.
func TestTurnEndHandoff(t *testing.T) {
	server, h, _, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	sid := "eeeeeeee-3481-1b00-0000-000000000005"
	name := "claude-" + sid
	board := "team-3481"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "echo", WorkingDir: "/w", BoardName: &board}))
	tok := agentToken(t, h, sid, name)
	_, err := h.bs.Subscribe(ctx, board, "Worker", "Worker", name, nil, nil, "all")
	require.NoError(t, err)
	_, err = h.bs.Subscribe(ctx, board, "Lead", "Lead", "", nil, nil, "all")
	require.NoError(t, err)

	ask := func(body, tok string) (int, map[string]any) {
		req, _ := http.NewRequest("POST", server.URL+"/api/sessions/live/echo/turn-end", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if tok != "" {
			req.Header.Set(store.SessionTokenHeader, tok)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	code, _ := ask(`{}`, "")
	assert.Equal(t, http.StatusUnauthorized, code)
	_, out := ask(`{}`, tok)
	assert.Equal(t, false, out["handoff"], "nothing unread: the turn ends")

	_, err = h.bs.PostMessage(ctx, board, "Lead", "@Worker please review #12", nil)
	require.NoError(t, err)
	// Coral holds the nudge for it (the agent's screen shows a prompt), and an unrelated send.
	for _, key := range []string{"board-unread:" + board, "ui-request:9"} {
		_, err := ptymanager.DeliverAutomated(ctx, ptymanager.AutomatedInput{Term: menuOnScreen{}, Name: name, AgentType: "claude", SessionID: sid,
			Key: key, Text: "held " + key, What: "test", Send: func(context.Context) error { return nil }})
		require.NoError(t, err)
	}
	require.Equal(t, 2, ptymanager.HeldCount(name))

	_, out = ask(`{"stop_hook_active":true}`, tok)
	assert.Equal(t, false, out["handoff"], "at most once per stop chain")

	_, out = ask(`{}`, tok)
	require.Equal(t, true, out["handoff"])
	assert.Equal(t, "New board messages", out["reason"])
	assert.Contains(t, out["context"], "1 new message on "+board)
	assert.Contains(t, out["context"], "coral-board read")
	assert.Equal(t, 1, ptymanager.HeldCount(name), "the board nudge is retired; the other send stays held")
	ptymanager.DropHeld(name, "test done")

	// The setting off, or an agent with no board (a person drives it): no hand-off.
	require.NoError(t, ss.SetSetting(ctx, TurnEndHandoffSetting, "off"))
	_, out = ask(`{}`, tok)
	assert.Equal(t, false, out["handoff"], "turned off")
	require.NoError(t, ss.SetSetting(ctx, TurnEndHandoffSetting, "on"))
	sid2 := "eeeeeeee-3481-1b00-0000-000000000006"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid2, AgentType: "claude", AgentName: "solo", WorkingDir: "/w"}))
	_, out = ask(`{}`, agentToken(t, h, sid2, "claude-"+sid2))
	assert.Equal(t, false, out["handoff"], "no board")
}
