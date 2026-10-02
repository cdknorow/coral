package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/cdknorow/coral/internal/store"
	"github.com/stretchr/testify/require"
)

func TestLiveRefreshSleepingUsesStoredPromptUntilWake(t *testing.T) {
	server, _, terminal, sessions := setupSessionsTestServer(t)
	root := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", root)
	require.NoError(t, os.Mkdir(filepath.Join(root, "project"), 0700))
	id := "00000000-0000-0000-0000-000000004321"
	require.NoError(t, os.WriteFile(filepath.Join(root, "project", id+".jsonl"), []byte(`{"type":"user","message":{"content":"transcript prompt"}}`+"\n"), 0600))
	prompt := "stored prompt"
	require.NoError(t, sessions.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: id, AgentType: "claude", AgentName: "test", WorkingDir: "/tmp/test", IsSleeping: 1, Prompt: &prompt}))
	read := func() []map[string]any {
		response, err := http.Get(server.URL + "/api/sessions/live")
		require.NoError(t, err)
		defer response.Body.Close()
		require.Equal(t, 200, response.StatusCode)
		var rows []map[string]any
		require.NoError(t, json.NewDecoder(response.Body).Decode(&rows))
		return rows
	}
	rows := read()
	require.Len(t, rows, 1)
	require.Equal(t, "stored prompt", rows[0]["first_prompt"], "sleeping rows must not resolve/parse transcript")
	terminal.addSession("claude-"+id, "/tmp/test")
	rows = read()
	require.Len(t, rows, 1)
	require.Equal(t, "stored prompt", rows[0]["first_prompt"], "a lingering sleeping terminal must not trigger enrichment")
	require.NoError(t, sessions.SetSessionSleeping(context.Background(), id, false))
	terminal.addSession("claude-"+id, "/tmp/test")
	rows = read()
	require.Len(t, rows, 1)
	require.Equal(t, "transcript prompt", rows[0]["first_prompt"], "wake must resume fresh transcript reads")
}
