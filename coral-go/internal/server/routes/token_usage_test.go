package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

func setupTokenUsageTest(t *testing.T) (*store.TokenUsageStore, *TokenUsageHandler, *chi.Mux) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "token_test.db")
	db, err := store.Open(dbPath)
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
		SessionID: "s2", AgentName: "agent-b", AgentType: "gemini",
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
		SessionID: "s3", AgentName: "a3", AgentType: "gemini",
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
	db, err := store.Open(dbPath)
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

func TestTokenUsageHandler_StoreAccessor(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "accessor_test.db")
	db, err := store.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	h := NewTokenUsageHandler(db)
	assert.NotNil(t, h.Store())
}
