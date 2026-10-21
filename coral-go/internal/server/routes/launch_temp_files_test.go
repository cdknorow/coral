package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// #3601 W12: when the agent's prompt and settings files can't be written to the temp folder,
// a dashboard launch fails with an error naming that folder, and no terminal is made for an
// agent that would have started without Coral's prompt and hooks.
func TestLaunchFailsWhenTheTempFolderRefusesItsFiles(t *testing.T) {
	server, _, terminal, _ := setupSessionsTestServer(t)
	workDir := t.TempDir()
	gone := filepath.Join(t.TempDir(), "no-such-folder")
	t.Setenv("TMPDIR", gone)
	t.Setenv("TMP", gone) // os.TempDir on Windows
	t.Setenv("TEMP", gone)

	body, _ := json.Marshal(map[string]any{"working_dir": workDir, "agent_type": "claude", "prompt": "do the thing"})
	resp, err := http.Post(server.URL+"/api/sessions/launch", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	msg, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(msg), gone, "the error names the temp folder")

	sessions, _ := terminal.ListSessions(context.Background())
	assert.Empty(t, sessions, "no terminal is made")
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	assert.Empty(t, terminal.sent, "nothing is sent to a terminal")
}

// Security's SHOULD on #3601 W12: Restart and Resume build the launch command before the old
// terminal is touched, so a temp-folder failure leaves the old agent running in its terminal,
// under its name, and its session live; a sleeping agent's restart makes no new terminal.
func TestRestartAndResumeKeepTheOldAgentWhenTheTempFolderFails(t *testing.T) {
	server, _, term, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	const oldID = "11111111-aaaa-4bbb-8ccc-000000000001"
	const oldName = "claude-" + oldID
	term.addSession(oldName, t.TempDir())
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: oldID, AgentType: "claude", AgentName: "proj", WorkingDir: t.TempDir()}))
	const sleepID = "11111111-aaaa-4bbb-8ccc-000000000002"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sleepID, AgentType: "claude", AgentName: "sleepy", WorkingDir: t.TempDir()}))
	require.NoError(t, ss.SetSessionSleeping(ctx, sleepID, true))
	missing := filepath.Join(t.TempDir(), "no-such-folder")
	t.Setenv("TMPDIR", missing)
	t.Setenv("TMP", missing) // os.TempDir on Windows
	t.Setenv("TEMP", missing)

	post := func(path, body string) int {
		t.Helper()
		resp, err := http.Post(server.URL+path, "application/json", bytes.NewBufferString(body))
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	assert.Equal(t, http.StatusInternalServerError, post("/api/sessions/live/"+oldName+"/restart",
		`{"session_id":"`+oldID+`","agent_type":"claude","prompt":"do the thing"}`))
	assert.Equal(t, http.StatusInternalServerError, post("/api/sessions/live/"+oldName+"/resume",
		`{"session_id":"22222222-aaaa-4bbb-8ccc-000000000003","agent_type":"claude","current_session_id":"`+oldID+`"}`))
	assert.Equal(t, http.StatusInternalServerError, post("/api/sessions/live/claude-"+sleepID+"/restart",
		`{"session_id":"`+sleepID+`","agent_type":"claude","prompt":"do the thing"}`))

	term.mu.Lock()
	names := make([]string, 0, len(term.sessions))
	for n := range term.sessions {
		names = append(names, n)
	}
	sent := len(term.sent)
	term.mu.Unlock()
	assert.Equal(t, []string{oldName}, names, "the old terminal keeps its name; no new terminal is made")
	assert.Zero(t, sent, "nothing is sent to a terminal")
	ls, err := ss.GetLiveSession(ctx, oldID)
	require.NoError(t, err)
	require.NotNil(t, ls)
	assert.Nil(t, ls.StoppedAt, "the old session is still live")
}
