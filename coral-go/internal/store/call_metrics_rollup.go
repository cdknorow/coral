package store

import (
	"context"
	"sort"
	"time"
)

// Call metrics rollup (#4098, from the #3995 soak: call_metrics grew ~3.9 MB an hour, ~1.3 GB
// at 14 days). Raw rows are kept for CallMetricsRawHours; older ones are rolled into one row per
// hour and kind of call (call_metrics_hourly: counts, error count, duration sum, p50 and p95),
// kept for the same retention window as before. The call summary reads both, so it still covers
// the whole window. About 800 raw rows become one.

// CallMetricsRawHours is how long raw call metrics are kept before they are rolled up.
const CallMetricsRawHours = 24

// hourPrefix is the hour of t as created_at's leading characters ("2006-01-02T15"): created_at is
// RFC 3339 UTC text, so the rows of an hour are exactly those between its prefix and the next
// hour's. (Comparing against a full timestamp would not do: "...00.5Z" sorts before "...00Z".)
func hourPrefix(t time.Time) string { return t.UTC().Format("2006-01-02T15") }

type rollupKey struct {
	callType, operation, agentName, sessionID, boardName string
}

type rollupAcc struct {
	calls, errors int64
	durSum        int64
	durations     []int64 // rows that recorded a duration
}

// percentile is the nearest-rank p-th percentile of sorted values (0 for none).
func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(p*float64(len(sorted))+0.999999) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

// RollupCallMetrics rolls raw call metrics from hours that ended before cutoff into
// call_metrics_hourly, oldest hour first, at most maxHours hours, one transaction per hour (the
// rows are summed and deleted together, so a crash leaves either the raw rows or their rollup,
// never both). It reports how many hours and raw rows it rolled up.
func (d *DB) RollupCallMetrics(ctx context.Context, cutoff time.Time, maxHours int) (hours int, rows int64, err error) {
	end := hourPrefix(cutoff) // the hour holding cutoff is not complete
	for hours < maxHours && ctx.Err() == nil {
		var first string
		if err := d.GetContext(ctx, &first, `SELECT COALESCE(MIN(substr(created_at, 1, 13)), '') FROM call_metrics WHERE created_at < ?`, end); err != nil {
			return hours, rows, err
		}
		if first == "" || first >= end {
			return hours, rows, nil
		}
		n, err := d.rollupHour(ctx, first)
		if err != nil {
			return hours, rows, err
		}
		hours++
		rows += n
	}
	return hours, rows, ctx.Err()
}

// rollupHour rolls the raw rows of one hour (its created_at prefix) into call_metrics_hourly.
func (d *DB) rollupHour(ctx context.Context, hour string) (int64, error) {
	start, err := time.Parse("2006-01-02T15", hour)
	if err != nil {
		// A created_at that is not a timestamp: its rows can't be placed in an hour. They are
		// dropped (metrics, not user data), or this hour would be picked again on every run.
		_, err := d.ExecContext(ctx, `DELETE FROM call_metrics WHERE substr(created_at, 1, 13) = ?`, hour)
		return 0, err
	}
	next := hourPrefix(start.Add(time.Hour))
	tx, err := d.BeginTxx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var raw []struct {
		CallType   string `db:"call_type"`
		Operation  string `db:"operation"`
		AgentName  string `db:"agent_name"`
		SessionID  string `db:"session_id"`
		BoardName  string `db:"board_name"`
		DurationMs *int64 `db:"duration_ms"`
		IsError    int64  `db:"is_error"`
	}
	if err := tx.SelectContext(ctx, &raw, `SELECT call_type, operation, COALESCE(agent_name, '') AS agent_name,
		COALESCE(session_id, '') AS session_id, COALESCE(board_name, '') AS board_name, duration_ms, is_error
		FROM call_metrics WHERE created_at >= ? AND created_at < ?`, hour, next); err != nil {
		return 0, err
	}
	groups := map[rollupKey]*rollupAcc{}
	for _, r := range raw {
		k := rollupKey{r.CallType, r.Operation, r.AgentName, r.SessionID, r.BoardName}
		a := groups[k]
		if a == nil {
			a = &rollupAcc{}
			groups[k] = a
		}
		a.calls++
		if r.IsError != 0 {
			a.errors++
		}
		if r.DurationMs != nil {
			a.durSum += *r.DurationMs
			a.durations = append(a.durations, *r.DurationMs)
		}
	}
	for k, a := range groups {
		sort.Slice(a.durations, func(i, j int) bool { return a.durations[i] < a.durations[j] })
		// A late row for an hour already rolled up adds to it; its percentiles keep the
		// larger sample's.
		if _, err := tx.ExecContext(ctx, `INSERT INTO call_metrics_hourly
			(hour, call_type, operation, agent_name, session_id, board_name, calls, errors, duration_sum_ms, duration_n, p50_ms, p95_ms)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(hour, call_type, operation, agent_name, session_id, board_name) DO UPDATE SET
				p50_ms = CASE WHEN excluded.duration_n > duration_n THEN excluded.p50_ms ELSE p50_ms END,
				p95_ms = CASE WHEN excluded.duration_n > duration_n THEN excluded.p95_ms ELSE p95_ms END,
				calls = calls + excluded.calls, errors = errors + excluded.errors,
				duration_sum_ms = duration_sum_ms + excluded.duration_sum_ms, duration_n = duration_n + excluded.duration_n`,
			hour, k.callType, k.operation, k.agentName, k.sessionID, k.boardName,
			a.calls, a.errors, a.durSum, len(a.durations), percentile(a.durations, 0.50), percentile(a.durations, 0.95)); err != nil {
			return 0, err
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM call_metrics WHERE created_at >= ? AND created_at < ?`, hour, next)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneCallMetricsHourly deletes hourly rollups for hours before cutoff (the retention window).
func (d *DB) PruneCallMetricsHourly(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := d.ExecContext(ctx, `DELETE FROM call_metrics_hourly WHERE hour < ?`, hourPrefix(cutoff))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
