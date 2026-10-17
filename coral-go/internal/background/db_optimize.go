package background

import (
	"context"
	"log/slog"
	"sort"
	"time"
)

// Optimizer is a database that can refresh its query planner's statistics (PRAGMA optimize).
type Optimizer interface {
	Optimize(ctx context.Context, first bool) error
}

// DBOptimize runs PRAGMA optimize on Coral's databases (#4142): a fuller pass a little after
// startup (so it never adds to the time before the dashboard answers), then a plain one every
// Interval, as SQLite recommends for long-lived connections. Each pass is bounded by
// analysis_limit; neither changes any data.
type DBOptimize struct {
	targets  map[string]Optimizer
	First    time.Duration
	Interval time.Duration
	logger   *slog.Logger
}

// NewDBOptimize returns the job with its production timings.
func NewDBOptimize(targets map[string]Optimizer) *DBOptimize {
	return &DBOptimize{targets: targets, First: 2 * time.Minute, Interval: 6 * time.Hour, logger: slog.Default()}
}

// Run optimizes First after startup, then every Interval, until ctx is done.
func (j *DBOptimize) Run(ctx context.Context) error {
	first := true
	return runEvery(ctx, j.First, j.Interval, func() {
		j.RunOnce(ctx, first)
		first = false
	})
}

// RunOnce optimizes every target once, in name order.
func (j *DBOptimize) RunOnce(ctx context.Context, first bool) {
	names := make([]string, 0, len(j.targets))
	for n := range j.targets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		start := time.Now()
		if err := j.targets[n].Optimize(ctx, first); err != nil {
			if ctx.Err() == nil {
				j.logger.Warn("database optimize failed", "database", n, "error", err)
			}
			continue
		}
		j.logger.Debug("database optimized", "database", n, "first", first, "took_ms", time.Since(start).Milliseconds())
	}
}
