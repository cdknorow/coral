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
// is left over goes in the next run.
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
	now       func() time.Time
}

// NewCallMetricsRetention returns the job with its production settings.
func NewCallMetricsRetention(db *store.DB) *CallMetricsRetention {
	return &CallMetricsRetention{
		db: db, ss: store.NewSessionStore(db), logger: slog.Default(),
		FirstRun: 2 * time.Minute, Interval: time.Hour,
		BatchSize: 5000, Pause: 50 * time.Millisecond, RunCap: 500_000,
		now: time.Now,
	}
}

// Run prunes FirstRun after startup, then every Interval, until ctx is done.
func (j *CallMetricsRetention) Run(ctx context.Context) error {
	wait := j.FirstRun
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		j.RunOnce(ctx)
		wait = j.Interval
	}
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
	if total > 0 || capped {
		j.logger.Info("call metrics retention", "deleted", total, "keep_days", days,
			"took_ms", j.now().Sub(started).Milliseconds(), "more_next_run", capped)
	}
	return total
}
