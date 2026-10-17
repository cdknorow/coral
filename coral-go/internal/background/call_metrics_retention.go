package background

import (
	"context"
	"log/slog"
	"time"

	"github.com/cdknorow/coral/internal/store"
)

// CallMetricsRetention deletes raw call metrics older than the retention window
// (the call_metrics_retention_days setting, 14 days by default) (#3261). The
// table had no retention: 6M rows and ~1.5 GB of a 2 GB sessions.db on a busy
// server. Every request shares the database's single connection, so the job
// deletes in short batches with a pause between them, and caps each run; what
// is left over goes in the next run. Raw rows older than a day are rolled into
// hourly rows first (#4098), which are kept for the same window.
type CallMetricsRetention struct {
	db     *store.DB
	ss     *store.SessionStore
	logger *slog.Logger

	// Tunables (fields so tests can shrink them).
	FirstRun  time.Duration // after startup
	Interval  time.Duration // between runs
	BatchSize int
	Pause     time.Duration // between batches: the connection is free for requests
	RunCap    int64         // rows per run at most
	// RollupHours is how many hours of raw rows one run rolls up at most (one transaction
	// each, with Pause between): after an upgrade the backlog clears over a few runs.
	RollupHours int
	now         func() time.Time
}

// NewCallMetricsRetention returns the job with its production settings.
func NewCallMetricsRetention(db *store.DB) *CallMetricsRetention {
	return &CallMetricsRetention{
		db: db, ss: store.NewSessionStore(db), logger: slog.Default(),
		FirstRun: 2 * time.Minute, Interval: time.Hour,
		BatchSize: 5000, Pause: 50 * time.Millisecond, RunCap: 500_000,
		RollupHours: 48, now: time.Now,
	}
}

// Run prunes FirstRun after startup, then every Interval, until ctx is done.
func (j *CallMetricsRetention) Run(ctx context.Context) error {
	return runEvery(ctx, j.FirstRun, j.Interval, func() { j.RunOnce(ctx) })
}

// RunOnce deletes expired rows, batch by batch, up to RunCap, and reports how
// many it deleted.
func (j *CallMetricsRetention) RunOnce(ctx context.Context) int64 {
	days := store.CallMetricsRetentionDefaultDays
	if settings, err := j.ss.GetSettings(ctx); err == nil {
		days = store.CallMetricsRetentionDays(settings)
	}
	cutoff := j.now().Add(-time.Duration(days) * 24 * time.Hour)
	started := j.now()
	var total int64
	capped := false
	for ctx.Err() == nil {
		batch := j.BatchSize
		if left := j.RunCap - total; left < int64(batch) {
			batch = int(left)
		}
		if batch <= 0 {
			capped = true
			break
		}
		n, err := j.db.PruneCallMetrics(ctx, cutoff, batch)
		if err != nil {
			if ctx.Err() == nil {
				j.logger.Warn("call metrics retention: delete failed", "error", err)
			}
			break
		}
		total += n
		if n < int64(batch) {
			break // nothing older left
		}
		select {
		case <-ctx.Done():
		case <-time.After(j.Pause):
		}
	}
	// Rows past the window are deleted, never rolled up: while the prune is capped some are
	// left, so the rollup waits for a run that finished it.
	var rolledHours int
	var rolledRows int64
	if !capped {
		rolledHours, rolledRows = j.rollup(ctx)
	}
	hourly, err := j.db.PruneCallMetricsHourly(ctx, cutoff)
	if err != nil && ctx.Err() == nil {
		j.logger.Warn("call metrics retention: deleting old hourly rows failed", "error", err)
	}
	if total > 0 || capped || rolledHours > 0 || hourly > 0 {
		j.logger.Info("call metrics retention", "deleted", total, "keep_days", days,
			"rolled_up_hours", rolledHours, "rolled_up_rows", rolledRows, "hourly_deleted", hourly,
			"took_ms", j.now().Sub(started).Milliseconds(), "more_next_run", capped || rolledHours >= j.RollupHours)
	}
	return total
}

// rollup rolls raw rows older than store.CallMetricsRawHours into hourly rows, one hour per
// transaction with Pause between them, at most RollupHours hours.
func (j *CallMetricsRetention) rollup(ctx context.Context) (hours int, rows int64) {
	cutoff := j.now().Add(-store.CallMetricsRawHours * time.Hour)
	for hours < j.RollupHours && ctx.Err() == nil {
		h, n, err := j.db.RollupCallMetrics(ctx, cutoff, 1)
		if err != nil {
			if ctx.Err() == nil {
				j.logger.Warn("call metrics retention: rollup failed", "error", err)
			}
			break
		}
		if h == 0 {
			break // nothing older than a day left
		}
		hours += h
		rows += n
		select {
		case <-ctx.Done():
		case <-time.After(j.Pause):
		}
	}
	return hours, rows
}

// runEvery calls fn after first and then every interval until ctx ends, and returns ctx's error.
// The retention jobs' loop (#3824: it was written twice).
func runEvery(ctx context.Context, first, interval time.Duration, fn func()) error {
	wait := first
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		fn()
		wait = interval
	}
}
