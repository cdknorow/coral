package background

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// #4163 part 2: the job rolls up only what is older than store.TokenUsageRawDays, a step at a
// time up to its cap, and a second run finds nothing more.
func TestTokenUsageRetentionKeepsTheLastMonthRaw(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	s := store.NewTokenUsageStore(db)
	now := time.Now().UTC()
	for i, age := range []time.Duration{40 * 24 * time.Hour, 35 * 24 * time.Hour, 31 * 24 * time.Hour, 29 * 24 * time.Hour, time.Hour} {
		require.NoError(t, s.RecordUsage(ctx, &store.TokenUsage{SessionID: "s", AgentName: "a", InputTokens: 10 + i, CostUSD: 0.5,
			RecordedAt: now.Add(-age).Format(store.ISOFormat)}))
	}
	before, err := s.GetSessionUsage(ctx, "s")
	require.NoError(t, err)

	j := NewTokenUsageRetention(db)
	j.Pause, j.HoursPerStep = time.Millisecond, 1
	hours, rows := j.RunOnce(ctx)
	assert.Equal(t, 3, hours, "40, 35 and 31 days old")
	assert.EqualValues(t, 3, rows)
	var raw int
	require.NoError(t, db.Get(&raw, `SELECT COUNT(*) FROM token_usage`))
	assert.Equal(t, 2, raw, "29 days and 1 hour old stay raw")
	after, err := s.GetSessionUsage(ctx, "s")
	require.NoError(t, err)
	assert.Equal(t, before, after)

	hours, _ = j.RunOnce(ctx)
	assert.Zero(t, hours)
}
