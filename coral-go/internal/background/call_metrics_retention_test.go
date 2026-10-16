package background

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

func addMetrics(t *testing.T, db *store.DB, n int, at time.Time) {
	t.Helper()
	_, err := db.Exec(`WITH RECURSIVE c(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM c WHERE i < ?)
		INSERT INTO call_metrics(call_type, operation, method, created_at) SELECT 'api', '/api/x', 'GET', ? FROM c`,
		n, at.UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)
}

func metricCount(t *testing.T, db *store.DB) int {
	t.Helper()
	var n int
	require.NoError(t, db.Get(&n, `SELECT COUNT(*) FROM call_metrics`))
	return n
}

func smallJob(db *store.DB) *CallMetricsRetention {
	j := NewCallMetricsRetention(db)
	j.BatchSize, j.Pause, j.RunCap = 100, time.Millisecond, 250
	return j
}

// A run deletes up to its cap and leaves the rest for the next run; recent rows
// are never touched; the retention setting decides the window.
func TestCallMetricsRetentionRunsInCappedBatches(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	now := time.Now()
	addMetrics(t, db, 400, now.Add(-30*24*time.Hour))
	addMetrics(t, db, 50, now.Add(-time.Hour))
	j := smallJob(db)

	assert.EqualValues(t, 250, j.RunOnce(ctx), "the per-run cap")
	assert.Equal(t, 200, metricCount(t, db))
	assert.EqualValues(t, 150, j.RunOnce(ctx), "the rest, next run")
	assert.EqualValues(t, 0, j.RunOnce(ctx))
	assert.Equal(t, 50, metricCount(t, db), "recent rows stay")

	// Rows older than a day are rolled into hourly rows (#4098), kept for the window; a
	// shorter window (1 day) removes them.
	addMetrics(t, db, 20, now.Add(-3*24*time.Hour))
	assert.EqualValues(t, 0, NewCallMetricsRetention(db).RunOnce(ctx), "nothing past the 14-day default")
	assert.Equal(t, 50, metricCount(t, db), "the 3-day-old rows were rolled up")
	assert.Equal(t, 20, hourlyCalls(t, db))
	require.NoError(t, store.NewSessionStore(db).SetSetting(ctx, store.CallMetricsRetentionSetting, "1"))
	j.RunOnce(ctx)
	assert.Equal(t, 0, hourlyCalls(t, db), "past a 1-day window the rollup goes too")
	assert.Equal(t, 50, metricCount(t, db), "recent rows stay")
}

func hourlyCalls(t *testing.T, db *store.DB) int {
	t.Helper()
	var n int
	require.NoError(t, db.Get(&n, `SELECT COALESCE(SUM(calls), 0) FROM call_metrics_hourly`))
	return n
}

// Cancelling stops a run between batches.
func TestCallMetricsRetentionStopsOnCancel(t *testing.T) {
	db := setupTestDB(t)
	addMetrics(t, db, 1000, time.Now().Add(-30*24*time.Hour))
	j := smallJob(db)
	j.RunCap, j.Pause = 1000, 50*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int64)
	go func() { done <- j.RunOnce(ctx) }()
	time.Sleep(120 * time.Millisecond) // sleep-ok: let a couple of batches run before the cancel
	cancel()
	select {
	case n := <-done:
		assert.Less(t, n, int64(1000), "stopped early")
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not stop on cancel")
	}
}

// The job never monopolizes the single connection: a request that needs the
// database during a long run completes within about one batch.
func TestCallMetricsRetentionYieldsTheConnection(t *testing.T) {
	db := setupTestDB(t)
	addMetrics(t, db, 20000, time.Now().Add(-30*24*time.Hour))
	j := NewCallMetricsRetention(db)
	j.BatchSize, j.Pause, j.RunCap = 500, 20*time.Millisecond, 20000
	done := make(chan struct{})
	go func() { j.RunOnce(context.Background()); close(done) }()
	var worst time.Duration
	for i := 0; i < 20; i++ {
		start := time.Now()
		var n int
		require.NoError(t, db.Get(&n, `SELECT COUNT(*) FROM live_sessions`))
		if d := time.Since(start); d > worst {
			worst = d
		}
		time.Sleep(10 * time.Millisecond) // sleep-ok: spread the probes over the run
	}
	<-done
	assert.Less(t, worst, 500*time.Millisecond, "a request waited %s for the retention job", worst)
	assert.Equal(t, 0, metricCount(t, db))
}
