package background

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// #4752: the job builds the hours that are over, a few per run up to its cap, never the hour still being written, and a second run finds nothing
// more; the Cost view's numbers are the same before and after.
func TestTokenUsageHourAggBuildsFinishedHoursUpToItsCap(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	s := store.NewTokenUsageStore(db)
	now := time.Date(2026, 10, 18, 14, 20, 0, 0, time.UTC)
	for i, age := range []time.Duration{50 * time.Hour, 26 * time.Hour, 3 * time.Hour, 20 * time.Minute} {
		require.NoError(t, s.RecordUsage(ctx, &store.TokenUsage{SessionID: "s", AgentName: "a", AgentType: "claude", InputTokens: 10 + i, CostUSD: 0.5,
			RecordedAt: now.Add(-age).Format(store.ISOFormat)}))
	}
	since := "2026-10-16T00:00:00Z"
	before, err := s.GetUsageSummary(ctx, since)
	require.NoError(t, err)

	j := NewTokenUsageHourAgg(db)
	j.now, j.Pause, j.HoursPerStep, j.RunCap = func() time.Time { return now }, time.Millisecond, 1, 2
	assert.Equal(t, 2, j.RunOnce(ctx), "the cap")
	assert.Equal(t, 1, j.RunOnce(ctx), "the third finished hour")
	assert.Equal(t, 0, j.RunOnce(ctx), "the hour still being written is not built")
	var built int
	require.NoError(t, db.Get(&built, `SELECT COUNT(DISTINCT hour) FROM token_usage_hour_agg`))
	assert.Equal(t, 3, built)
	after, err := s.GetUsageSummary(ctx, since)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}
