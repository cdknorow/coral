package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

func setupTokenUsageTest(t *testing.T) (*store.TokenUsageStore, *TokenUsageHandler, *chi.Mux) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "token_test.db")
	db, err := storetest.Open(t, dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	ts := store.NewTokenUsageStore(db)
	h := NewTokenUsageHandler(db)
	r := chi.NewRouter()
	r.Get("/api/token-usage", h.ListUsage)
	r.Get("/api/token-usage/summary", h.UsageSummary)
	return ts, h, r
}

func TestListUsage_Empty(t *testing.T) {
	_, _, router := setupTokenUsageTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/token-usage", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	// records may be null/nil when empty
	if records, ok := body["records"].([]any); ok {
		assert.Len(t, records, 0)
	} else {
		assert.Nil(t, body["records"])
	}

	totals := body["totals"].(map[string]any)
	assert.Equal(t, float64(0), totals["total_tokens"])
	assert.Equal(t, float64(0), totals["cost_usd"])
	assert.Equal(t, float64(0), totals["num_sessions"])
}

func TestListUsage_WithRecords(t *testing.T) {
	ts, _, router := setupTokenUsageTest(t)
	ctx := t.Context()

	teamID := int64(5)
	board := "eng-team"
	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: "s1", AgentName: "agent-a", AgentType: "claude",
		TeamID: &teamID, BoardName: &board,
		InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 100, CacheWriteTokens: 50,
		TotalTokens: 1650, CostUSD: 0.05, NumTurns: 3,
	}))
	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: "s2", AgentName: "agent-b", AgentType: "agy",
		InputTokens: 2000, OutputTokens: 1000, TotalTokens: 3000, CostUSD: 0.02,
	}))

	// Unfiltered
	req := httptest.NewRequest(http.MethodGet, "/api/token-usage", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	records := body["records"].([]any)
	assert.Len(t, records, 2)

	totals := body["totals"].(map[string]any)
	assert.Equal(t, float64(3000), totals["input_tokens"])
	assert.Equal(t, float64(1500), totals["output_tokens"])
	assert.Equal(t, float64(4650), totals["total_tokens"])
	assert.InDelta(t, 0.07, totals["cost_usd"].(float64), 0.001)
	assert.Equal(t, float64(2), totals["num_sessions"])
}

func TestListUsage_ExecutionTimeUsesFirstAndLastMessage(t *testing.T) {
	ts, _, router := setupTokenUsageTest(t)
	ctx := t.Context()
	board := "timed-team"

	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: "s1", AgentName: "agent-a", BoardName: &board, TotalTokens: 10,
		SessionStartAt: "2026-08-27T10:00:00Z", LastActivityAt: "2026-08-27T10:05:00Z",
	}))
	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: "s2", AgentName: "agent-b", BoardName: &board, TotalTokens: 10,
		SessionStartAt: "2026-08-27T10:02:00Z", LastActivityAt: "2026-08-27T10:12:30Z",
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/token-usage?board_name=timed-team", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	totals := body["totals"].(map[string]any)
	assert.Equal(t, float64(750), totals["execution_time_sec"])
	assert.Equal(t, "2026-08-27T10:00:00Z", totals["first_message_at"])
	assert.Equal(t, "2026-08-27T10:12:30Z", totals["last_message_at"])

	records := body["records"].([]any)
	durations := map[string]float64{}
	for _, raw := range records {
		record := raw.(map[string]any)
		durations[record["session_id"].(string)] = record["execution_time_sec"].(float64)
	}
	assert.Equal(t, float64(300), durations["s1"])
	assert.Equal(t, float64(630), durations["s2"])
}

func TestListUsage_FilterBySessionID(t *testing.T) {
	ts, _, router := setupTokenUsageTest(t)
	ctx := t.Context()

	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: "s1", AgentName: "a1", TotalTokens: 100, CostUSD: 0.01,
	}))
	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: "s2", AgentName: "a2", TotalTokens: 200, CostUSD: 0.02,
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/token-usage?session_id=s1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	records := body["records"].([]any)
	assert.Len(t, records, 1)
	assert.Equal(t, "s1", records[0].(map[string]any)["session_id"])
}

func TestListUsage_FilterByTeamID(t *testing.T) {
	ts, _, router := setupTokenUsageTest(t)
	ctx := t.Context()

	teamID := int64(42)
	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: "s1", AgentName: "a1", TeamID: &teamID, TotalTokens: 100,
	}))
	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: "s2", AgentName: "a2", TotalTokens: 200,
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/token-usage?team_id=42", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	records := body["records"].([]any)
	assert.Len(t, records, 1)
}

func TestListUsage_FilterByBoardName(t *testing.T) {
	ts, _, router := setupTokenUsageTest(t)
	ctx := t.Context()

	board := "my-board"
	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: "s1", AgentName: "a1", BoardName: &board, TotalTokens: 100,
	}))
	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: "s2", AgentName: "a2", TotalTokens: 200,
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/token-usage?board_name=my-board", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	records := body["records"].([]any)
	assert.Len(t, records, 1)
}

func TestListUsage_InvalidTeamID(t *testing.T) {
	_, _, router := setupTokenUsageTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/token-usage?team_id=notanumber", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestListUsage_SumsPerTurnDeltas(t *testing.T) {
	ts, _, router := setupTokenUsageTest(t)
	ctx := t.Context()

	// Record two per-turn delta records for the same session — should be summed
	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: "s1", AgentName: "a1", InputTokens: 100, TotalTokens: 100, CostUSD: 0.01,
	}))
	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: "s1", AgentName: "a1", InputTokens: 500, TotalTokens: 500, CostUSD: 0.05,
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/token-usage", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	records := body["records"].([]any)
	// Per-turn deltas summed per session — one aggregated row
	assert.Len(t, records, 1)
	assert.Equal(t, float64(600), records[0].(map[string]any)["total_tokens"])

	totals := body["totals"].(map[string]any)
	assert.Equal(t, float64(600), totals["total_tokens"])
	assert.InDelta(t, 0.06, totals["cost_usd"].(float64), 0.001)
}

func TestUsageSummary_Empty(t *testing.T) {
	_, _, router := setupTokenUsageTest(t)

	req := httptest.NewRequest(http.MethodGet, "/api/token-usage/summary", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))

	if byType, ok := body["by_agent_type"].([]any); ok {
		assert.Len(t, byType, 0)
	} else {
		assert.Nil(t, body["by_agent_type"])
	}

	totals := body["totals"].(map[string]any)
	assert.Equal(t, float64(0), totals["total_tokens"])
}

func TestUsageSummary_GroupsByAgentType(t *testing.T) {
	ts, _, router := setupTokenUsageTest(t)
	ctx := t.Context()

	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: "s1", AgentName: "a1", AgentType: "claude",
		InputTokens: 1000, OutputTokens: 500, TotalTokens: 1500, CostUSD: 0.05,
	}))
	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: "s2", AgentName: "a2", AgentType: "claude",
		InputTokens: 2000, OutputTokens: 800, TotalTokens: 2800, CostUSD: 0.08,
	}))
	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: "s3", AgentName: "a3", AgentType: "agy",
		InputTokens: 500, OutputTokens: 200, TotalTokens: 700, CostUSD: 0.01,
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/token-usage/summary", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))

	byType := body["by_agent_type"].([]any)
	assert.Len(t, byType, 2)

	totals := body["totals"].(map[string]any)
	assert.Equal(t, float64(3500), totals["input_tokens"])
	assert.Equal(t, float64(1500), totals["output_tokens"])
	assert.Equal(t, float64(5000), totals["total_tokens"])
	assert.InDelta(t, 0.14, totals["cost_usd"].(float64), 0.001)
	assert.Equal(t, float64(3), totals["num_sessions"])
}

func TestUsageSummary_SinceFilter(t *testing.T) {
	ts, _, router := setupTokenUsageTest(t)
	ctx := t.Context()

	// Old record
	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: "s-old", AgentName: "a1", AgentType: "claude",
		TotalTokens: 1000, CostUSD: 0.05, RecordedAt: "2020-01-01T00:00:00Z",
	}))
	// Recent record
	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: "s-new", AgentName: "a2", AgentType: "claude",
		TotalTokens: 2000, CostUSD: 0.10, RecordedAt: "2025-06-01T00:00:00Z",
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/token-usage/summary?since=2025-01-01T00:00:00Z", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))

	totals := body["totals"].(map[string]any)
	assert.Equal(t, float64(2000), totals["total_tokens"])
	assert.Equal(t, float64(1), totals["num_sessions"])
}

func TestUsageSummary_SumsPerTurnDeltas(t *testing.T) {
	ts, _, router := setupTokenUsageTest(t)
	ctx := t.Context()

	// Two per-turn delta records for the same session — should be summed
	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: "s1", AgentName: "a1", AgentType: "claude",
		TotalTokens: 100, CostUSD: 0.01,
	}))
	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: "s1", AgentName: "a1", AgentType: "claude",
		TotalTokens: 500, CostUSD: 0.05,
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/token-usage/summary", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))

	totals := body["totals"].(map[string]any)
	// Records are per-turn deltas — both should be summed
	assert.Equal(t, float64(600), totals["total_tokens"])
	assert.Equal(t, float64(1), totals["num_sessions"])
}

func setupTokenUsageTestWithGit(t *testing.T) (*store.TokenUsageStore, *store.GitStore, *TokenUsageHandler, *chi.Mux) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "token_branch_test.db")
	db, err := storetest.Open(t, dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	ts := store.NewTokenUsageStore(db)
	gs := store.NewGitStore(db)
	h := NewTokenUsageHandler(db)
	r := chi.NewRouter()
	r.Get("/api/token-usage/by-branch", h.UsageSummaryByBranch)
	return ts, gs, h, r
}

func TestUsageSummaryByBranch_Empty(t *testing.T) {
	_, _, _, router := setupTokenUsageTestWithGit(t)

	req := httptest.NewRequest(http.MethodGet, "/api/token-usage/by-branch", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))

	if branches, ok := body["branches"].([]any); ok {
		assert.Len(t, branches, 0)
	} else {
		assert.Nil(t, body["branches"])
	}

	totals := body["totals"].(map[string]any)
	assert.Equal(t, float64(0), totals["total_tokens"])
	assert.Equal(t, float64(0), totals["cost_usd"])
}

func TestUsageSummaryByBranch_GroupsByBranch(t *testing.T) {
	ts, gs, _, router := setupTokenUsageTestWithGit(t)
	ctx := t.Context()

	sid1 := "sess-feat-a"
	sid2 := "sess-feat-b"
	sid3 := "sess-main"

	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: sid1, AgentName: "dev1", AgentType: "claude",
		InputTokens: 1000, OutputTokens: 500, TotalTokens: 1500, CostUSD: 0.05,
	}))
	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: sid2, AgentName: "dev2", AgentType: "claude",
		InputTokens: 2000, OutputTokens: 800, TotalTokens: 2800, CostUSD: 0.08,
	}))
	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: sid3, AgentName: "dev3", AgentType: "claude",
		InputTokens: 500, OutputTokens: 200, TotalTokens: 700, CostUSD: 0.01,
	}))

	require.NoError(t, gs.UpsertGitSnapshot(ctx, &store.GitSnapshot{
		AgentName: "dev1", AgentType: "claude", WorkingDirectory: "/repo",
		Branch: "feature-a", CommitHash: "aaa111", SessionID: &sid1,
	}))
	require.NoError(t, gs.UpsertGitSnapshot(ctx, &store.GitSnapshot{
		AgentName: "dev2", AgentType: "claude", WorkingDirectory: "/repo",
		Branch: "feature-a", CommitHash: "bbb222", SessionID: &sid2,
	}))
	require.NoError(t, gs.UpsertGitSnapshot(ctx, &store.GitSnapshot{
		AgentName: "dev3", AgentType: "claude", WorkingDirectory: "/repo",
		Branch: "main", CommitHash: "ccc333", SessionID: &sid3,
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/token-usage/by-branch", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))

	branches := body["branches"].([]any)
	assert.Len(t, branches, 2)

	// Ordered by cost DESC — feature-a first
	featureA := branches[0].(map[string]any)
	assert.Equal(t, "feature-a", featureA["branch"])
	assert.Equal(t, float64(4300), featureA["total_tokens"])
	assert.InDelta(t, 0.13, featureA["cost_usd"].(float64), 0.001)
	assert.Equal(t, float64(2), featureA["num_agents"])

	main := branches[1].(map[string]any)
	assert.Equal(t, "main", main["branch"])
	assert.Equal(t, float64(700), main["total_tokens"])

	totals := body["totals"].(map[string]any)
	assert.Equal(t, float64(5000), totals["total_tokens"])
	assert.Equal(t, float64(3), totals["num_agents"])
}

func TestUsageSummaryByBranch_FilterByBranch(t *testing.T) {
	ts, gs, _, router := setupTokenUsageTestWithGit(t)
	ctx := t.Context()

	sid1 := "sess-1"
	sid2 := "sess-2"

	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: sid1, AgentName: "dev1", TotalTokens: 1000, CostUSD: 0.05,
	}))
	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: sid2, AgentName: "dev2", TotalTokens: 2000, CostUSD: 0.10,
	}))

	require.NoError(t, gs.UpsertGitSnapshot(ctx, &store.GitSnapshot{
		AgentName: "dev1", AgentType: "claude", WorkingDirectory: "/repo",
		Branch: "feature-x", CommitHash: "aaa", SessionID: &sid1,
	}))
	require.NoError(t, gs.UpsertGitSnapshot(ctx, &store.GitSnapshot{
		AgentName: "dev2", AgentType: "claude", WorkingDirectory: "/repo",
		Branch: "feature-y", CommitHash: "bbb", SessionID: &sid2,
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/token-usage/by-branch?branch=feature-x", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))

	branches := body["branches"].([]any)
	assert.Len(t, branches, 1)
	assert.Equal(t, "feature-x", branches[0].(map[string]any)["branch"])
	assert.Equal(t, float64(1000), branches[0].(map[string]any)["total_tokens"])
}

func TestUsageSummaryByBranch_UsesLatestBranch(t *testing.T) {
	ts, gs, _, router := setupTokenUsageTestWithGit(t)
	ctx := t.Context()

	sid := "sess-switched"
	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: sid, AgentName: "dev1", TotalTokens: 1000, CostUSD: 0.05,
	}))

	// Agent started on feature-old, then switched to feature-new
	require.NoError(t, gs.UpsertGitSnapshot(ctx, &store.GitSnapshot{
		AgentName: "dev1", AgentType: "claude", WorkingDirectory: "/repo",
		Branch: "feature-old", CommitHash: "old111", SessionID: &sid,
	}))
	require.NoError(t, gs.UpsertGitSnapshot(ctx, &store.GitSnapshot{
		AgentName: "dev1", AgentType: "claude", WorkingDirectory: "/repo",
		Branch: "feature-new", CommitHash: "new222", SessionID: &sid,
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/token-usage/by-branch", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))

	branches := body["branches"].([]any)
	assert.Len(t, branches, 1)
	assert.Equal(t, "feature-new", branches[0].(map[string]any)["branch"])
}

func TestUsageSummaryByBranch_IgnoresSessionsWithoutGit(t *testing.T) {
	ts, gs, _, router := setupTokenUsageTestWithGit(t)
	ctx := t.Context()

	sid1 := "sess-with-git"
	sid2 := "sess-no-git"

	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: sid1, AgentName: "dev1", TotalTokens: 1000, CostUSD: 0.05,
	}))
	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: sid2, AgentName: "dev2", TotalTokens: 2000, CostUSD: 0.10,
	}))

	require.NoError(t, gs.UpsertGitSnapshot(ctx, &store.GitSnapshot{
		AgentName: "dev1", AgentType: "claude", WorkingDirectory: "/repo",
		Branch: "main", CommitHash: "aaa", SessionID: &sid1,
	}))
	// No git snapshot for sid2

	req := httptest.NewRequest(http.MethodGet, "/api/token-usage/by-branch", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))

	branches := body["branches"].([]any)
	assert.Len(t, branches, 1)

	totals := body["totals"].(map[string]any)
	assert.Equal(t, float64(1000), totals["total_tokens"])
	assert.Equal(t, float64(1), totals["num_agents"])
}

// #4588: the summary carries by_model, one row per agent type and model id; an unrecorded
// model is "" and the list is [] (never null) when there is no usage.
func TestUsageSummary_ByModel(t *testing.T) {
	ts, _, router := setupTokenUsageTest(t)
	ctx := context.Background()
	for i, m := range []string{"claude-opus-5-5", "claude-sonnet-5-5", ""} {
		require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
			SessionID: "s1", AgentName: "a1", AgentType: "claude", Model: m,
			RecordedAt:  fmt.Sprintf("2026-10-01T10:0%d:00Z", i),
			InputTokens: 100 * (i + 1), OutputTokens: 10, CacheReadTokens: 5, CacheWriteTokens: 6, TotalTokens: 110, CostUSD: 0.1,
		}))
	}
	require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{
		SessionID: "s2", AgentName: "a2", AgentType: "codex", Model: "gpt-5-codex",
		RecordedAt: "2026-10-01T11:00:00Z", InputTokens: 7, TotalTokens: 7, CostUSD: 0.2,
	}))

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/token-usage/summary", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		ByModel []map[string]any `json:"by_model"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	got := map[string]map[string]any{}
	for _, r := range body.ByModel {
		got[r["agent_type"].(string)+"|"+r["model"].(string)] = r
	}
	require.Len(t, got, 4, "%v", body.ByModel)
	assert.EqualValues(t, 100, got["claude|claude-opus-5-5"]["input_tokens"])
	assert.EqualValues(t, 5, got["claude|claude-opus-5-5"]["cache_read_tokens"])
	assert.EqualValues(t, 6, got["claude|claude-opus-5-5"]["cache_write_tokens"])
	assert.EqualValues(t, 1, got["claude|claude-opus-5-5"]["num_sessions"])
	assert.EqualValues(t, 300, got["claude|"]["input_tokens"], "the unknown bucket")
	assert.EqualValues(t, 7, got["codex|gpt-5-codex"]["input_tokens"])

	empty := httptest.NewRecorder()
	_, _, r2 := setupTokenUsageTest(t)
	r2.ServeHTTP(empty, httptest.NewRequest(http.MethodGet, "/api/token-usage/summary", nil))
	assert.Contains(t, empty.Body.String(), `"by_model":[]`)
}

// #4969: the summary has by_thinking: the levels the transcripts recorded, and for each agent type a "" row (shown as "Not reported")
// for the rest of its usage. It is never filled with the level an agent was launched with.
func TestUsageSummary_ByThinkingHasANotReportedRemainder(t *testing.T) {
	ts, _, router := setupTokenUsageTest(t)
	ctx := t.Context()
	rec := func(sid, typ, level, at string, in, out, think int, cost float64) {
		require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{SessionID: sid, AgentName: sid, AgentType: typ, Thinking: level, ThinkingTokens: think, Source: "jsonl",
			RecordedAt: at, InputTokens: in, OutputTokens: out, TotalTokens: in + out, CostUSD: cost}))
	}
	rec("s1", "claude", "high", "2026-10-19T10:00:00Z", 1000, 100, 40, 1.0)
	rec("s1", "claude", "low", "2026-10-19T10:10:00Z", 500, 50, 0, 0.5)
	rec("s1", "claude", "", "2026-10-19T10:20:00Z", 200, 20, 0, 0.25) // no level recorded
	rec("s2", "agy", "", "2026-10-19T10:30:00Z", 300, 30, 0, 0.1)     // a type that records none

	req := httptest.NewRequest(http.MethodGet, "/api/token-usage/summary", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		ByThinking []store.ThinkingUsageSummary `json:"by_thinking"`
		Totals     map[string]any               `json:"totals"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	got := map[string]store.ThinkingUsageSummary{}
	var tokens int64
	var cost float64
	for _, r := range body.ByThinking {
		got[r.AgentType+"|"+r.Thinking] = r
		tokens += r.TotalTokens
		cost += r.CostUSD
	}
	require.Len(t, got, 4, "%+v", body.ByThinking)
	assert.EqualValues(t, 1000, got["claude|high"].InputTokens)
	assert.EqualValues(t, 40, got["claude|high"].ThinkingTokens)
	assert.EqualValues(t, 500, got["claude|low"].InputTokens)
	assert.EqualValues(t, 200, got["claude|"].InputTokens, "the claude call with no level is Not reported")
	assert.InDelta(t, 0.25, got["claude|"].CostUSD, 1e-9)
	assert.EqualValues(t, 300, got["agy|"].InputTokens, "a type with no level at all is all Not reported")
	assert.Equal(t, body.Totals["total_tokens"], float64(tokens), "the breakdown adds up to the totals")
	assert.InDelta(t, body.Totals["cost_usd"], cost, 1e-9)
}

func TestWithNotReportedNeverGoesNegative(t *testing.T) {
	// A range that starts inside an hour tracks a little more (the breakdown is by hour) than the totals count.
	tracked := []store.ThinkingUsageSummary{{AgentType: "claude", Model: "m1", Thinking: "high", InputTokens: 100, TotalTokens: 110, CostUSD: 1}}
	byModel := []store.ModelUsageSummary{{AgentType: "claude", Model: "m1", InputTokens: 90, TotalTokens: 99, CostUSD: 0.9}}
	out := withNotReported(tracked, byModel)
	require.Len(t, out, 1, "nothing left over: no Not reported row")
	assert.Equal(t, "high", out[0].Thinking)
}

// A remainder that has something left in one figure and nothing in another never shows a negative one.
func TestWithNotReportedEveryFigureOfARemainderIsNonNegative(t *testing.T) {
	tracked := []store.ThinkingUsageSummary{{AgentType: "claude", Model: "m1", Thinking: "high", InputTokens: 100, OutputTokens: 0, TotalTokens: 50, CostUSD: 0.2}}
	byModel := []store.ModelUsageSummary{{AgentType: "claude", Model: "m1", InputTokens: 90, OutputTokens: 40, TotalTokens: 99, CostUSD: 0.9}}
	out := withNotReported(tracked, byModel)
	require.Len(t, out, 2)
	for _, r := range out {
		assert.GreaterOrEqual(t, r.InputTokens, int64(0))
		assert.GreaterOrEqual(t, r.OutputTokens, int64(0))
		assert.GreaterOrEqual(t, r.CacheReadTokens, int64(0))
		assert.GreaterOrEqual(t, r.CacheWriteTokens, int64(0))
		assert.GreaterOrEqual(t, r.TotalTokens, int64(0))
		assert.GreaterOrEqual(t, r.CostUSD, 0.0)
	}
}

// #5120: the remainder is per provider and model: each model's total less what is tracked for it, and every (provider, model) adds up to its
// By Model row, in tokens and in cost.
func TestUsageSummary_ByThinkingIsByProviderAndModel(t *testing.T) {
	ts, _, router := setupTokenUsageTest(t)
	ctx := t.Context()
	rec := func(sid, typ, model, level, source, at string, in, out, think int, cost float64) {
		require.NoError(t, ts.RecordUsage(ctx, &store.TokenUsage{SessionID: sid, AgentName: sid, AgentType: typ, Model: model, Thinking: level, ThinkingTokens: think, Source: source,
			RecordedAt: at, InputTokens: in, OutputTokens: out, TotalTokens: in + out, CostUSD: cost}))
	}
	rec("s1", "claude", "claude-opus-5-5", "high", "jsonl", "2026-10-19T10:00:00Z", 1000, 100, 40, 1.0)
	rec("s1", "claude", "claude-opus-5-5", "high", "jsonl", "2026-10-19T10:05:00Z", 400, 40, 10, 0.4)
	rec("s1", "claude", "claude-opus-5-5", "low", "jsonl", "2026-10-19T10:10:00Z", 500, 50, 0, 0.5)
	rec("s1", "claude", "claude-opus-5-5", "", "jsonl", "2026-10-19T10:20:00Z", 200, 20, 0, 0.25) // no level: Not reported for opus
	rec("s2", "claude", "claude-sonnet-5-5", "medium", "jsonl", "2026-10-19T10:25:00Z", 300, 30, 0, 0.3)
	rec("s3", "claude", "claude-haiku-5-5", "", "jsonl", "2026-10-19T10:26:00Z", 60, 6, 0, 0.06) // a model with no level at all
	rec("s4", "codex", "gpt-5-codex", "medium", "jsonl", "2026-10-19T10:30:00Z", 70, 7, 3, 0.07)
	rec("s5", "claude", "claude-opus-5-5", "high", "proxy", "2026-10-19T10:40:00Z", 9000, 90, 0, 9.0) // a proxy row: no level line, in the totals as Not reported

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/token-usage/summary", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		ByThinking []store.ThinkingUsageSummary `json:"by_thinking"`
		ByModel    []store.ModelUsageSummary    `json:"by_model"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	got := map[string]store.ThinkingUsageSummary{}
	for _, r := range body.ByThinking {
		got[r.AgentType+"|"+r.Model+"|"+r.Thinking] = r
		assert.GreaterOrEqual(t, r.InputTokens, int64(0))
		assert.GreaterOrEqual(t, r.CostUSD, 0.0)
	}
	assert.EqualValues(t, 1400, got["claude|claude-opus-5-5|high"].InputTokens, "two calls of one model and level")
	assert.EqualValues(t, 50, got["claude|claude-opus-5-5|high"].ThinkingTokens)
	assert.EqualValues(t, 2, got["claude|claude-opus-5-5|high"].NumCalls)
	assert.EqualValues(t, 500, got["claude|claude-opus-5-5|low"].InputTokens)
	assert.EqualValues(t, 300, got["claude|claude-sonnet-5-5|medium"].InputTokens)
	assert.EqualValues(t, 70, got["codex|gpt-5-codex|medium"].InputTokens)
	assert.EqualValues(t, 9200, got["claude|claude-opus-5-5|"].InputTokens, "opus's remainder: the call with no level and the proxy row")
	assert.EqualValues(t, 60, got["claude|claude-haiku-5-5|"].InputTokens, "a model with no level at all is all Not reported")
	assert.NotContains(t, got, "claude|claude-sonnet-5-5|", "nothing left over for sonnet")
	assert.NotContains(t, got, "codex|gpt-5-codex|")
	// every (provider, model) adds up to its By Model row, and the thinking tokens are in no total
	type k struct{ typ, model string }
	in := map[k]int64{}
	cost := map[k]float64{}
	for _, r := range body.ByThinking {
		in[k{r.AgentType, r.Model}] += r.InputTokens
		cost[k{r.AgentType, r.Model}] += r.CostUSD
	}
	require.NotEmpty(t, body.ByModel)
	for _, m := range body.ByModel {
		assert.Equal(t, m.InputTokens, in[k{m.AgentType, m.Model}], "%s %s", m.AgentType, m.Model)
		assert.InDelta(t, m.CostUSD, cost[k{m.AgentType, m.Model}], 1e-9, "%s %s", m.AgentType, m.Model)
	}
}

// Rows kept from before the model was in the key have no model: they are shown as they are and taken out of the type's remainders, so a type's
// rows still add up to its totals and nothing is counted twice or attributed to a model.
func TestWithNotReportedTakesModelLessRowsOutOfTheRemainders(t *testing.T) {
	tracked := []store.ThinkingUsageSummary{
		{AgentType: "claude", Model: "", Thinking: "high", InputTokens: 300, OutputTokens: 30, TotalTokens: 330, CostUSD: 3},
		{AgentType: "claude", Model: "opus", Thinking: "low", InputTokens: 100, OutputTokens: 10, TotalTokens: 110, CostUSD: 1},
	}
	byModel := []store.ModelUsageSummary{
		{AgentType: "claude", Model: "opus", InputTokens: 600, OutputTokens: 60, TotalTokens: 660, CostUSD: 6},   // 100 tracked, 500 left
		{AgentType: "claude", Model: "sonnet", InputTokens: 200, OutputTokens: 20, TotalTokens: 220, CostUSD: 2}, // none tracked, 200 left
		{AgentType: "claude", Model: "", InputTokens: 50, OutputTokens: 5, TotalTokens: 55, CostUSD: 0.5},
	}
	out := withNotReported(tracked, byModel)
	var in int64
	var cost float64
	for _, r := range out {
		in += r.InputTokens
		cost += r.CostUSD
		assert.GreaterOrEqual(t, r.InputTokens, int64(0))
		assert.GreaterOrEqual(t, r.CostUSD, 0.0)
	}
	assert.EqualValues(t, 850, in, "the type's rows add up to its total (600+200+50), the model-less tracked 300 counted once")
	assert.InDelta(t, 8.5, cost, 1e-9)
	var kept bool
	for _, r := range out {
		if r.Model == "" && r.Thinking == "high" {
			kept = true
			assert.EqualValues(t, 300, r.InputTokens, "the model-less tracked row is shown as it is")
		}
	}
	assert.True(t, kept)
}
