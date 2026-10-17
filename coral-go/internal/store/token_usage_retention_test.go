package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// usageSnapshot is every reader of token usage totals, as JSON, so a roll-up can be checked to
// change none of them.
func usageSnapshot(t *testing.T, s *TokenUsageStore, sessions []string, since string) map[string]string {
	t.Helper()
	ctx := context.Background()
	out := map[string]string{}
	put := func(name string, v any, err error) {
		t.Helper()
		require.NoError(t, err, name)
		b, err := json.Marshal(v)
		require.NoError(t, err)
		out[name] = string(b)
	}
	for _, id := range sessions {
		u, err := s.GetSessionUsage(ctx, id)
		put("session "+id, u, err)
	}
	team := int64(1)
	v1, err := s.GetTeamUsage(ctx, team)
	put("team", v1, err)
	v2, err := s.GetBoardUsage(ctx, "board-a")
	put("board", v2, err)
	v3, err := s.GetUsageSummary(ctx, since)
	put("summary", v3, err)
	v4, err := s.GetUsageSummaryByAgent(ctx, since)
	for i := range v4 {
		v4[i].FirstSeen = "" // the first raw row's time within its hour is not kept
	}
	put("by agent", v4, err)
	v5, err := s.ListUsage(ctx, UsageFilter{Since: since})
	put("list", v5, err)
	v6, err := s.GetLatestUsageBySessionIDs(ctx, sessions)
	put("latest", v6, err)
	v7, err := s.GetLatestTurnContextBySessionIDs(ctx, sessions)
	put("context", v7, err)
	v8, err := s.GetUsageTimeSeries(ctx, since, "1d")
	put("series", v8, err)
	v9, err := s.GetUsageSummaryByBoard(ctx, since)
	put("by board", v9, err)
	return out
}

// #4163 part 2: 40 days of usage, three sessions over two boards and two models, one with proxy
// rows besides its transcript rows. Rolling up everything older than 30 days leaves the newest
// month raw and changes no total, count or latest value any reader reports; a turn re-read from
// an old transcript is not counted twice; and a later rebuild of the per-session sums still
// includes the rolled-up rows.
func TestTokenUsageRollupKeepsEveryTotal(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	s := NewTokenUsageStore(d)
	now := time.Now().UTC().Truncate(time.Hour)
	sessions := []string{"s-a", "s-b", "s-c"}
	team := int64(1)
	start := now.Add(-40 * 24 * time.Hour)
	n := 0
	for h := 0; h < 40*24; h += 5 {
		at := start.Add(time.Duration(h) * time.Hour)
		for i, id := range sessions {
			n++
			u := &TokenUsage{
				SessionID: id, AgentName: "agent-" + id, AgentType: "claude", BoardName: ptrTo([]string{"board-a", "board-a", "board-b"}[i]),
				InputTokens: 100 + n%7, OutputTokens: 20 + n%5, CacheReadTokens: 1000 + n%11, CacheWriteTokens: n % 3,
				TotalTokens: 120 + n%12, CostUSD: float64(n%8) * 0.25, Model: []string{"claude-opus-5-5", "claude-sonnet-5-5"}[n%2],
				SessionStartAt: start.Format(ISOFormat), LastActivityAt: at.Add(time.Duration(i) * time.Minute).Format(ISOFormat),
				RecordedAt: at.Add(time.Duration(i)*time.Minute + time.Duration(n%50)*time.Second).Format(ISOFormat),
			}
			if i < 2 {
				u.TeamID = &team
			}
			require.NoError(t, s.RecordUsage(ctx, u))
			if id == "s-c" && h%25 == 0 { // proxy rows besides the transcript's: not counted
				p := *u
				p.Source, p.RecordedAt = "proxy", at.Add(30*time.Minute).Format(ISOFormat)
				require.NoError(t, s.RecordUsage(ctx, &p))
			}
		}
	}
	since := now.Add(-35 * 24 * time.Hour).Format(ISOFormat) // an hour boundary
	before := usageSnapshot(t, s, sessions, since)
	beforeAll := usageSnapshot(t, s, sessions, "")
	var rawBefore int
	require.NoError(t, d.Get(&rawBefore, `SELECT COUNT(*) FROM token_usage`))

	hours, rows, err := d.RollupTokenUsage(ctx, now.Add(-TokenUsageRawDays*24*time.Hour), 10000)
	require.NoError(t, err)
	assert.Positive(t, hours)
	var rawAfter, hourly, oldRaw int
	require.NoError(t, d.Get(&rawAfter, `SELECT COUNT(*) FROM token_usage`))
	require.NoError(t, d.Get(&hourly, `SELECT COUNT(*) FROM token_usage_hourly`))
	require.NoError(t, d.Get(&oldRaw, `SELECT COUNT(*) FROM token_usage WHERE recorded_at < ?`, now.Add(-TokenUsageRawDays*24*time.Hour).Format(ISOFormat)))
	assert.EqualValues(t, rawBefore-rawAfter, rows)
	assert.Zero(t, oldRaw, "nothing older than the raw window is left raw")
	assert.Positive(t, hourly)
	t.Logf("rolled up %d hours: %d raw rows into %d hourly rows; %d raw rows kept", hours, rows, hourly, rawAfter)

	assert.Equal(t, before, usageSnapshot(t, s, sessions, since), "every reader, since 35 days ago")
	assert.Equal(t, beforeAll, usageSnapshot(t, s, sessions, ""), "every reader, all time")

	// An old transcript read again: its turn is not counted twice.
	old := &TokenUsage{SessionID: "s-a", AgentName: "agent-s-a", InputTokens: 999, CostUSD: 9,
		RecordedAt: start.Add(5 * time.Hour).Format(ISOFormat)}
	require.NoError(t, s.RecordUsage(ctx, old))
	assert.Equal(t, beforeAll, usageSnapshot(t, s, sessions, ""), "a re-read old turn is ignored")

	// Nothing more to roll up.
	hours, _, err = d.RollupTokenUsage(ctx, now.Add(-TokenUsageRawDays*24*time.Hour), 10000)
	require.NoError(t, err)
	assert.Zero(t, hours)

	// A raw row changed later (a rename rebuilds the per-session sums): the rebuild still counts
	// the rolled-up hours.
	_, err = d.Exec(`UPDATE token_usage SET agent_name = 'renamed' WHERE id = (SELECT MAX(id) FROM token_usage WHERE session_id = 's-a')`)
	require.NoError(t, err)
	latest, err := s.GetLatestUsageBySessionIDs(ctx, sessions)
	require.NoError(t, err)
	full, err := s.GetSessionUsage(ctx, "s-a")
	require.NoError(t, err)
	assert.Equal(t, full.InputTokens, latest["s-a"].InputTokens)
	assert.Equal(t, full.CostUSD, latest["s-a"].CostUSD)
	assert.Equal(t, full.NumTurns, latest["s-a"].NumTurns, fmt.Sprint("turns ", full.NumTurns))
}

func TestTokenHourBounds(t *testing.T) {
	from, to, ok := tokenHourBounds("2026-09-16T23")
	assert.True(t, ok)
	assert.Equal(t, "2026-09-16T23", from)
	assert.Equal(t, "2026-09-17T00", to)
	_, to, ok = tokenHourBounds("2026-09-16 05")
	assert.True(t, ok)
	assert.Equal(t, "2026-09-16 06", to)
	for _, bad := range []string{"", "garbage-here!", "2026-09-16X05", "2026-13-16T05"} {
		_, _, ok := tokenHourBounds(bad)
		assert.False(t, ok, bad)
	}
}

func ptrTo[T any](v T) *T { return &v }
