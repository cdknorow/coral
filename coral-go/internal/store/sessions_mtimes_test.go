package store

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// indexedMtimesBefore4669 is the query GetIndexedMtimes ran before #4669, kept so the new one is compared with it.
const indexedMtimesBefore4669 = `SELECT si.source_file, si.file_mtime FROM session_index si
		 WHERE EXISTS (SELECT 1 FROM session_fts f WHERE f.session_id = si.session_id)`

// #4669: GetIndexedMtimes reads the set of session ids in session_fts once instead of once per indexed session. On random indexes
// (sessions with and without a full-text row, files shared by several sessions with different mtimes) it answers what the old
// query answered, and its plan reads session_fts once (a list subquery) instead of probing it for each session, which on an FTS5
// table is a scan of every transcript's text.
func TestGetIndexedMtimesMatchesTheOldQueryAndDoesNotScanPerSession(t *testing.T) {
	db := openTestDB(t)
	s := NewSessionStore(db)
	ctx := context.Background()
	rnd := rand.New(rand.NewSource(4669))
	withFTS, withoutFTS, sharedFile := 0, 0, 0
	for i := 0; i < 80; i++ {
		file := fmt.Sprintf("/history/%d.jsonl", i)
		if i%7 == 3 && i > 0 {
			file = fmt.Sprintf("/history/%d.jsonl", i-1) // two sessions in one file: the newest mtime wins
			sharedFile++
		}
		sid := fmt.Sprintf("sess-%d", i)
		require.NoError(t, s.UpsertSessionIndex(ctx, &SessionIndex{SessionID: sid, SourceType: "claude", SourceFile: file, FileMtime: float64(rnd.Intn(5000))}))
		if rnd.Intn(3) != 0 {
			require.NoError(t, s.UpsertFTS(ctx, sid, strings.Repeat("body text ", 1+rnd.Intn(30))))
			withFTS++
		} else {
			withoutFTS++
		}
	}
	require.GreaterOrEqual(t, withFTS, 30)
	require.GreaterOrEqual(t, withoutFTS, 10)
	require.GreaterOrEqual(t, sharedFile, 8)

	got, err := s.GetIndexedMtimes(ctx)
	require.NoError(t, err)
	var rows []struct {
		SourceFile string  `db:"source_file"`
		FileMtime  float64 `db:"file_mtime"`
	}
	require.NoError(t, s.reader().SelectContext(ctx, &rows, indexedMtimesBefore4669))
	want := map[string]float64{}
	for _, r := range rows {
		if prev, ok := want[r.SourceFile]; !ok || r.FileMtime > prev {
			want[r.SourceFile] = r.FileMtime
		}
	}
	require.NotEmpty(t, want)
	assert.Equal(t, want, got)

	plan, err := s.reader().QueryxContext(ctx, "EXPLAIN QUERY PLAN "+indexedMtimesSQL)
	require.NoError(t, err)
	defer plan.Close()
	var lines []string
	for plan.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, plan.Scan(&id, &parent, &unused, &detail))
		lines = append(lines, detail)
	}
	all := strings.Join(lines, "\n")
	assert.Contains(t, all, "LIST SUBQUERY", "the ids in session_fts are read once, into a list")
	assert.NotContains(t, all, "EXISTS", "no per-session lookup of session_fts, which is a scan of every transcript's text")
}

func TestHistoryMtimesRechecksLegacyCodexOnceAndRemembersExclusions(t *testing.T) {
	db := openTestDB(t)
	s := NewSessionStore(db)
	ctx := context.Background()
	normal := &SessionIndex{SessionID: "parent", SourceType: "codex", SourceFile: "/history/parent.jsonl", FileMtime: 42}
	require.NoError(t, s.UpsertSessionIndex(ctx, normal))
	require.NoError(t, s.UpsertFTS(ctx, normal.SessionID, "ordinary chat"))
	legacy, err := s.GetIndexedMtimes(ctx)
	require.NoError(t, err)
	require.Equal(t, float64(42), legacy[normal.SourceFile])
	known, err := s.GetHistoryMtimes(ctx)
	require.NoError(t, err)
	assert.NotContains(t, known, normal.SourceFile, "an old unchanged Codex row is checked once")
	normal.IndexVersion = 1
	require.NoError(t, s.UpsertSessionIndex(ctx, normal))
	require.NoError(t, s.RemoveExcludedSessionSource(ctx, "codex", "/history/guardian.jsonl", 43, 1))
	// New store wrapper models a restart without keeping any in-memory sweep state.
	s = NewSessionStore(db)
	known, err = s.GetHistoryMtimes(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]float64{normal.SourceFile: 42, "/history/guardian.jsonl": 43}, known)
	_, err = db.ExecContext(ctx, `DELETE FROM session_fts WHERE session_id = 'parent'`)
	require.NoError(t, err)
	known, err = s.GetHistoryMtimes(ctx)
	require.NoError(t, err)
	assert.NotContains(t, known, normal.SourceFile, "missing FTS still backfills")
	// A changed rollout that becomes ordinary clears the exclusion cache.
	normal.SourceFile = "/history/guardian.jsonl"
	normal.SessionID = "changed"
	normal.FileMtime = 44
	require.NoError(t, s.UpsertSessionIndex(ctx, normal))
	require.NoError(t, s.UpsertFTS(ctx, normal.SessionID, "now ordinary"))
	known, err = s.GetHistoryMtimes(ctx)
	require.NoError(t, err)
	assert.Equal(t, float64(44), known[normal.SourceFile])
	var exclusions int
	require.NoError(t, db.GetContext(ctx, &exclusions, `SELECT count(*) FROM history_exclusions`))
	assert.Zero(t, exclusions)
}

// #6098: the exclusion mark of a rollout file that no longer exists is forgotten; one whose file is there, or cannot be checked, stays.
func TestPruneHistoryExclusionsForgetsOnlyFilesKnownToBeGone(t *testing.T) {
	db := openTestDB(t)
	s := NewSessionStore(db)
	ctx := context.Background()
	for _, f := range []string{"/h/kept.jsonl", "/h/gone-1.jsonl", "/h/gone-2.jsonl", "/h/unreadable.jsonl"} {
		require.NoError(t, s.RemoveExcludedSessionSource(ctx, "codex", f, 100, 1))
	}
	exists := func(path string) bool { return path == "/h/kept.jsonl" || path == "/h/unreadable.jsonl" } // the last: a file that cannot be checked counts as there
	n, err := s.PruneHistoryExclusions(ctx, exists)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	var left []string
	require.NoError(t, db.SelectContext(ctx, &left, `SELECT source_file FROM history_exclusions ORDER BY source_file`))
	assert.Equal(t, []string{"/h/kept.jsonl", "/h/unreadable.jsonl"}, left)
	known, err := s.GetHistoryMtimes(ctx)
	require.NoError(t, err)
	assert.Contains(t, known, "/h/kept.jsonl", "a kept exclusion still keeps the file out of the next pass")
	assert.NotContains(t, known, "/h/gone-1.jsonl")
	n, err = s.PruneHistoryExclusions(ctx, exists)
	require.NoError(t, err)
	assert.Zero(t, n, "nothing left to prune")
}
