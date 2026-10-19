package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// #4969: a session's "thinking_actual" is the level its transcript's newest turn ran at (the chip reads it; the transcript, not what
// was asked for, is what the CLI really did), and "thinking" is the level it was started with. Neither is ever a guess: a turn that
// recorded no level has no thinking_actual.
func TestSessionThinkingActualFollowsTheTranscript(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	sid := "00000000-0000-4000-8000-00000000c969"
	path := filepath.Join(dir, "x", sid+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h := NewSessionsHandler(db, &config.Config{LogDir: t.TempDir()}, nil, newMockTerminal(), nil)

	entry := map[string]any{"model": "opus"}
	h.addTranscriptModel(entry, sid, nil)
	assert.NotContains(t, entry, "thinking_actual", "nothing read yet")

	write := func(effort string) {
		require.NoError(t, os.WriteFile(path, []byte(`{"sessionId":"`+sid+`","type":"assistant",`+effort+`"message":{"model":"claude-sonnet-5-5","content":[]},"timestamp":"2026-10-16T00:00:00Z"}`+"\n"), 0o600))
		h.jsonl.ReadClaudeTurnEvent(sid, "")
	}
	write(`"effort":"high",`)
	entry = map[string]any{}
	h.addTranscriptModel(entry, sid, nil)
	assert.Equal(t, "high", entry["thinking_actual"])
	assert.Equal(t, "claude-sonnet-5-5", entry["model"])

	os.Remove(path)
	h.jsonl = nil // a handler with no reader adds nothing and does not panic
	entry = map[string]any{}
	h.addTranscriptModel(entry, sid, nil)
	assert.NotContains(t, entry, "thinking_actual")
}

func TestLiveRowsAndInfoCarryTheStoredThinkingLevel(t *testing.T) {
	server, handler, term, ss := setupSessionsTestServer(t)
	ctx := context.Background()
	dir := t.TempDir()
	high := "high"
	for _, c := range []struct {
		id    string
		level *string
	}{{"11111111-2222-3333-4444-555555555555", &high}, {"22222222-2222-3333-4444-555555555555", nil}} {
		term.addSession("claude-"+c.id, dir)
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: c.id, AgentType: "claude", AgentName: "proj", WorkingDir: dir, Thinking: c.level}))
	}
	// A sleeping agent keeps its level in the list too.
	sleepID := "33333333-2222-3333-4444-555555555555"
	low := "low"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sleepID, AgentType: "claude", AgentName: "zz", WorkingDir: dir, Thinking: &low}))
	require.NoError(t, ss.SetSessionSleeping(ctx, sleepID, true))

	get := func(url string, into any) {
		resp, err := http.Get(server.URL + url)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.NoError(t, json.NewDecoder(resp.Body).Decode(into))
	}
	var rows []map[string]any
	get("/api/sessions/live", &rows)
	byID := map[string]map[string]any{}
	for _, r := range rows {
		byID[r["session_id"].(string)] = r
	}
	require.Len(t, byID, 3)
	assert.Equal(t, "high", byID["11111111-2222-3333-4444-555555555555"]["thinking"])
	assert.NotContains(t, byID["22222222-2222-3333-4444-555555555555"], "thinking", "no level: no field")
	assert.Equal(t, "low", byID[sleepID]["thinking"])

	ws, err := handler.buildSessionListForWS(httptest.NewRequest(http.MethodGet, "/ws/coral", nil))
	require.NoError(t, err)
	found := false
	for _, r := range ws {
		if r["session_id"] == "11111111-2222-3333-4444-555555555555" {
			found = true
			assert.Equal(t, "high", r["thinking"], "the feed carries it too")
		}
	}
	assert.True(t, found)

	var info map[string]any
	get("/api/sessions/live/claude-11111111-2222-3333-4444-555555555555/info?agent_type=claude&session_id=11111111-2222-3333-4444-555555555555", &info)
	assert.Equal(t, "high", info["thinking"])
}
