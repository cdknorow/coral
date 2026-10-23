package background

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/store"
)

// A summary stored cut by the older parser (a secret straddling byte 200) is replaced once on the next pass, though its file has not changed.
func TestAnOlderParsersCutSummaryIsReplacedOnce(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", root)
	const sid = "00000000-0000-4000-8000-000000006207"
	secret := "ghp_" + strings.Repeat("aB3dE5gH7j", 3) + "kLmNoP"
	reply := strings.Repeat("x", 200-len(secret)+30) + " " + secret + " and the rest"
	path := filepath.Join(root, "proj", sid+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`{"type":"user","timestamp":"2026-10-23T00:00:00Z","message":{"role":"user","content":"hi"}}`+"\n"+
		`{"type":"assistant","timestamp":"2026-10-23T00:00:01Z","message":{"content":[{"type":"text","text":"`+reply+`"}]}}`+"\n"), 0o644))
	info, err := os.Stat(path)
	require.NoError(t, err)

	ctx := context.Background()
	db, err := store.OpenWithKey(ctx, filepath.Join(t.TempDir(), "sessions.db"), "")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ss := store.NewSessionStore(db)
	cutOld := reply[:200] // what the old parser stored: a plain 200-byte cut
	require.NoError(t, ss.UpsertSessionIndex(ctx, &store.SessionIndex{SessionID: sid, SourceType: "claude", SourceFile: path, FileMtime: float64(info.ModTime().Unix()), MessageCount: 2, DisplaySummary: cutOld}))
	require.NoError(t, ss.UpsertFTS(ctx, sid, "hi "+reply))

	idx := NewSessionIndexer(ss, []agent.HistoryScanner{&agent.ClaudeAgent{}}, time.Hour, 0)
	require.NoError(t, idx.RunOnce(ctx))
	row, err := ss.GetIndexedSession(ctx, sid)
	require.NoError(t, err)
	assert.NotEqual(t, cutOld, row.DisplaySummary, "the old row was read again and its summary cut safely")
	assert.NotContains(t, row.DisplaySummary, secret[:12])
	assert.Equal(t, agent.HistoryIndexVersion, row.IndexVersion)
	known, err := ss.GetHistoryMtimes(ctx, agent.HistoryIndexVersion)
	require.NoError(t, err)
	assert.Contains(t, known, path, "once: the next pass skips the unchanged file")
}
