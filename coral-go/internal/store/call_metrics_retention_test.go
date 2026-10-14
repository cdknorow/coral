package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// insertMetrics adds n call metrics created at t (fast: one statement).
func insertMetrics(t *testing.T, db *DB, n int, at time.Time) {
	t.Helper()
	_, err := db.Exec(`WITH RECURSIVE c(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM c WHERE i < ?)
		INSERT INTO call_metrics(call_type, operation, method, created_at) SELECT 'api', '/api/x', 'GET', ? FROM c`,
		n, at.UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)
}

func countMetrics(t *testing.T, db *DB) int {
	t.Helper()
	var n int
	require.NoError(t, db.Get(&n, `SELECT COUNT(*) FROM call_metrics`))
	return n
}

func TestCallMetricsRetentionDays(t *testing.T) {
	for v, want := range map[string]int{"": 14, "x": 14, "7": 7, " 30 ": 30, "0": 1, "-5": 1, "91": 90, "90": 90, "1": 1} {
		assert.Equal(t, want, CallMetricsRetentionDays(map[string]string{CallMetricsRetentionSetting: v}), "%q", v)
	}
	assert.Equal(t, 14, CallMetricsRetentionDays(nil))
}

// Rows before the cutoff go, oldest first, a batch at a time; rows at or after
// it stay.
func TestPruneCallMetrics(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	cutoff := now.Add(-14 * 24 * time.Hour)
	insertMetrics(t, db, 7, cutoff.Add(-48*time.Hour))
	insertMetrics(t, db, 3, cutoff.Add(-time.Second))
	insertMetrics(t, db, 4, cutoff) // exactly at the cutoff: kept
	insertMetrics(t, db, 5, now)

	n, err := db.PruneCallMetrics(ctx, cutoff, 6)
	require.NoError(t, err)
	assert.EqualValues(t, 6, n)
	n, err = db.PruneCallMetrics(ctx, cutoff, 6)
	require.NoError(t, err)
	assert.EqualValues(t, 4, n, "the rest of the expired rows")
	n, err = db.PruneCallMetrics(ctx, cutoff, 6)
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Equal(t, 9, countMetrics(t, db))
}

// A batch walks idx_call_metrics_created and never sorts the whole history, and
// the summary still reads its window from the same index.
func TestCallMetricsQueriesUseTheCreatedIndex(t *testing.T) {
	db := openTestDB(t)
	plan := func(q string, args ...any) string {
		rows, err := db.Queryx("EXPLAIN QUERY PLAN "+q, args...)
		require.NoError(t, err)
		defer rows.Close()
		var lines []string
		for rows.Next() {
			var id, parent, notused int
			var detail string
			require.NoError(t, rows.Scan(&id, &parent, &notused, &detail))
			lines = append(lines, detail)
		}
		return strings.Join(lines, "\n")
	}
	prune := plan(`DELETE FROM call_metrics WHERE id IN (
		SELECT id FROM call_metrics WHERE created_at < ? ORDER BY created_at LIMIT ?)`, "2026-01-01", 5000)
	assert.Contains(t, prune, "idx_call_metrics_created", prune)
	assert.NotContains(t, prune, "TEMP B-TREE", "no sort of every expired row:\n"+prune)
	summary := plan(`SELECT COUNT(*) FROM call_metrics cm WHERE cm.created_at >= ?`, "2026-01-01")
	assert.Contains(t, summary, "idx_call_metrics_created", summary)
}

// An existing database loses the two indexes nothing queries (#3261).
func TestOpenDropsTheUnusedCallMetricsIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := Open(path)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE INDEX idx_call_metrics_agent ON call_metrics(agent_name, created_at);
		CREATE INDEX idx_call_metrics_board ON call_metrics(board_name, created_at)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	db, err = Open(path)
	require.NoError(t, err)
	defer db.Close()
	var names []string
	require.NoError(t, db.Select(&names, `SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'call_metrics' AND name LIKE 'idx_%' ORDER BY name`))
	assert.Equal(t, []string{"idx_call_metrics_created"}, names)
}
