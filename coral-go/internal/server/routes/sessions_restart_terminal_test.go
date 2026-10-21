package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store"
)

// Restarting a plain terminal starts no agent: a fresh shell in the same folder, nothing typed into it. It
// used to type a launch line for the Claude implementation that stands in for the type, which ran the
// claude on PATH whatever cli_path_claude said (the CDP gate's PATH-CLI check, a restarted terminal pane).
func TestSessionsRestartTerminalTypesNoLaunchCommand(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sleeping bool
	}{{"a fresh shell (the terminal is asleep)", true}, {"the live pane respawned", false}} {
		t.Run(tc.name, func(t *testing.T) {
			server, _, term, ss := setupSessionsTestServer(t)
			ctx := context.Background()
			wd := t.TempDir()
			require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{AgentName: "shell", AgentType: "terminal", WorkingDir: wd, SessionID: "term-1"}))
			if tc.sleeping {
				require.NoError(t, ss.SetSessionSleeping(ctx, "term-1", true))
			} else {
				term.mu.Lock()
				term.sessions["shell"] = &ptymanager.PaneInfo{SessionName: "shell", Target: "shell:0.0", CurrentPath: wd}
				term.mu.Unlock()
			}
			resp, err := http.Post(server.URL+"/api/sessions/live/shell/restart", "application/json",
				bytes.NewBufferString(`{"session_id": "term-1", "agent_type": "terminal"}`))
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode)
			var body struct {
				SessionID   string `json:"session_id"`
				SessionName string `json:"session_name"`
			}
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
			assert.Contains(t, body.SessionName, "terminal-")

			term.mu.Lock()
			for target, cmds := range term.sent {
				assert.Empty(t, cmds, "nothing is typed into %s", target)
			}
			term.mu.Unlock()
			ls, err := ss.GetLiveSession(ctx, body.SessionID)
			require.NoError(t, err)
			require.NotNil(t, ls)
			assert.Equal(t, "terminal", ls.AgentType)
		})
	}
}

// Resume is for an agent's conversation: for a plain terminal it is refused, and nothing is typed into any
// terminal (the Claude implementation that stands in for the type would otherwise build a resume line).
func TestSessionsResumeRefusesATerminal(t *testing.T) {
	server, _, term, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	const sid = "11111111-2222-3333-4444-555555555556"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{AgentName: "shell", AgentType: "terminal", WorkingDir: t.TempDir(), SessionID: sid}))
	resp, err := http.Post(server.URL+"/api/sessions/live/shell/resume", "application/json",
		bytes.NewBufferString(`{"session_id": "`+sid+`", "agent_type": "terminal", "current_session_id": "`+sid+`"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	term.mu.Lock()
	defer term.mu.Unlock()
	for target, cmds := range term.sent {
		assert.Empty(t, cmds, "nothing is typed into %s", target)
	}
	ls, err := ss.GetLiveSession(ctx, sid)
	require.NoError(t, err)
	require.NotNil(t, ls)
	assert.Equal(t, "terminal", ls.AgentType, "the session is untouched")
}
