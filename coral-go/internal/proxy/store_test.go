package proxy

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestGetRequestByID(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Create a request
	err := store.CreateRequest(ctx, "req-abc", "session-1", ProviderAnthropic, "claude-sonnet-4-20250514", false)
	require.NoError(t, err)

	// Complete it
	err = store.CompleteRequest(ctx, "req-abc", TokenUsage{InputTokens: 500, OutputTokens: 100}, CalculateCostBreakdown("claude-sonnet-4-20250514", TokenUsage{InputTokens: 500, OutputTokens: 100}), 200, "success", "")
	require.NoError(t, err)

	// Get by ID
	req, err := store.GetRequestByID(ctx, "req-abc")
	require.NoError(t, err)
	assert.Equal(t, "req-abc", req.RequestID)
	assert.Equal(t, "session-1", req.SessionID)
	assert.Equal(t, 500, req.InputTokens)
	assert.Equal(t, 100, req.OutputTokens)
	assert.Equal(t, "success", req.Status)

	// Not found
	_, err = store.GetRequestByID(ctx, "nonexistent")
	assert.Error(t, err)
}

func TestGetStatsByAgent(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Create requests for different sessions
	store.CreateRequest(ctx, "r1", "agent-1", ProviderAnthropic, "claude-sonnet-4-20250514", false)
	store.CompleteRequest(ctx, "r1", TokenUsage{InputTokens: 1000, CacheReadTokens: 200}, CalculateCostBreakdown("claude-sonnet-4-20250514", TokenUsage{InputTokens: 1000, CacheReadTokens: 200}), 200, "success", "")

	store.CreateRequest(ctx, "r2", "agent-1", ProviderAnthropic, "claude-sonnet-4-20250514", false)
	store.CompleteRequest(ctx, "r2", TokenUsage{InputTokens: 2000, CacheWriteTokens: 50}, CalculateCostBreakdown("claude-sonnet-4-20250514", TokenUsage{InputTokens: 2000, CacheWriteTokens: 50}), 200, "success", "")

	store.CreateRequest(ctx, "r3", "agent-2", ProviderOpenAI, "gpt-4o", false)
	store.CompleteRequest(ctx, "r3", TokenUsage{InputTokens: 500, OutputTokens: 25}, CalculateCostBreakdown("gpt-4o", TokenUsage{InputTokens: 500, OutputTokens: 25}), 200, "success", "")

	byAgent, err := store.GetStatsByAgent(ctx, "1970-01-01")
	require.NoError(t, err)
	require.Len(t, byAgent, 2)

	// Ordered by cost descending
	assert.Equal(t, "agent-1", byAgent[0].SessionID)
	assert.Equal(t, 2, byAgent[0].Requests)
	assert.Equal(t, 3000, byAgent[0].InputTokens)
	assert.Equal(t, 0, byAgent[0].OutputTokens)
	assert.Equal(t, 200, byAgent[0].CacheReadTokens)
	assert.Equal(t, 50, byAgent[0].CacheWriteTokens)
	assert.Greater(t, byAgent[0].CostUSD, 0.0)

	assert.Equal(t, "agent-2", byAgent[1].SessionID)
	assert.Equal(t, 1, byAgent[1].Requests)
	assert.Equal(t, 500, byAgent[1].InputTokens)
	assert.Equal(t, 25, byAgent[1].OutputTokens)
	assert.Equal(t, 0, byAgent[1].CacheReadTokens)
	assert.Equal(t, 0, byAgent[1].CacheWriteTokens)
}

func TestGetStatsByAgentEmpty(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	ctx := context.Background()

	byAgent, err := store.GetStatsByAgent(ctx, "1970-01-01")
	require.NoError(t, err)
	assert.NotNil(t, byAgent) // Empty slice, not nil
	assert.Len(t, byAgent, 0)
}

func TestGetStatsIncludesCacheBreakdown(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	ctx := context.Background()

	usage1 := TokenUsage{InputTokens: 1000, OutputTokens: 200, CacheReadTokens: 300}
	err := store.CreateRequest(ctx, "stats-r1", "session-1", ProviderAnthropic, "claude-sonnet-4-20250514", false)
	require.NoError(t, err)
	err = store.CompleteRequest(ctx, "stats-r1", usage1, CalculateCostBreakdown("claude-sonnet-4-20250514", usage1), 200, "success", "")
	require.NoError(t, err)

	usage2 := TokenUsage{InputTokens: 500, OutputTokens: 100, CacheWriteTokens: 50}
	err = store.CreateRequest(ctx, "stats-r2", "session-2", ProviderOpenAI, "gpt-4o", false)
	require.NoError(t, err)
	err = store.CompleteRequest(ctx, "stats-r2", usage2, CalculateCostBreakdown("gpt-4o", usage2), 200, "success", "")
	require.NoError(t, err)

	stats, byModel, err := store.GetStats(ctx, "1970-01-01", "")
	require.NoError(t, err)
	assert.Equal(t, 1500, stats.TotalInputTokens)
	assert.Equal(t, 300, stats.TotalOutputTokens)
	assert.Equal(t, 300, stats.TotalCacheReadTokens)
	assert.Equal(t, 50, stats.TotalCacheWriteTokens)
	require.Len(t, byModel, 2)

	foundAnthropic := false
	foundOpenAI := false
	for _, model := range byModel {
		switch model.Model {
		case "claude-sonnet-4-20250514":
			foundAnthropic = true
			assert.Equal(t, 1000, model.InputTokens)
			assert.Equal(t, 200, model.OutputTokens)
			assert.Equal(t, 300, model.CacheReadTokens)
			assert.Equal(t, 0, model.CacheWriteTokens)
		case "gpt-4o":
			foundOpenAI = true
			assert.Equal(t, 500, model.InputTokens)
			assert.Equal(t, 100, model.OutputTokens)
			assert.Equal(t, 0, model.CacheReadTokens)
			assert.Equal(t, 50, model.CacheWriteTokens)
		}
	}
	assert.True(t, foundAnthropic)
	assert.True(t, foundOpenAI)
}

func TestCompleteRequestStoresCostBreakdown(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	ctx := context.Background()

	err := store.CreateRequest(ctx, "req-breakdown", "session-1", ProviderAnthropic, "claude-sonnet-4-20250514", false)
	require.NoError(t, err)

	usage := TokenUsage{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 200, CacheWriteTokens: 100}
	breakdown := CalculateCostBreakdown("claude-sonnet-4-20250514", usage)
	err = store.CompleteRequest(ctx, "req-breakdown", usage, breakdown, 200, "success", "")
	require.NoError(t, err)

	req, err := store.GetRequestByID(ctx, "req-breakdown")
	require.NoError(t, err)
	assert.InDelta(t, breakdown.InputCostUSD, req.InputCostUSD, 0.000001)
	assert.InDelta(t, breakdown.OutputCostUSD, req.OutputCostUSD, 0.000001)
	assert.InDelta(t, breakdown.CacheReadCostUSD, req.CacheReadCostUSD, 0.000001)
	assert.InDelta(t, breakdown.CacheWriteCostUSD, req.CacheWriteCostUSD, 0.000001)
	assert.InDelta(t, breakdown.Pricing.InputPerMTok, req.PricingInputPerMTok, 0.000001)
	assert.InDelta(t, breakdown.TotalCostUSD, req.CostUSD, 0.000001)
}

func TestGetTaskRunCost(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	ctx := context.Background()

	db.MustExec(`CREATE TABLE scheduled_runs (id INTEGER PRIMARY KEY, session_id TEXT)`)
	db.MustExec(`INSERT INTO scheduled_runs (id, session_id) VALUES (42, 'task-session-1')`)

	err := store.CreateRequest(ctx, "task-r1", "task-session-1", ProviderAnthropic, "claude-sonnet-4-20250514", false)
	require.NoError(t, err)
	err = store.CompleteRequest(ctx, "task-r1", TokenUsage{InputTokens: 1000}, CalculateCostBreakdown("claude-sonnet-4-20250514", TokenUsage{InputTokens: 1000}), 200, "success", "")
	require.NoError(t, err)

	err = store.CreateRequest(ctx, "task-r2", "task-session-1", ProviderOpenAI, "gpt-4o", false)
	require.NoError(t, err)
	err = store.CompleteRequest(ctx, "task-r2", TokenUsage{InputTokens: 500, OutputTokens: 100}, CalculateCostBreakdown("gpt-4o", TokenUsage{InputTokens: 500, OutputTokens: 100}), 200, "success", "")
	require.NoError(t, err)

	cost, err := store.GetTaskRunCost(ctx, 42)
	require.NoError(t, err)
	assert.Equal(t, int64(42), cost.RunID)
	assert.Equal(t, 2, cost.TotalRequests)
	assert.Equal(t, 1500, cost.TotalInputTokens)
	assert.Equal(t, 100, cost.TotalOutputTokens)
	assert.Greater(t, cost.TotalCostUSD, 0.0)
}

func TestDisplayNamePersistsAfterSessionDeletion(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Insert a live session with a display_name
	db.MustExec(`INSERT INTO live_sessions (session_id, agent_name, display_name, created_at) VALUES ('sess-1', 'coral-go', 'My Cool Agent', '2025-01-01T00:00:00Z')`)

	// Create a proxy request — should capture display_name from live_sessions
	err := store.CreateRequest(ctx, "r1", "sess-1", ProviderAnthropic, "claude-sonnet-4-20250514", false)
	require.NoError(t, err)
	store.CompleteRequest(ctx, "r1", TokenUsage{InputTokens: 1000}, CalculateCostBreakdown("claude-sonnet-4-20250514", TokenUsage{InputTokens: 1000}), 200, "success", "")

	// Verify display_name is stored in proxy_requests
	req, err := store.GetRequestByID(ctx, "r1")
	require.NoError(t, err)
	require.NotNil(t, req.DisplayName)
	assert.Equal(t, "My Cool Agent", *req.DisplayName)

	// While live, is_live should be true
	byAgent, err := store.GetStatsByAgent(ctx, "1970-01-01")
	require.NoError(t, err)
	require.Len(t, byAgent, 1)
	assert.True(t, byAgent[0].IsLive)

	// Delete the live session (simulating agent termination)
	db.MustExec(`DELETE FROM live_sessions WHERE session_id = 'sess-1'`)

	// GetStatsByAgent should still return the correct display_name and is_live=false
	byAgent, err = store.GetStatsByAgent(ctx, "1970-01-01")
	require.NoError(t, err)
	require.Len(t, byAgent, 1)
	require.NotNil(t, byAgent[0].DisplayName)
	assert.Equal(t, "My Cool Agent", *byAgent[0].DisplayName)
	assert.False(t, byAgent[0].IsLive)
}

func TestDisplayNameFallsBackToSessionMeta(t *testing.T) {
	db := testDB(t)
	store := NewStore(db)
	ctx := context.Background()

	// Create proxy request WITHOUT a live session (simulating old data)
	_, err := db.Exec(`INSERT INTO proxy_requests
		(request_id, session_id, agent_name, provider, model_requested, model_used, is_streaming, started_at, status)
		VALUES ('r1', 'old-sess', 'coral-go', 'anthropic', 'claude-sonnet-4-20250514', 'claude-sonnet-4-20250514', 0, '2025-01-01T00:00:00Z', 'success')`)
	require.NoError(t, err)

	// Insert session_meta with display_name (this persists after termination)
	db.MustExec(`INSERT INTO session_meta (session_id, display_name, created_at, updated_at) VALUES ('old-sess', 'Old Agent Name', '2025-01-01', '2025-01-01')`)

	// GetStatsByAgent should fall back to session_meta display_name
	byAgent, err := store.GetStatsByAgent(ctx, "1970-01-01")
	require.NoError(t, err)
	require.Len(t, byAgent, 1)
	require.NotNil(t, byAgent[0].DisplayName)
	assert.Equal(t, "Old Agent Name", *byAgent[0].DisplayName)
	assert.False(t, byAgent[0].IsLive)
}

func TestPricingTableStableOrder(t *testing.T) {
	rows := PricingTable()
	require.NotEmpty(t, rows)
	raw, err := json.Marshal(rows)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"model":"claude-haiku-4-5"`)
}
