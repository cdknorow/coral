package background

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/store"
)

// #6207: a session indexed before the first user message was kept (an older parser version) is read again once, though its file has not changed, and a session never written to again gets its message.
func TestAnOlderParsersRowIsReadAgainOnceForTheFirstUserMessage(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", root)
	const sid = "00000000-0000-4000-8000-000000006207"
	path := filepath.Join(root, "proj", sid+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`{"type":"user","timestamp":"2026-10-23T00:00:00Z","message":{"role":"user","content":"which folder are you in?"}}`+"\n"+
		`{"type":"assistant","timestamp":"2026-10-23T00:00:01Z","message":{"content":[{"type":"text","text":"The project folder."}]}}`+"\n"), 0o644))
	info, err := os.Stat(path)
	require.NoError(t, err)

	ctx := context.Background()
	db, err := store.OpenWithKey(ctx, filepath.Join(t.TempDir(), "sessions.db"), "")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ss := store.NewSessionStore(db)
	// the row as the parser before this one wrote it: indexed, searchable, the same mtime, no first message, version 0
	require.NoError(t, ss.UpsertSessionIndex(ctx, &store.SessionIndex{SessionID: sid, SourceType: "claude", SourceFile: path, FileMtime: float64(info.ModTime().Unix()), MessageCount: 2, DisplaySummary: "The project folder."}))
	require.NoError(t, ss.UpsertFTS(ctx, sid, "which folder are you in? The project folder."))

	idx := NewSessionIndexer(ss, []agent.HistoryScanner{&agent.ClaudeAgent{}}, time.Hour, 0)
	require.NoError(t, idx.RunOnce(ctx))
	row, err := ss.GetIndexedSession(ctx, sid)
	require.NoError(t, err)
	assert.Equal(t, "which folder are you in?", row.FirstUserMessage, "the old row was read again and now has the first message")
	assert.Equal(t, agent.HistoryIndexVersion, row.IndexVersion)

	known, err := ss.GetHistoryMtimes(ctx, agent.HistoryIndexVersion)
	require.NoError(t, err)
	assert.Contains(t, known, path, "once: a pass over an unchanged file of the current version skips it")
}
