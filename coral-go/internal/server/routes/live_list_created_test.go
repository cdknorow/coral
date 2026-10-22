package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// #6058: a live row says when its agent was started (created_at, a fixed time, so a row does not change between lists), so the empty chat's "Waiting for the CLI to start" counts from the agent's own start and an agent that has
// been idle for hours never says it.
func TestLiveListRowsCarryWhenTheAgentStarted(t *testing.T) {
	server, _, terminal, ss := setupSessionsTestServer(t)
	defer server.Close()
	started := map[string]time.Time{
		"00000000-0000-0000-0000-0000000060a1": time.Now().Add(-3 * time.Second),
		"00000000-0000-0000-0000-0000000060a2": time.Now().Add(-2 * time.Hour),
	}
	for sid, at := range started {
		terminal.addSession("claude-"+sid, "/tmp/w")
		require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "w" + sid[len(sid)-2:], WorkingDir: "/tmp/w", CreatedAt: at.UTC().Format(time.RFC3339)}))
	}
	resp, err := http.Get(server.URL + "/api/sessions/live")
	require.NoError(t, err)
	defer resp.Body.Close()
	var rows []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rows))
	created := map[string]time.Time{}
	for _, r := range rows {
		sid, _ := r["session_id"].(string)
		s, ok := r["created_at"].(string)
		require.True(t, ok, "every awake row carries created_at: %v", r["session_id"])
		at, err := time.Parse(time.RFC3339, s)
		require.NoError(t, err)
		created[sid] = at
	}
	for sid, want := range started {
		assert.WithinDuration(t, want, created[sid], 2*time.Second, sid)
	}
}
