package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// #6592: the live row (HTTP list, the websocket feed), /resolve and /status all carry the same bounded line for what an agent waits for, never the agent's raw text.
func TestWaitingSummaryIsTheRedactedBoundedLineEverywhere(t *testing.T) {
	server, handler, terminal, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	sid := "00000000-0000-4000-8000-0000000006c1"
	terminal.addSession("claude-"+sid, t.TempDir())
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "inbox", WorkingDir: t.TempDir()}))
	tok := tokenOf("ghp_", 36)
	dirty := permissionNote + ": \x1b[31mcurl -H 'Authorization: Bearer " + tok + "'\x1b[0m " + strings.Repeat("padding ", 40) + tok
	id := sid
	_, err := handler.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "inbox", SessionID: &id, EventType: "notification", Summary: dirty})
	require.NoError(t, err)

	check := func(name string, v any) {
		s, ok := v.(string)
		require.Truef(t, ok, "%s: a waiting agent has a line, got %v", name, v)
		assert.NotEmpty(t, s, name)
		noFragment(t, s, tok, 8, name+": no part of the token")
		assert.NotContains(t, s, "\x1b", name)
		assert.LessOrEqual(t, utf8.RuneCountInString(s), maxWaitingLine+1, name+": one line of at most 200 characters")
		assert.NotContains(t, s, "\n", name)
	}
	httpState, wsState := httpAndWSState(t, server, handler, sid)
	assert.Equal(t, true, httpState["waiting_for_input"], "the dirty summary is still a pending request")
	check("list", httpState["waiting_summary"])
	check("ws", wsState["waiting_summary"])
	for _, path := range []string{"/resolve", "/status"} {
		resp, err := http.Get(server.URL + "/api/sessions/" + sid + path)
		require.NoError(t, err)
		var got map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		resp.Body.Close()
		check(path, got["waiting_summary"])
	}
}
