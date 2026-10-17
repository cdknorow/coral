package store

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// insertMetric writes one raw call metric at t.
func insertMetric(t *testing.T, d *DB, op, session, board string, dur int64, isErr bool, at time.Time) {
	t.Helper()
	_, err := d.Exec(`INSERT INTO call_metrics(call_type, operation, agent_name, session_id, board_name, method, status_code, duration_ms, is_error, created_at)
		VALUES ('api', ?, ?, ?, ?, 'GET', 200, ?, ?, ?)`, op, "agent-"+session, session, board, dur, isErr, at.UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)
}

func tableRows(t *testing.T, d *DB, table string) int {
	t.Helper()
	var n int
	require.NoError(t, d.Get(&n, `SELECT COUNT(*) FROM `+table))
	return n
}

func summaryByKey(t *testing.T, d *DB, since time.Time) map[string]CallMetricSummary {
	t.Helper()
	rows, err := d.CallMetricSummary(context.Background(), since)
	require.NoError(t, err)
	out := map[string]CallMetricSummary{}
	for _, r := range rows {
		out[r.Operation+"|"+r.AgentName+"|"+r.BoardName] = r
	}
	return out
}

// #4098: 30 hours of raw rows (two operations, two agents, known durations and errors). Rolling
// up everything older than 24 hours leaves only the last day raw, one hourly row per hour and
// kind of call, and the call summary over the whole window is the same as before.
func TestRollupCallMetricsKeepsTheSummary(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Hour).Add(30 * time.Minute)
	for h := 0; h < 30; h++ {
		hourStart := now.Add(-time.Duration(h) * time.Hour).Truncate(time.Hour)
		for i := 0; i < 20; i++ {
			at := hourStart.Add(time.Duration(i) * 2 * time.Minute).Add(time.Duration(i) * time.Millisecond)
			insertMetric(t, d, "/api/board/{project}/messages", "s1", "team-a", int64(10+i), i%10 == 0, at)
			insertMetric(t, d, "/api/sessions/live", "s2", "team-b", int64(100*(i+1)), false, at)
		}
	}
	since := now.Add(-48 * time.Hour)
	before := summaryByKey(t, d, since)
	require.Len(t, before, 2)

	hours, rows, err := d.RollupCallMetrics(ctx, now.Add(-CallMetricsRawHours*time.Hour), 1000)
	require.NoError(t, err)
	assert.Equal(t, 5, hours, "hours 25 to 29 back are complete and older than a day")
	assert.EqualValues(t, 5*40, rows)
	assert.Equal(t, 25*40, tableRows(t, d, "call_metrics"), "the last day stays raw")
	assert.Equal(t, 5*2, tableRows(t, d, "call_metrics_hourly"), "one row per hour and kind of call")

	after := summaryByKey(t, d, since)
	require.Len(t, after, 2)
	for k, b := range before {
		a := after[k]
		assert.Equal(t, b.Calls, a.Calls, k)
		assert.Equal(t, b.Errors, a.Errors, k)
		assert.InDelta(t, b.AvgDurationMs, a.AvgDurationMs, 0.001, k)
	}

	// The percentiles of an hour: /api/sessions/live took 100..2000 ms.
	var p struct {
		P50 int64 `db:"p50_ms"`
		P95 int64 `db:"p95_ms"`
		N   int64 `db:"duration_n"`
	}
	require.NoError(t, d.Get(&p, `SELECT p50_ms, p95_ms, duration_n FROM call_metrics_hourly WHERE operation = '/api/sessions/live' ORDER BY hour LIMIT 1`))
	assert.EqualValues(t, 20, p.N)
	assert.EqualValues(t, 1000, p.P50)
	assert.EqualValues(t, 1900, p.P95)

	// Nothing more to roll up; a late row for a rolled-up hour adds to it.
	hours, _, err = d.RollupCallMetrics(ctx, now.Add(-CallMetricsRawHours*time.Hour), 1000)
	require.NoError(t, err)
	assert.Equal(t, 0, hours)
	old := now.Add(-29 * time.Hour).Truncate(time.Hour).Add(time.Minute)
	insertMetric(t, d, "/api/sessions/live", "s2", "team-b", 5, true, old)
	_, _, err = d.RollupCallMetrics(ctx, now.Add(-CallMetricsRawHours*time.Hour), 1000)
	require.NoError(t, err)
	var calls, errs int
	require.NoError(t, d.QueryRow(`SELECT calls, errors FROM call_metrics_hourly WHERE operation = '/api/sessions/live' AND hour = ?`, hourPrefix(old)).Scan(&calls, &errs))
	assert.Equal(t, 21, calls)
	assert.Equal(t, 1, errs)

	// The window prune removes old hourly rows only.
	n, err := d.PruneCallMetricsHourly(ctx, now.Add(-27*time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 2*2, n, "hours 28 and 29 back: the hour holding the cutoff is kept")
}

// #4098: the soak's load (about 25,000 calls an hour over a handful of kinds of call) stays small:
// a day of raw rows becomes 24 hourly rows per kind of call, whatever the number of calls.
func TestRollupCallMetricsIsBounded(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	start := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	_, err := d.Exec(`WITH RECURSIVE c(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM c WHERE i < 24*500-1)
		INSERT INTO call_metrics(call_type, operation, agent_name, session_id, board_name, duration_ms, is_error, created_at)
		SELECT 'api', CASE i % 3 WHEN 0 THEN '/a' WHEN 1 THEN '/b' ELSE '/c' END, 'x', 's', 'b', i % 97, 0,
			strftime('%Y-%m-%dT%H:%M:%fZ', ?, '+' || (i * 7) || ' seconds') FROM c`, start.Format("2006-01-02 15:04:05"))
	require.NoError(t, err)
	require.Equal(t, 24*500, tableRows(t, d, "call_metrics"))

	hours, rows, err := d.RollupCallMetrics(ctx, time.Now().Add(-CallMetricsRawHours*time.Hour), 1000)
	require.NoError(t, err)
	assert.EqualValues(t, 24*500, rows)
	assert.Equal(t, 0, tableRows(t, d, "call_metrics"))
	assert.LessOrEqual(t, tableRows(t, d, "call_metrics_hourly"), (hours)*3)
	assert.LessOrEqual(t, hours, 24)

	var got []string
	require.NoError(t, d.Select(&got, `SELECT DISTINCT hour FROM call_metrics_hourly ORDER BY hour`))
	assert.True(t, sort.StringsAreSorted(got))
	assert.Equal(t, hourPrefix(start), got[0], fmt.Sprint(got))
}

// percentile is nearest-rank.
func TestPercentile(t *testing.T) {
	v := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	assert.EqualValues(t, 5, percentile(v, 0.5))
	assert.EqualValues(t, 10, percentile(v, 0.95))
	assert.EqualValues(t, 0, percentile(nil, 0.5))
	assert.EqualValues(t, 7, percentile([]int64{7}, 0.95))
}
