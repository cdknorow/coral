package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func byModelKey(rows []ModelUsageSummary) map[string]ModelUsageSummary {
	m := map[string]ModelUsageSummary{}
	for _, r := range rows {
		m[r.AgentType+"|"+r.Model] = r
	}
	return m
}

// #4588: usage is grouped by the model each row recorded, with the agent type alongside; a
// session that switched models is split between the models' rows, and a row with no model is
// the "" bucket, never a guess.
func TestGetUsageSummaryByModel_SplitsBySwitchAndKeepsAnUnknownBucket(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()
	rec := func(sid, typ, model, at string, in, out, cr, cw int, cost float64) {
		t.Helper()
		require.NoError(t, s.RecordUsage(ctx, &TokenUsage{SessionID: sid, AgentName: sid, AgentType: typ, Model: model,
			RecordedAt: at, InputTokens: in, OutputTokens: out, CacheReadTokens: cr, CacheWriteTokens: cw,
			TotalTokens: in + out, CostUSD: cost}))
	}
	rec("s1", "claude", "claude-opus-5-5", "2026-10-01T10:00:00Z", 100, 10, 1, 2, 1.0)
	rec("s1", "claude", "claude-sonnet-5-5", "2026-10-01T10:05:00Z", 200, 20, 3, 4, 0.5) // /model switch
	rec("s1", "claude", "claude-opus-5-5", "2026-10-01T10:10:00Z", 50, 5, 0, 0, 0.25)
	rec("s2", "claude", "claude-opus-5-5", "2026-10-01T11:00:00Z", 10, 1, 0, 0, 0.1)
	rec("s3", "codex", "gpt-5-codex", "2026-10-01T12:00:00Z", 70, 7, 0, 0, 0.2)
	rec("s4", "agy", "", "2026-10-01T13:00:00Z", 30, 3, 0, 0, 0)
	rec("s5", "claude", "", "2026-10-01T14:00:00Z", 5, 1, 0, 0, 0.01) // no model recorded

	rows, err := s.GetUsageSummaryByModel(ctx, "")
	require.NoError(t, err)
	got := byModelKey(rows)
	require.Len(t, got, 5, "%+v", rows)

	opus := got["claude|claude-opus-5-5"]
	assert.EqualValues(t, 160, opus.InputTokens)
	assert.EqualValues(t, 16, opus.OutputTokens)
	assert.EqualValues(t, 1, opus.CacheReadTokens)
	assert.EqualValues(t, 2, opus.CacheWriteTokens)
	assert.InDelta(t, 1.35, opus.CostUSD, 1e-9)
	assert.Equal(t, 2, opus.NumSessions, "s1 and s2")
	sonnet := got["claude|claude-sonnet-5-5"]
	assert.EqualValues(t, 200, sonnet.InputTokens)
	assert.Equal(t, 1, sonnet.NumSessions, "the switching session is in the sonnet row too")
	assert.EqualValues(t, 70, got["codex|gpt-5-codex"].InputTokens)
	assert.EqualValues(t, 30, got["agy|"].InputTokens, "agy without a model: the unknown bucket under its own type")
	assert.EqualValues(t, 5, got["claude|"].InputTokens, "an unknown model is its own row, not folded into a named one")
	assert.Equal(t, "claude-opus-5-5", rows[0].Model, "the dearest row first")
}

// The model survives the hourly rollup (rows older than TokenUsageRawDays), so history splits too.
func TestGetUsageSummaryByModel_IncludesRolledUpHistoryAndHonoursSince(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()
	old := time.Now().UTC().Add(-45 * 24 * time.Hour)
	for i, m := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-opus-5-5"} {
		require.NoError(t, s.RecordUsage(ctx, &TokenUsage{SessionID: "old", AgentName: "a", AgentType: "claude", Model: m,
			RecordedAt: old.Add(time.Duration(i) * time.Second).Format("2006-01-02T15:04:05Z"), InputTokens: 10, TotalTokens: 10, CostUSD: 1}))
	}
	recent := time.Now().UTC().Add(-time.Hour).Format("2006-01-02T15:04:05Z")
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{SessionID: "new", AgentName: "b", AgentType: "claude", Model: "claude-opus-5-5",
		RecordedAt: recent, InputTokens: 7, TotalTokens: 7, CostUSD: 1}))
	_, rolled, err := db.RollupTokenUsage(ctx, time.Now().Add(-TokenUsageRawDays*24*time.Hour), 10000)
	require.NoError(t, err)
	require.EqualValues(t, 3, rolled)

	rows, err := s.GetUsageSummaryByModel(ctx, "")
	require.NoError(t, err)
	got := byModelKey(rows)
	assert.EqualValues(t, 27, got["claude|claude-opus-5-5"].InputTokens)
	assert.EqualValues(t, 10, got["claude|claude-sonnet-5-5"].InputTokens)

	rows, err = s.GetUsageSummaryByModel(ctx, time.Now().UTC().Add(-24*time.Hour).Format("2006-01-02T15:04:05Z"))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.EqualValues(t, 7, rows[0].InputTokens)
}

func TestGetUsageSummaryByModel_EmptyIsAnEmptyList(t *testing.T) {
	rows, err := NewTokenUsageStore(openTestDB(t)).GetUsageSummaryByModel(context.Background(), "")
	require.NoError(t, err)
	assert.NotNil(t, rows, "the route answers [] and not null")
	assert.Empty(t, rows)
}
