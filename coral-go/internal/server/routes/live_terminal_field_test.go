package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store"
)

// Every row of GET /api/sessions/live says which terminal the agent really runs on ("tmux" survives a quit, "pty" ends with Coral): the hub shows it. The stored launch path cannot say, since on a tmux Coral
// it reads "pty" for every agent. Through the route, not the handler's helper.
func liveRowTerminals(t *testing.T, serverURL string) map[string]string {
	t.Helper()
	resp, err := http.Get(serverURL + "/api/sessions/live")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var rows []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rows))
	out := map[string]string{}
	for _, r := range rows {
		sid, _ := r["session_id"].(string)
		term, ok := r["terminal"].(string)
		require.True(t, ok, "row %s has a terminal field: %v", sid, r)
		out[sid] = term
	}
	return out
}

func TestEveryLiveRowSaysWhichTerminalTheAgentRunsOn(t *testing.T) {
	server, h, mock, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	const awake, asleepPty, asleepTmux = "bbbbbbbb-6146-4000-8000-000000000001", "bbbbbbbb-6146-4000-8000-000000000002", "bbbbbbbb-6146-4000-8000-000000000003"
	pty, tmuxPath := "pty", "tmux"
	mock.addSession(naming.SessionName("claude", awake), "/tmp/project")
	for sid, label := range map[string]*string{awake: &pty, asleepPty: &pty, asleepTmux: &tmuxPath} {
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentName: "a-" + sid[len(sid)-1:], AgentType: "claude", WorkingDir: "/tmp/project", Backend: label}))
	}
	require.NoError(t, ss.SetSessionSleepingFor(ctx, asleepPty, ""))
	require.NoError(t, ss.SetSessionSleepingFor(ctx, asleepTmux, ""))

	// A tmux Coral (every agent's launch path reads "pty"): all of them are on tmux, awake or asleep.
	h.backend = &ptymanager.TmuxBackend{}
	got := liveRowTerminals(t, server.URL)
	assert.Equal(t, map[string]string{awake: "tmux", asleepPty: "tmux", asleepTmux: "tmux"}, got)

	// A pty Coral: its agents are on pty, except one started the legacy tmux way.
	h.backend = &ptymanager.PTYBackend{}
	got = liveRowTerminals(t, server.URL)
	assert.Equal(t, "pty", got[asleepPty])
	assert.Equal(t, "tmux", got[asleepTmux], "a legacy tmux agent on a pty Coral")
	assert.Equal(t, "pty", got[awake])
}
