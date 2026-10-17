package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4142: optimize gathers the planner's statistics (sqlite_stat1) and changes no data.
func TestOptimizeGathersStatistics(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	_, err := d.Exec(`WITH RECURSIVE c(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM c WHERE i < 199)
		INSERT INTO call_metrics(call_type, operation, agent_name, session_id, board_name, duration_ms, is_error, created_at)
		SELECT 'api', '/api/x', 'a', 's1', 'b', i, 0, strftime('%Y-%m-%dT%H:%M:%fZ', 'now') FROM c`)
	require.NoError(t, err)
	count := func() (n int) { require.NoError(t, d.Get(&n, `SELECT COUNT(*) FROM call_metrics`)); return }
	before := count()
	require.NoError(t, d.Optimize(ctx, true))
	var stats int
	require.NoError(t, d.Get(&stats, `SELECT COUNT(*) FROM sqlite_stat1 WHERE tbl = 'call_metrics'`))
	assert.Positive(t, stats, "the first pass analyzes tables that have no statistics")
	require.NoError(t, d.Optimize(ctx, false), "a later pass")
	assert.Equal(t, before, count())
}

// #4142: the writer truncates the WAL back to JournalSizeLimit after a checkpoint, so one large
// transaction doesn't leave a large -wal file for the life of the process.
func TestWriterLimitsTheWALAfterACheckpoint(t *testing.T) {
	d := openTestDB(t)
	var limit int64
	require.NoError(t, d.Get(&limit, `PRAGMA journal_size_limit`))
	assert.EqualValues(t, JournalSizeLimit, limit)

	_, err := d.Exec(`CREATE TABLE wal_probe(b BLOB)`)
	require.NoError(t, err)
	// About 24 MiB in one transaction: the WAL grows past the limit.
	_, err = d.Exec(`WITH RECURSIVE c(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM c WHERE i < 24*16-1)
		INSERT INTO wal_probe SELECT randomblob(65536) FROM c`)
	require.NoError(t, err)
	wal := walPath(t, d)
	big, err := os.Stat(wal)
	require.NoError(t, err)
	require.Greater(t, big.Size(), int64(JournalSizeLimit))

	_, err = d.Exec(`PRAGMA wal_checkpoint(RESTART)`)
	require.NoError(t, err)
	_, err = d.Exec(`DELETE FROM wal_probe WHERE rowid = 1`) // the next write resets the WAL
	require.NoError(t, err)
	after, err := os.Stat(wal)
	require.NoError(t, err)
	assert.LessOrEqual(t, after.Size(), int64(JournalSizeLimit), "the WAL was truncated back")
}

func walPath(t *testing.T, d *DB) string {
	t.Helper()
	var seq int
	var name, file string
	require.NoError(t, d.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &file))
	require.True(t, strings.HasSuffix(file, ".db"), file)
	return filepath.Clean(file) + "-wal"
}
