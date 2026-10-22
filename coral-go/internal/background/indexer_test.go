package background

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/store"
)

type fakeScanner struct{ sessions []agent.IndexedSession }

func (f *fakeScanner) HistoryBasePath() string    { return "/fake" }
func (f *fakeScanner) HistoryGlobPattern() string { return "*.jsonl" }
func (f *fakeScanner) ExtractSessions(string, map[string]float64) ([]agent.IndexedSession, error) {
	return f.sessions, nil
}

// An indexer pass writes every changed session's index row and search text, in batches of indexerFTSBatch: 250 sessions (more than two
// batches) all land, a session whose text is unchanged or empty keeps what it had, and a later pass replaces a session's old text.
func TestIndexerPassWritesEverySessionInBatches(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenWithKey(ctx, filepath.Join(t.TempDir(), "sessions.db"), "")
	require.NoError(t, err)
	defer db.Close()
	ss := store.NewSessionStore(db)
	fs := &fakeScanner{}
	const n = 250
	for i := 0; i < n; i++ {
		fs.sessions = append(fs.sessions, agent.IndexedSession{SessionID: fmt.Sprintf("s%03d", i), SourceType: "claude", SourceFile: fmt.Sprintf("/f/s%03d.jsonl", i),
			MessageCount: 3, FileMtime: float64(i + 1), FTSBody: fmt.Sprintf("first body %d needle", i)})
	}
	fs.sessions[10].FTSBody = "" // no text: no search row
	idx := NewSessionIndexer(ss, []agent.HistoryScanner{fs}, time.Hour, 0)
	require.Greater(t, n, 2*indexerFTSBatch)
	require.NoError(t, idx.RunOnce(ctx))
	count := func(q string, args ...any) int {
		var c int
		require.NoError(t, db.GetContext(ctx, &c, q, args...))
		return c
	}
	assert.Equal(t, n, count(`SELECT COUNT(*) FROM session_index`))
	assert.Equal(t, n-1, count(`SELECT COUNT(DISTINCT session_id) FROM session_fts`), "every session with text has its search row")
	assert.Equal(t, 0, count(`SELECT COUNT(*) FROM session_fts WHERE session_id = 's010'`))
	assert.Equal(t, n-1, count(`SELECT COUNT(*) FROM session_fts WHERE session_fts MATCH 'needle'`))

	// a second pass: every session has new text (session 10 now has some), one is marked unchanged (its old text stays)
	for i := range fs.sessions {
		fs.sessions[i].FileMtime += 1000
		fs.sessions[i].FTSBody = fmt.Sprintf("second body %d haystack", i)
	}
	fs.sessions[20].FTSUnchanged = true
	require.NoError(t, idx.RunOnce(ctx))
	assert.Equal(t, n, count(`SELECT COUNT(DISTINCT session_id) FROM session_fts`))
	assert.Equal(t, n-1, count(`SELECT COUNT(*) FROM session_fts WHERE session_fts MATCH 'haystack'`), "all new text but the unchanged one's")
	assert.Equal(t, 1, count(`SELECT COUNT(*) FROM session_fts WHERE session_fts MATCH 'needle'`), "only the unchanged session keeps its first text")
	assert.Equal(t, 0, count(`SELECT COUNT(*) FROM (SELECT session_id FROM session_fts GROUP BY session_id HAVING COUNT(*) > 1)`), "never two search rows for a session")
	// every session has a search row, so the next pass reads none of their files again
	known, err := ss.GetIndexedMtimes(ctx)
	require.NoError(t, err)
	assert.Len(t, known, n, "every session has its search row now: none is read again")
}

// #6098: a pass forgets the exclusion rows of rollout files that no longer exist, and only those: one whose file is there keeps keeping the file out of later passes.
func TestIndexerPassPrunesExclusionsOfFilesThatAreGone(t *testing.T) {
	ctx := context.Background()
	db, err := store.OpenWithKey(ctx, filepath.Join(t.TempDir(), "sessions.db"), "")
	require.NoError(t, err)
	defer db.Close()
	ss := store.NewSessionStore(db)
	dir := t.TempDir()
	kept := filepath.Join(dir, "rollout-kept.jsonl")
	require.NoError(t, os.WriteFile(kept, []byte("x"), 0o600))
	gone := filepath.Join(dir, "rollout-gone.jsonl") // never created: Codex deleted it
	for _, f := range []string{kept, gone} {
		require.NoError(t, ss.RemoveExcludedSessionSource(ctx, "codex", f, 1, 1))
	}
	idx := NewSessionIndexer(ss, []agent.HistoryScanner{&fakeScanner{}}, time.Hour, 0)
	require.NoError(t, idx.RunOnce(ctx))
	var left []string
	require.NoError(t, db.SelectContext(ctx, &left, `SELECT source_file FROM history_exclusions`))
	assert.Equal(t, []string{kept}, left)
}
