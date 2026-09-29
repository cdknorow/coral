package routes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionsChat_AgyExplicitIsolationAndDiscovery(t *testing.T) {
	server, _, _, _ := setupSessionsTestServer(t)
	root := t.TempDir()
	brain := filepath.Join(root, "brain")
	t.Setenv("ANTIGRAVITY_DATA_DIR", brain)
	t.Setenv("GEMINI_TMP_DIR", t.TempDir())
	write := func(conv, text string) {
		t.Helper()
		p := filepath.Join(brain, conv, ".system_generated", "logs", "transcript.jsonl")
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0755))
		b, err := json.Marshal(map[string]any{"type": "USER_INPUT", "content": text})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(p, append(b, '\n'), 0644))
	}
	write("old", "old native history")
	require.NoError(t, os.WriteFile(filepath.Join(root, "history.jsonl"), []byte("{\"workspace\":\"/shared\",\"conversationId\":\"old\"}\n"), 0644))
	read := func(id string) (string, int) {
		t.Helper()
		q := url.Values{"agent_type": {"agy"}, "working_directory": {"/shared"}}
		if id != "" {
			q.Set("session_id", id)
		}
		resp, err := http.Get(server.URL + "/api/sessions/live/history-browser/chat?" + q.Encode())
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, 200, resp.StatusCode)
		var body struct {
			Messages []map[string]any `json:"messages"`
			Total    int              `json:"total"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		return fmt.Sprint(body.Messages), body.Total
	}
	text, total := read("")
	require.Equal(t, 1, total)
	require.Contains(t, text, "old native history")
	_, total = read("00000000-0000-0000-0000-000000000801")
	require.Zero(t, total)
	write("one", "CORAL_SESSION_ID: 00000000-0000-0000-0000-000000000801\nfirst exact")
	text, total = read("00000000-0000-0000-0000-000000000801")
	require.Equal(t, 1, total)
	require.Contains(t, text, "first exact")
	require.NotContains(t, text, "old native")
	_, total = read("00000000-0000-0000-0000-000000000802")
	require.Zero(t, total)
	write("two", "CORAL_SESSION_ID: 00000000-0000-0000-0000-000000000802\nsecond exact")
	text, total = read("00000000-0000-0000-0000-000000000802")
	require.Equal(t, 1, total)
	require.Contains(t, text, "second exact")
	require.NotContains(t, text, "first exact")
	text, total = read("00000000-0000-0000-0000-000000000801")
	require.Equal(t, 1, total)
	require.Contains(t, text, "first exact")
	require.NotContains(t, text, "second exact")
}
