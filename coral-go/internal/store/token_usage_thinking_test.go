package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func thinkingKey(rows []ThinkingUsageSummary) map[string]ThinkingUsageSummary {
	m := map[string]ThinkingUsageSummary{}
	for _, r := range rows {
		m[r.AgentType+"|"+r.Thinking] = r
	}
	return m
}

// #4969: the Cost view's breakdown by thinking level is kept as calls are recorded: only calls whose turn recorded a level, once each,
// only from the transcripts (not the proxy), by UTC hour.
func TestUsageByThinkingIsKeptAsCallsAreRecorded(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()
	rec := func(sid, typ, level, source, at string, in, out int, think int, cost float64) {
		t.Helper()
		require.NoError(t, s.RecordUsage(ctx, &TokenUsage{SessionID: sid, AgentName: sid, AgentType: typ, Thinking: level, ThinkingTokens: think, Source: source,
			RecordedAt: at, InputTokens: in, OutputTokens: out, TotalTokens: in + out, CostUSD: cost}))
	}
	rec("s1", "claude", "high", "jsonl", "2026-10-01T10:00:00Z", 100, 10, 4, 1.0)
	rec("s1", "claude", "high", "jsonl", "2026-10-01T10:30:00Z", 50, 5, 2, 0.5)
	rec("s1", "claude", "low", "jsonl", "2026-10-01T11:00:00Z", 20, 2, 0, 0.1)
	rec("s2", "claude", "", "jsonl", "2026-10-01T11:10:00Z", 999, 9, 0, 9.9)     // recorded no level: not here
	rec("s3", "codex", "medium", "jsonl", "2026-10-01T12:00:00Z", 70, 7, 3, 0.2) // another type
	rec("s4", "claude", "high", "proxy", "2026-10-01T10:45:00Z", 777, 7, 0, 7.7) // not from a transcript: the same call would count twice

	// The same call reported again is ignored, as the usage rows ignore it.
	rec("s1", "claude", "high", "jsonl", "2026-10-01T10:00:00Z", 100, 10, 4, 1.0)

	rows, err := s.GetUsageByThinking(ctx, "")
	require.NoError(t, err)
	got := thinkingKey(rows)
	require.Len(t, got, 3, "%+v", rows)
	high := got["claude|high"]
	assert.EqualValues(t, 150, high.InputTokens)
	assert.EqualValues(t, 15, high.OutputTokens)
	assert.EqualValues(t, 165, high.TotalTokens)
	assert.EqualValues(t, 6, high.ThinkingTokens)
	assert.InDelta(t, 1.5, high.CostUSD, 1e-9)
	assert.EqualValues(t, 2, high.NumCalls, "the repeated report did not count")
	assert.EqualValues(t, 20, got["claude|low"].InputTokens)
	assert.EqualValues(t, 3, got["codex|medium"].ThinkingTokens)
	assert.NotContains(t, got, "claude|", "no level: not tracked here")
	assert.Equal(t, "claude", rows[0].AgentType, "the dearest row first")

	// By hour: from the hour since falls in.
	rows, err = s.GetUsageByThinking(ctx, "2026-10-01T11:30:00Z")
	require.NoError(t, err)
	got = thinkingKey(rows)
	assert.NotContains(t, got, "claude|high", "the 10:00 hour is before")
	assert.EqualValues(t, 20, got["claude|low"].InputTokens, "the 11:00 hour holds since")
	assert.Contains(t, got, "codex|medium")
}
