package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// #3213: Restart and Resume of an awake agent whose own terminal is gone start it in a fresh
// terminal in its working directory (before, both answered 409 terminal_gone until the agent
// showed asleep). A teammate's pane with the same name is never touched.
func TestRestartAndResumeRecoverAGoneTerminal(t *testing.T) {
	for _, route := range []string{"restart", "resume"} {
		t.Run(route, func(t *testing.T) {
			server, _, terminal, ss := setupSessionsTestServer(t)
			ctx := context.Background()
			const sid = "00000000-0000-0000-0000-000000003213"
			wd := t.TempDir()
			// A project setting in the agent's folder. Claude Code reads it itself, behind its
			// folder-trust prompt; Coral never copies it into --settings (#3591). That the launch
			// is built for this folder shows in the terminal's working directory below.
			require.NoError(t, os.MkdirAll(filepath.Join(wd, ".claude"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(wd, ".claude", "settings.json"), []byte(`{"env":{"QA_3213_MARKER":"own-folder"}}`), 0o644))
			require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "team", WorkingDir: wd}))
			terminal.addSession("team", "/tmp/other-team") // a teammate's pane by the same name
			terminal.gone = true                           // the agent's own terminal is gone

			body := `{"agent_type":"claude","session_id":"` + sid + `"}`
			if route == "resume" {
				body = `{"agent_type":"claude","session_id":"11111111-2222-4333-8444-555555555555","current_session_id":"` + sid + `"}`
			}
			resp, err := http.Post(server.URL+"/api/sessions/live/team/"+route, "application/json", bytes.NewBufferString(body))
			require.NoError(t, err)
			defer resp.Body.Close()
			var got struct {
				SessionID   string `json:"session_id"`
				SessionName string `json:"session_name"`
				Error       string `json:"error"`
			}
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
			require.Equal(t, http.StatusOK, resp.StatusCode, got.Error)
			assert.NotEqual(t, sid, got.SessionID)

			terminal.mu.Lock()
			pane := terminal.sessions[got.SessionName]
			launched := terminal.sent[got.SessionName+".0"]
			sibling := terminal.sessions["team"]
			terminal.mu.Unlock()
			require.NotNil(t, pane, "a fresh terminal is created for the agent")
			assert.Equal(t, wd, pane.CurrentPath, "in the agent's own working directory")
			require.Len(t, launched, 1, "the agent is launched in its new terminal")
			m := oracleSettingsArg.FindStringSubmatch(launched[0]) // a quoted path on Windows (PowerShell)
			require.Len(t, m, 2, "launch has a settings file: %s", launched[0])
			settings, err := os.ReadFile(strings.Trim(m[1], `'"`))
			require.NoError(t, err)
			assert.NotContains(t, string(settings), "QA_3213_MARKER", "a repository's settings never reach --settings (#3591)")
			if route == "resume" {
				assert.True(t, strings.Contains(launched[0], "11111111-2222-4333-8444-555555555555"), "resume continues the conversation: %s", launched[0])
			}
			require.NotNil(t, sibling)
			assert.Equal(t, "team", sibling.SessionName, "the teammate's pane is not renamed")
			assert.Equal(t, "/tmp/other-team", sibling.CurrentPath)
			terminal.mu.Lock()
			assert.Empty(t, terminal.killSessionCalls, "nothing is killed")
			assert.Empty(t, terminal.sent["team"], "nothing is typed into the teammate's pane")
			terminal.mu.Unlock()

			ls, err := ss.GetLiveSession(ctx, got.SessionID)
			require.NoError(t, err)
			require.NotNil(t, ls)
			assert.Equal(t, wd, ls.WorkingDir)
			assert.Equal(t, 0, ls.IsSleeping)
		})
	}
}

// Without a working directory to start in, a gone terminal still answers terminal_gone.
func TestResumeWithoutAWorkingDirStaysTerminalGone(t *testing.T) {
	server, _, terminal, ss := setupSessionsTestServer(t)
	const sid = "00000000-0000-0000-0000-00000000321a"
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "team"}))
	terminal.gone = true
	resp, err := http.Post(server.URL+"/api/sessions/live/team/resume", "application/json",
		bytes.NewBufferString(`{"agent_type":"claude","session_id":"11111111-2222-4333-8444-555555555555","current_session_id":"`+sid+`"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	var got map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Equal(t, "terminal_gone", got["code"])
}
