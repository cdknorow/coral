package board

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4142: the board database gets the same optimize pass and WAL limit as sessions.db.
func TestBoardOptimizeAndWALLimit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		_, err := s.PostMessage(ctx, "proj", "sess-1", "hello", nil)
		require.NoError(t, err)
	}
	require.NoError(t, s.Optimize(ctx, true))
	var stats int
	require.NoError(t, s.db.GetContext(ctx, &stats, `SELECT COUNT(*) FROM sqlite_stat1`))
	assert.Positive(t, stats)
	require.NoError(t, s.Optimize(ctx, false))

	var limit int64
	require.NoError(t, s.db.GetContext(ctx, &limit, `PRAGMA journal_size_limit`))
	assert.EqualValues(t, 16<<20, limit)
}
