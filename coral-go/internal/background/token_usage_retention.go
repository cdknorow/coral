package background

import (
	"context"
	"log/slog"
	"time"

	"github.com/cdknorow/coral/internal/store"
)

// TokenUsageRetention rolls raw token usage older than store.TokenUsageRawDays into hourly rows
// (#4163 part 2), a few hours at a time with a pause between them, so the single writer stays
// free for requests; what is left goes in the next run. Totals never change.
type TokenUsageRetention struct {
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

// NewTokenUsageRetention returns the job with its production settings.
func NewTokenUsageRetention(db *store.DB) *TokenUsageRetention {
	return &TokenUsageRetention{
		db: db, logger: slog.Default(),
		FirstRun: 3 * time.Minute, Interval: time.Hour,
		HoursPerStep: 6, Pause: 50 * time.Millisecond, RunCap: 24 * 14,
		now: time.Now,
	}
}

// Run rolls up FirstRun after startup, then every Interval, until ctx is done.
func (j *TokenUsageRetention) Run(ctx context.Context) error {
	return runEvery(ctx, j.FirstRun, j.Interval, func() { j.RunOnce(ctx) })
}

// RunOnce rolls up what is older than the raw window, up to RunCap hours, and reports how many
// hours and raw rows it rolled up.
func (j *TokenUsageRetention) RunOnce(ctx context.Context) (hours int, rows int64) {
	cutoff := j.now().Add(-store.TokenUsageRawDays * 24 * time.Hour)
	started := j.now()
	for hours < j.RunCap && ctx.Err() == nil {
		step := j.HoursPerStep
		if left := j.RunCap - hours; left < step {
			step = left
		}
		h, n, err := j.db.RollupTokenUsage(ctx, cutoff, step)
		hours += h
		rows += n
		if err != nil {
			if ctx.Err() == nil {
				j.logger.Warn("token usage retention: roll-up failed", "error", err)
			}
			break
		}
		if h < step {
			break // nothing older left
		}
		select {
		case <-ctx.Done():
		case <-time.After(j.Pause):
		}
	}
	if hours > 0 {
		j.logger.Info("token usage retention", "hours", hours, "raw_rows", rows, "keep_days", store.TokenUsageRawDays,
			"took_ms", j.now().Sub(started).Milliseconds())
	}
	return hours, rows
}
