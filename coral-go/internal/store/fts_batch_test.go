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

// #4669: UpsertFTSBatch gives what calling UpsertFTS for each session, in order, gives: the same rows, the last body of a session named twice,
// old text replaced, whatever the batch sizes (above and below its chunk), and searches find the same sessions.

func ftsRows(t *testing.T, db *DB) map[string]string {
	t.Helper()
	var rows []struct {
		ID   string `db:"session_id"`
		Body string `db:"body"`
	}
	require.NoError(t, db.SelectContext(context.Background(), &rows, `SELECT session_id, body FROM session_fts ORDER BY session_id, rowid`))
	out := map[string]string{}
	for _, r := range rows {
		_, dup := out[r.ID]
		require.False(t, dup, "two search rows for session %q", r.ID)
		out[r.ID] = r.Body
	}
	return out
}

func TestUpsertFTSBatchEqualsUpsertFTSOneByOne(t *testing.T) {
	ctx := context.Background()
	words := []string{"alpha", "beta", "gamma", "delta", "parser", "indexer", "transcript", "window", "cursor", "budget"}
	trials := 12
	if testing.Short() {
		trials = 4
	}
	floors := map[string]int{}
	for trial := 0; trial < trials; trial++ {
		rng := rand.New(rand.NewSource(int64(trial) + 1))
		seq, batch := NewSessionStore(openTestDB(t)), NewSessionStore(openTestDB(t))
		for round := 0; round < 6; round++ {
			n := []int{0, 1, 5, ftsBatchIDs - 1, ftsBatchIDs, ftsBatchIDs + 1, 2*ftsBatchIDs + 7}[rng.Intn(7)]
			items := make([]FTSItem, n)
			pool := 1 + rng.Intn(n+1) // fewer ids than items: sessions named twice, and ids that earlier rounds wrote
			for i := range items {
				var b strings.Builder
				for j := 0; j < 1+rng.Intn(12); j++ {
					b.WriteString(words[rng.Intn(len(words))])
					b.WriteByte(' ')
				}
				id := fmt.Sprintf("sess-%d", rng.Intn(pool))
				if rng.Intn(20) == 0 {
					id = "it's \"quoted\" %_ id"
				}
				items[i] = FTSItem{SessionID: id, Body: b.String()}
			}
			for _, it := range items {
				require.NoError(t, seq.UpsertFTS(ctx, it.SessionID, it.Body))
			}
			require.NoError(t, batch.UpsertFTSBatch(ctx, items))
			want, got := ftsRows(t, seq.db), ftsRows(t, batch.db)
			require.Equal(t, want, got, "trial %d round %d (%d items)", trial, round, n)
			for _, w := range words[:4] {
				var a, b int
				require.NoError(t, seq.db.GetContext(ctx, &a, `SELECT COUNT(*) FROM session_fts WHERE session_fts MATCH ?`, w))
				require.NoError(t, batch.db.GetContext(ctx, &b, `SELECT COUNT(*) FROM session_fts WHERE session_fts MATCH ?`, w))
				require.Equal(t, a, b, "a search for %q", w)
			}
			floors["rounds"]++
			if n > ftsBatchIDs {
				floors["more than a chunk"]++
			}
			if len(want) < n {
				floors["a session named twice or replaced"]++
			}
			if n == 0 {
				floors["empty"]++
			}
		}
	}
	assert.GreaterOrEqual(t, floors["rounds"], 24)
	assert.GreaterOrEqual(t, floors["more than a chunk"], 5, "%v", floors)
	assert.GreaterOrEqual(t, floors["a session named twice or replaced"], 10, "%v", floors)
}

// A session named twice in one batch keeps its last body, and its old text from an earlier call is gone.
func TestUpsertFTSBatchLastBodyWins(t *testing.T) {
	ctx := context.Background()
	s := NewSessionStore(openTestDB(t))
	require.NoError(t, s.UpsertFTS(ctx, "a", "old text"))
	require.NoError(t, s.UpsertFTSBatch(ctx, []FTSItem{{"a", "first new"}, {"b", "other"}, {"a", "second new"}}))
	assert.Equal(t, map[string]string{"a": "second new", "b": "other"}, ftsRows(t, s.db))
	require.NoError(t, s.UpsertFTSBatch(ctx, nil))
	assert.Len(t, ftsRows(t, s.db), 2, "an empty batch changes nothing")
}
