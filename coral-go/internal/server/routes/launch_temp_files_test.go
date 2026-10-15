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
)

// #3601 W12: when the agent's prompt and settings files can't be written to the temp folder,
// a dashboard launch fails with an error naming that folder, and no terminal is made for an
// agent that would have started without Coral's prompt and hooks.
func TestLaunchFailsWhenTheTempFolderRefusesItsFiles(t *testing.T) {
	server, _, terminal, _ := setupSessionsTestServer(t)
	workDir := t.TempDir()
	gone := filepath.Join(t.TempDir(), "no-such-folder")
	t.Setenv("TMPDIR", gone)

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
