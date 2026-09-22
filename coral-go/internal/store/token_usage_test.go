package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenUsageStore_RecordAndGet(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	err := s.RecordUsage(ctx, &TokenUsage{
		SessionID:    "sess-1",
		AgentName:    "my-agent",
		AgentType:    "claude",
		InputTokens:  1000,
		OutputTokens: 500,
		TotalTokens:  1500,
		CostUSD:      0.03,
		NumTurns:     5,
	})
	require.NoError(t, err)

	got, err := s.GetSessionUsage(ctx, "sess-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "sess-1", got.SessionID)
	assert.Equal(t, 1000, got.InputTokens)
	assert.Equal(t, 500, got.OutputTokens)
	assert.Equal(t, 1500, got.TotalTokens)
	assert.InDelta(t, 0.03, got.CostUSD, 0.001)
	assert.Equal(t, 1, got.NumTurns) // COUNT(*) of records
}

func TestTokenUsageStore_GetSessionUsage_Latest(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	// Record two snapshots for the same session
	err := s.RecordUsage(ctx, &TokenUsage{
		SessionID: "sess-1", AgentName: "a", InputTokens: 100, OutputTokens: 50, TotalTokens: 150, CostUSD: 0.01,
	})
	require.NoError(t, err)

	err = s.RecordUsage(ctx, &TokenUsage{
		SessionID: "sess-1", AgentName: "a", InputTokens: 200, OutputTokens: 100, TotalTokens: 300, CostUSD: 0.02,
	})
	require.NoError(t, err)

	// Should return the sum of all per-turn records
	got, err := s.GetSessionUsage(ctx, "sess-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 450, got.TotalTokens) // 150 + 300
}

func TestTokenUsageStore_GetSessionUsage_NotFound(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	got, err := s.GetSessionUsage(ctx, "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestTokenUsageStore_GetTeamUsage(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	teamID := int64(1)
	// Two sessions in the same team, each with 2 snapshots
	err := s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s1", AgentName: "a1", TeamID: &teamID, InputTokens: 100, TotalTokens: 150, CostUSD: 0.01,
	})
	require.NoError(t, err)
	err = s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s1", AgentName: "a1", TeamID: &teamID, InputTokens: 200, TotalTokens: 300, CostUSD: 0.02,
	})
	require.NoError(t, err)
	err = s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s2", AgentName: "a2", TeamID: &teamID, InputTokens: 500, TotalTokens: 800, CostUSD: 0.05,
	})
	require.NoError(t, err)

	summary, err := s.GetTeamUsage(ctx, teamID)
	require.NoError(t, err)
	// Should sum all per-turn records: s1(150+300) + s2(800) = 1250
	assert.Equal(t, int64(1250), summary.TotalTokens)
	assert.InDelta(t, 0.08, summary.CostUSD, 0.001)
	assert.Equal(t, 2, summary.NumSessions)
}

func TestTokenUsageStore_GetBoardUsage(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	board := "my-board"
	err := s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s1", AgentName: "a1", BoardName: &board, TotalTokens: 500, CostUSD: 0.03,
	})
	require.NoError(t, err)
	err = s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s2", AgentName: "a2", BoardName: &board, TotalTokens: 300, CostUSD: 0.02,
	})
	require.NoError(t, err)

	summary, err := s.GetBoardUsage(ctx, "my-board")
	require.NoError(t, err)
	assert.Equal(t, int64(800), summary.TotalTokens)
	assert.InDelta(t, 0.05, summary.CostUSD, 0.001)
}

func TestTokenUsageStore_GetUsageSummary(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	err := s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s1", AgentName: "a1", AgentType: "claude", TotalTokens: 1000, CostUSD: 0.05,
	})
	require.NoError(t, err)
	err = s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s2", AgentName: "a2", AgentType: "gemini", TotalTokens: 2000, CostUSD: 0.01,
	})
	require.NoError(t, err)

	summaries, err := s.GetUsageSummary(ctx, "")
	require.NoError(t, err)
	assert.Len(t, summaries, 2)

	// Find claude summary
	for _, s := range summaries {
		if s.AgentType == "claude" {
			assert.Equal(t, int64(1000), s.TotalTokens)
			assert.Equal(t, 1, s.NumSessions)
		}
		if s.AgentType == "gemini" {
			assert.Equal(t, int64(2000), s.TotalTokens)
		}
	}
}

func TestTokenUsageStore_ListUsage(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	teamID := int64(1)
	board := "b1"
	err := s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s1", AgentName: "a1", TeamID: &teamID, BoardName: &board, TotalTokens: 100,
	})
	require.NoError(t, err)
	err = s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s2", AgentName: "a2", TotalTokens: 200,
	})
	require.NoError(t, err)

	// List all
	results, err := s.ListUsage(ctx, UsageFilter{})
	require.NoError(t, err)
	assert.Len(t, results, 2)

	// Filter by session
	results, err = s.ListUsage(ctx, UsageFilter{SessionID: "s1"})
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "s1", results[0].SessionID)

	// Filter by team
	results, err = s.ListUsage(ctx, UsageFilter{TeamID: &teamID})
	require.NoError(t, err)
	assert.Len(t, results, 1)

	// Filter by board
	results, err = s.ListUsage(ctx, UsageFilter{BoardName: "b1"})
	require.NoError(t, err)
	assert.Len(t, results, 1)
}

func TestTokenUsageStore_DefaultAgentType(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	err := s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s1", AgentName: "a1", TotalTokens: 100,
	})
	require.NoError(t, err)

	got, err := s.GetSessionUsage(ctx, "s1")
	require.NoError(t, err)
	assert.Equal(t, "claude", got.AgentType)
}

func TestTokenUsageStore_CacheTokens(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	err := s.RecordUsage(ctx, &TokenUsage{
		SessionID:        "s1",
		AgentName:        "a1",
		InputTokens:      1000,
		OutputTokens:     500,
		CacheReadTokens:  300,
		CacheWriteTokens: 100,
		TotalTokens:      1900,
		CostUSD:          0.04,
	})
	require.NoError(t, err)

	got, err := s.GetSessionUsage(ctx, "s1")
	require.NoError(t, err)
	assert.Equal(t, 300, got.CacheReadTokens)
	assert.Equal(t, 100, got.CacheWriteTokens)
	assert.Equal(t, 1900, got.TotalTokens)
}

func TestTokenUsageStore_GetLatestUsageBySessionIDs(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	// Record multiple snapshots for s1 and one for s2
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s1", AgentName: "a1", TotalTokens: 100, CostUSD: 0.01,
	}))
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s1", AgentName: "a1", TotalTokens: 500, CostUSD: 0.05,
	}))
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s2", AgentName: "a2", TotalTokens: 200, CostUSD: 0.02,
	}))

	result, err := s.GetLatestUsageBySessionIDs(ctx, []string{"s1", "s2", "s3"})
	require.NoError(t, err)
	assert.Len(t, result, 2)

	// s1 should have the sum of all per-turn records
	assert.Equal(t, 600, result["s1"].TotalTokens) // 100 + 500
	assert.InDelta(t, 0.06, result["s1"].CostUSD, 0.001)

	// s2 should have its only record
	assert.Equal(t, 200, result["s2"].TotalTokens)

	// s3 should not be present
	assert.Nil(t, result["s3"])
}

func TestTokenUsageStore_GetLatestUsageBySessionIDs_Empty(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	result, err := s.GetLatestUsageBySessionIDs(ctx, []string{})
	require.NoError(t, err)
	assert.Len(t, result, 0)
}

func TestTokenUsageStore_ListUsage_SinceFilter(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s-old", AgentName: "a1", TotalTokens: 100, RecordedAt: "2020-01-01T00:00:00Z",
	}))
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s-new", AgentName: "a2", TotalTokens: 500, RecordedAt: "2025-06-01T00:00:00Z",
	}))

	results, err := s.ListUsage(ctx, UsageFilter{Since: "2025-01-01T00:00:00Z"})
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "s-new", results[0].SessionID)
}

func TestTokenUsageStore_GetUsageSummary_SinceFilter(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s-old", AgentName: "a1", AgentType: "claude",
		TotalTokens: 100, CostUSD: 0.01, RecordedAt: "2020-01-01T00:00:00Z",
	}))
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s-new", AgentName: "a2", AgentType: "claude",
		TotalTokens: 500, CostUSD: 0.05, RecordedAt: "2025-06-01T00:00:00Z",
	}))

	summaries, err := s.GetUsageSummary(ctx, "2025-01-01T00:00:00Z")
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	assert.Equal(t, int64(500), summaries[0].TotalTokens)
	assert.Equal(t, 1, summaries[0].NumSessions)
}

func TestTokenUsageStore_RecordSetsID(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	u := &TokenUsage{SessionID: "s1", AgentName: "a1", TotalTokens: 100}
	require.NoError(t, s.RecordUsage(ctx, u))
	assert.Greater(t, u.ID, int64(0))
}

func TestTokenUsageStore_RecordDefaultsRecordedAt(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	u := &TokenUsage{SessionID: "s1", AgentName: "a1", TotalTokens: 100}
	require.NoError(t, s.RecordUsage(ctx, u))
	assert.NotEmpty(t, u.RecordedAt)
}

func TestTokenUsageStore_GetTeamUsage_CacheTokens(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	teamID := int64(1)
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s1", AgentName: "a1", TeamID: &teamID,
		InputTokens: 100, CacheReadTokens: 50, CacheWriteTokens: 20,
		TotalTokens: 170, CostUSD: 0.01,
	}))
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s2", AgentName: "a2", TeamID: &teamID,
		InputTokens: 200, CacheReadTokens: 30, CacheWriteTokens: 10,
		TotalTokens: 240, CostUSD: 0.02,
	}))

	summary, err := s.GetTeamUsage(ctx, teamID)
	require.NoError(t, err)
	assert.Equal(t, int64(80), summary.CacheReadTokens)
	assert.Equal(t, int64(30), summary.CacheWriteTokens)
}

func TestTokenUsageStore_GetBoardUsage_NoMatch(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	summary, err := s.GetBoardUsage(ctx, "nonexistent")
	require.NoError(t, err)
	assert.Equal(t, int64(0), summary.TotalTokens)
	assert.Equal(t, 0, summary.NumSessions)
}

func TestTokenUsageStore_ListUsage_MultipleFilters(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	teamID := int64(1)
	board := "eng"
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s1", AgentName: "a1", TeamID: &teamID, BoardName: &board, TotalTokens: 100,
	}))
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s2", AgentName: "a2", TeamID: &teamID, TotalTokens: 200,
	}))
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s3", AgentName: "a3", BoardName: &board, TotalTokens: 300,
	}))

	// Filter by both team AND board
	results, err := s.ListUsage(ctx, UsageFilter{TeamID: &teamID, BoardName: "eng"})
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "s1", results[0].SessionID)
}

func TestTokenUsageStore_GetLatestTurnContext_BasicCase(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	// Turn 1: small context
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID:        "s1",
		AgentName:        "a1",
		InputTokens:      5,
		CacheReadTokens:  50000,
		CacheWriteTokens: 2000,
		RecordedAt:       "2026-01-01T00:00:00Z",
	}))
	// Turn 2: larger context (latest)
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID:        "s1",
		AgentName:        "a1",
		InputTokens:      1,
		CacheReadTokens:  190000,
		CacheWriteTokens: 500,
		RecordedAt:       "2026-01-01T00:01:00Z",
	}))

	result, err := s.GetLatestTurnContextBySessionIDs(ctx, []string{"s1"})
	require.NoError(t, err)
	assert.Len(t, result, 1)
	// Should return latest turn's total: 1 + 190000 + 500 = 190501
	assert.Equal(t, 190501, result["s1"])
}

func TestTokenUsageStore_GetLatestTurnContext_MultipleSessions(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	// Session 1: two turns
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s1", AgentName: "a1",
		InputTokens: 10, CacheReadTokens: 100000, CacheWriteTokens: 5000,
		RecordedAt: "2026-01-01T00:00:00Z",
	}))
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s1", AgentName: "a1",
		InputTokens: 2, CacheReadTokens: 120000, CacheWriteTokens: 800,
		RecordedAt: "2026-01-01T00:01:00Z",
	}))

	// Session 2: one turn
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s2", AgentName: "a2",
		InputTokens: 3, CacheReadTokens: 40000, CacheWriteTokens: 200,
		RecordedAt: "2026-01-01T00:00:30Z",
	}))

	result, err := s.GetLatestTurnContextBySessionIDs(ctx, []string{"s1", "s2", "s3"})
	require.NoError(t, err)
	assert.Len(t, result, 2)

	// s1 latest: 2 + 120000 + 800 = 120802
	assert.Equal(t, 120802, result["s1"])
	// s2: 3 + 40000 + 200 = 40203
	assert.Equal(t, 40203, result["s2"])
	// s3 not present
	_, exists := result["s3"]
	assert.False(t, exists)
}

func TestTokenUsageStore_GetLatestTurnContext_Empty(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	result, err := s.GetLatestTurnContextBySessionIDs(ctx, []string{})
	require.NoError(t, err)
	assert.Len(t, result, 0)
}

func TestTokenUsageStore_GetLatestTurnContext_NoData(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	result, err := s.GetLatestTurnContextBySessionIDs(ctx, []string{"nonexistent"})
	require.NoError(t, err)
	assert.Len(t, result, 0)
}

func TestTokenUsageStore_GetLatestTurnContext_ZeroCacheTokens(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	// No caching — all tokens are fresh input
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s1", AgentName: "a1",
		InputTokens: 150000, CacheReadTokens: 0, CacheWriteTokens: 0,
		RecordedAt: "2026-01-01T00:00:00Z",
	}))

	result, err := s.GetLatestTurnContextBySessionIDs(ctx, []string{"s1"})
	require.NoError(t, err)
	assert.Equal(t, 150000, result["s1"])
}

func TestTokenUsageStore_GetUsageTimeSeries(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	// Insert records at different times
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s1", AgentName: "a1",
		InputTokens: 100, OutputTokens: 50, CostUSD: 0.01,
		RecordedAt: "2026-01-01T10:00:00Z",
	}))
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s1", AgentName: "a1",
		InputTokens: 200, OutputTokens: 100, CostUSD: 0.02,
		RecordedAt: "2026-01-01T11:00:00Z",
	}))
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s2", AgentName: "a2",
		InputTokens: 300, OutputTokens: 150, CostUSD: 0.03,
		RecordedAt: "2026-01-01T11:30:00Z",
	}))

	buckets, err := s.GetUsageTimeSeries(ctx, "", "1h")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(buckets), 2)

	// First bucket should have the 10:00 record
	assert.Equal(t, 0.01, buckets[0].CostUSD)
	// Second bucket should combine 11:00 and 11:30 records
	assert.InDelta(t, 0.05, buckets[1].CostUSD, 0.001)
}

func TestTokenUsageStore_GetUsageTimeSeries_Empty(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	buckets, err := s.GetUsageTimeSeries(ctx, "", "1h")
	require.NoError(t, err)
	assert.Len(t, buckets, 0)
}

func TestTokenUsageStore_GetUsageSummaryByBoard(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	board1 := "team-alpha"
	board2 := "team-beta"

	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s1", AgentName: "a1", BoardName: &board1,
		InputTokens: 100, OutputTokens: 50, CostUSD: 0.10,
		RecordedAt: "2026-01-01T10:00:00Z",
	}))
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s2", AgentName: "a2", BoardName: &board1,
		InputTokens: 200, OutputTokens: 100, CostUSD: 0.20,
		RecordedAt: "2026-01-01T10:00:00Z",
	}))
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s3", AgentName: "a3", BoardName: &board2,
		InputTokens: 50, OutputTokens: 25, CostUSD: 0.05,
		RecordedAt: "2026-01-01T10:00:00Z",
	}))

	teams, err := s.GetUsageSummaryByBoard(ctx, "")
	require.NoError(t, err)
	assert.Len(t, teams, 2)

	// Sorted by cost DESC, so team-alpha first
	assert.Equal(t, "team-alpha", teams[0].BoardName)
	assert.InDelta(t, 0.30, teams[0].CostUSD, 0.001)
	assert.Equal(t, 2, teams[0].NumAgents)

	assert.Equal(t, "team-beta", teams[1].BoardName)
	assert.InDelta(t, 0.05, teams[1].CostUSD, 0.001)
	assert.Equal(t, 1, teams[1].NumAgents)
}

func TestTokenUsageStore_GetUsageSummaryByBoard_WithSince(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	board := "team-x"
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s1", AgentName: "a1", BoardName: &board,
		InputTokens: 100, CostUSD: 0.10,
		RecordedAt: "2026-01-01T08:00:00Z",
	}))
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{
		SessionID: "s2", AgentName: "a2", BoardName: &board,
		InputTokens: 200, CostUSD: 0.20,
		RecordedAt: "2026-01-01T12:00:00Z",
	}))

	// Only records after 10:00
	teams, err := s.GetUsageSummaryByBoard(ctx, "2026-01-01T10:00:00Z")
	require.NoError(t, err)
	assert.Len(t, teams, 1)
	assert.InDelta(t, 0.20, teams[0].CostUSD, 0.001)
}

// ── model column ─────────────────────────────────────────────

// usageModel reads the raw stored value, distinguishing NULL from "".
func usageModel(t *testing.T, db *DB, sessionID, recordedAt string) (model string, isNull bool) {
	t.Helper()
	var v *string
	require.NoError(t, db.GetContext(context.Background(), &v,
		"SELECT model FROM token_usage WHERE session_id = ? AND recorded_at = ?", sessionID, recordedAt))
	if v == nil {
		return "", true
	}
	return *v, false
}

func TestTokenUsageStore_StoresModelPerRow(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	// One session, two models: a user can switch model mid-session, which is
	// why the model belongs on the row and not on the session.
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{SessionID: "sess-1", AgentName: "a", InputTokens: 10,
		RecordedAt: "2026-09-21T10:00:00Z", Model: "claude-opus-5"}))
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{SessionID: "sess-1", AgentName: "a", InputTokens: 10,
		RecordedAt: "2026-09-21T10:05:00Z", Model: "claude-fable-5-1"}))

	first, null := usageModel(t, db, "sess-1", "2026-09-21T10:00:00Z")
	assert.False(t, null)
	assert.Equal(t, "claude-opus-5", first)
	second, _ := usageModel(t, db, "sess-1", "2026-09-21T10:05:00Z")
	assert.Equal(t, "claude-fable-5-1", second)
}

// An unobserved model is NULL, never "". NULL says "unknown"; an empty string
// would look like a value and need special-casing in every later query.
func TestTokenUsageStore_UnknownModelIsNull(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{SessionID: "s", AgentName: "a", InputTokens: 1, RecordedAt: "2026-09-21T10:00:00Z"}))
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{SessionID: "s", AgentName: "a", InputTokens: 1, RecordedAt: "2026-09-21T10:01:00Z", Model: "   "}))

	for _, at := range []string{"2026-09-21T10:00:00Z", "2026-09-21T10:01:00Z"} {
		_, null := usageModel(t, db, "s", at)
		assert.True(t, null, "row at %s", at)
	}
}

func TestTokenUsageStore_ModelIsTrimmedButOtherwiseVerbatim(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	require.NoError(t, s.RecordUsage(context.Background(), &TokenUsage{SessionID: "s", AgentName: "a", InputTokens: 1,
		RecordedAt: "2026-09-21T10:00:00Z", Model: " claude-opus-5[1m] "}))
	got, _ := usageModel(t, db, "s", "2026-09-21T10:00:00Z")
	assert.Equal(t, "claude-opus-5[1m]", got, "stored as reported; normalising is the pricing lookup's job")
}

// Rows are insert-only: a re-poll of the same call must not rewrite the model.
func TestTokenUsageStore_DuplicateRowDoesNotOverwriteModel(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()
	at := "2026-09-21T10:00:00Z"
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{SessionID: "s", AgentName: "a", InputTokens: 1, RecordedAt: at, Model: "claude-opus-5"}))
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{SessionID: "s", AgentName: "a", InputTokens: 1, RecordedAt: at, Model: "something-else"}))
	got, _ := usageModel(t, db, "s", at)
	assert.Equal(t, "claude-opus-5", got)
}

// The existing session-level reads must keep working with the new column, and
// with sessions whose rows span models or predate the column.
func TestTokenUsageStore_ExistingReadsUnaffectedByModel(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{SessionID: "s", AgentName: "a", InputTokens: 100, CostUSD: 1, RecordedAt: "2026-09-21T10:00:00Z", Model: "claude-opus-5"}))
	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{SessionID: "s", AgentName: "a", InputTokens: 200, CostUSD: 2, RecordedAt: "2026-09-21T10:01:00Z"}))

	got, err := s.GetSessionUsage(ctx, "s")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 300, got.InputTokens)
	assert.InDelta(t, 3.0, got.CostUSD, 1e-9)

	list, err := s.ListUsage(ctx, UsageFilter{SessionID: "s"})
	require.NoError(t, err)
	assert.Len(t, list, 1)
}

// A database from before the column existed gains it on open. Its rows keep
// their data and read as NULL: their model was never recorded and is not guessed.
func TestTokenUsageSchema_ModelColumnIsMigratedIn(t *testing.T) {
	path := t.TempDir() + "/older.db"
	db, err := Open(path)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, NewTokenUsageStore(db).RecordUsage(ctx, &TokenUsage{SessionID: "old", AgentName: "a",
		InputTokens: 777, CostUSD: 4.5, RecordedAt: "2026-08-01T00:00:00Z"}))
	_, err = db.ExecContext(ctx, "ALTER TABLE token_usage DROP COLUMN model")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	db, err = Open(path)
	require.NoError(t, err)
	defer db.Close()
	s := NewTokenUsageStore(db)

	_, null := usageModel(t, db, "old", "2026-08-01T00:00:00Z")
	assert.True(t, null, "pre-existing row")
	old, err := s.GetSessionUsage(ctx, "old")
	require.NoError(t, err)
	assert.Equal(t, 777, old.InputTokens)
	assert.InDelta(t, 4.5, old.CostUSD, 1e-9)

	require.NoError(t, s.RecordUsage(ctx, &TokenUsage{SessionID: "new", AgentName: "a", InputTokens: 1,
		RecordedAt: "2026-09-21T00:00:00Z", Model: "gpt-5.6-sol"}))
	got, _ := usageModel(t, db, "new", "2026-09-21T00:00:00Z")
	assert.Equal(t, "gpt-5.6-sol", got)
}

func TestTokenUsageStore_GetUsageSummaryByAgent_NameFallback(t *testing.T) {
	db := openTestDB(t)
	s := NewTokenUsageStore(db)
	ctx := context.Background()

	// Unnamed solo agent: poller recorded an empty name.
	_, err := db.Exec(`INSERT INTO live_sessions (session_id, agent_type, agent_name, working_dir, created_at)
		VALUES ('unnamed', 'codex', 'coral-go', '/tmp/coral-go', '2026-01-01T00:00:00Z')`)
	require.NoError(t, err)
	// Renamed after the usage was recorded: the current display name wins.
	_, err = db.Exec(`INSERT INTO live_sessions (session_id, agent_type, agent_name, working_dir, created_at, display_name)
		VALUES ('renamed', 'claude', 'coral-go', '/tmp/coral-go', '2026-01-01T00:00:00Z', 'Debugger')`)
	require.NoError(t, err)

	for _, u := range []*TokenUsage{
		{SessionID: "unnamed", AgentType: "codex", InputTokens: 10},
		{SessionID: "renamed", AgentName: "Agent", AgentType: "claude", InputTokens: 10},
		{SessionID: "orphan", AgentName: "Old Name", AgentType: "claude", InputTokens: 10},
	} {
		require.NoError(t, s.RecordUsage(ctx, u))
	}

	rows, err := s.GetUsageSummaryByAgent(ctx, "")
	require.NoError(t, err)
	names := map[string]string{}
	for _, r := range rows {
		names[r.SessionID] = r.AgentName
	}
	assert.Equal(t, "coral-go", names["unnamed"])
	assert.Equal(t, "Debugger", names["renamed"])
	assert.Equal(t, "Old Name", names["orphan"])
}
