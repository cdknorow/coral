package routes

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// #4065: a session's "model" is the one its transcript last recorded once a turn read it
// ("model_source": "transcript"), else the launch's ("launch").
func TestSessionModelFollowsTheTranscript(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	sid := "00000000-0000-4000-8000-00000000c065"
	path := filepath.Join(dir, "x", sid+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h := NewSessionsHandler(db, &config.Config{LogDir: t.TempDir()}, nil, newMockTerminal(), nil)

	entry := map[string]any{"model": "opus"}
	h.addTranscriptModel(entry, sid, nil)
	assert.Equal(t, map[string]any{"model": "opus", "model_source": "launch"}, entry)

	require.NoError(t, os.WriteFile(path, []byte(`{"sessionId":"`+sid+`","type":"assistant","message":{"model":"claude-sonnet-5-5","content":[]},"timestamp":"2026-10-16T00:00:00Z"}`+"\n"), 0o600))
	h.jsonl.ReadClaudeTurnEvent(sid, "")
	h.addTranscriptModel(entry, sid, nil)
	assert.Equal(t, "claude-sonnet-5-5", entry["model"])
	assert.Equal(t, "transcript", entry["model_source"])

	none := map[string]any{}
	h.addTranscriptModel(none, "another", nil)
	assert.Empty(t, none, "no model known: no field")
}
