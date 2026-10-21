package background

import (
	"context"
	"log/slog"
	"time"

	"github.com/subgentic/coral/internal/store"
)

// TokenUsageHourAgg keeps the Cost view's hourly aggregate of token usage up to date (#4752): every minute it recomputes the hours that are over
// and have changed since (store.RefreshTokenUsageHourAgg), a few at a time with a pause between them so the single writer stays free for
// requests. The first run after an upgrade builds every hour of the history this way, a few hundred a minute; until then the Cost view reads the
// raw rows of the hours not built yet, so its numbers are right at every moment and only its speed catches up.
type TokenUsageHourAgg struct {
	db     *store.DB
	logger *slog.Logger

	// Tunables (fields so tests can shrink them).
	FirstRun     time.Duration
	Interval     time.Duration
	HoursPerStep int
	Pause        time.Duration
	RunCap       int // hours per run at most
	now          func() time.Time
}

// NewTokenUsageHourAgg returns the job with its production settings.
func NewTokenUsageHourAgg(db *store.DB) *TokenUsageHourAgg {
	return &TokenUsageHourAgg{
		db: db, logger: slog.Default(),
		FirstRun: 20 * time.Second, Interval: time.Minute,
		HoursPerStep: 8, Pause: 50 * time.Millisecond, RunCap: 24 * 30,
		now: time.Now,
	}
}

// Run refreshes FirstRun after startup, then every Interval, until ctx is done.
func (j *TokenUsageHourAgg) Run(ctx context.Context) error {
	return runEvery(ctx, j.FirstRun, j.Interval, func() { j.RunOnce(ctx) })
}

// RunOnce refreshes up to RunCap dirty hours and reports how many it did.
func (j *TokenUsageHourAgg) RunOnce(ctx context.Context) int {
	started := j.now()
	hours := 0
	for hours < j.RunCap && ctx.Err() == nil {
		step := j.HoursPerStep
		if left := j.RunCap - hours; left < step {
			step = left
		}
		n, err := j.db.RefreshTokenUsageHourAgg(ctx, j.now(), step)
		hours += n
		if err != nil {
			if ctx.Err() == nil {
				j.logger.Warn("token usage hour aggregate: refresh failed", "error", err)
			}
			break
		}
		if n < step {
			break // nothing dirty left
		}
		select {
		case <-ctx.Done():
		case <-time.After(j.Pause):
		}
	}
	if hours > 2 {
		j.logger.Info("token usage hour aggregate", "hours", hours, "took_ms", j.now().Sub(started).Milliseconds())
	}
	return hours
}
