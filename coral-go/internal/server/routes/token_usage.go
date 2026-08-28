package routes

import (
	"net/http"
	"strconv"
	"time"

	"github.com/cdknorow/coral/internal/gitutil"
	"github.com/cdknorow/coral/internal/store"
	"github.com/go-chi/chi/v5"
)

// TokenUsageHandler provides HTTP handlers for token usage API.
type TokenUsageHandler struct {
	ts *store.TokenUsageStore
}

// NewTokenUsageHandler creates a new TokenUsageHandler.
func NewTokenUsageHandler(db *store.DB) *TokenUsageHandler {
	return &TokenUsageHandler{
		ts: store.NewTokenUsageStore(db),
	}
}

// Store returns the underlying TokenUsageStore.
func (h *TokenUsageHandler) Store() *store.TokenUsageStore {
	return h.ts
}

// ListUsage returns filtered token usage records.
// GET /api/token-usage
func (h *TokenUsageHandler) ListUsage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	var filter store.UsageFilter
	filter.SessionID = q.Get("session_id")
	filter.BoardName = q.Get("board_name")
	filter.Since = q.Get("since")

	if teamIDStr := q.Get("team_id"); teamIDStr != "" {
		id, err := strconv.ParseInt(teamIDStr, 10, 64)
		if err != nil {
			errBadRequest(w, "invalid team_id")
			return
		}
		filter.TeamID = &id
	}

	records, err := h.ts.ListUsage(r.Context(), filter)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}

	// Compute aggregated totals
	var totalInput, totalOutput, totalCacheRead, totalCacheWrite, totalTokens int64
	var totalCost float64
	var firstMessage, lastMessage time.Time
	for _, r := range records {
		totalInput += int64(r.InputTokens)
		totalOutput += int64(r.OutputTokens)
		totalCacheRead += int64(r.CacheReadTokens)
		totalCacheWrite += int64(r.CacheWriteTokens)
		totalTokens += int64(r.TotalTokens)
		totalCost += r.CostUSD
		if ts, err := time.Parse(time.RFC3339Nano, r.SessionStartAt); err == nil && (firstMessage.IsZero() || ts.Before(firstMessage)) {
			firstMessage = ts
		}
		if ts, err := time.Parse(time.RFC3339Nano, r.LastActivityAt); err == nil && (lastMessage.IsZero() || ts.After(lastMessage)) {
			lastMessage = ts
		}
	}
	var executionTimeSec int64
	if !firstMessage.IsZero() && !lastMessage.IsZero() && lastMessage.After(firstMessage) {
		executionTimeSec = int64(lastMessage.Sub(firstMessage).Seconds())
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"records": records,
		"totals": map[string]any{
			"input_tokens":       totalInput,
			"output_tokens":      totalOutput,
			"cache_read_tokens":  totalCacheRead,
			"cache_write_tokens": totalCacheWrite,
			"total_tokens":       totalTokens,
			"cost_usd":           totalCost,
			"num_sessions":       len(records),
			"first_message_at":   formatOptionalTime(firstMessage),
			"last_message_at":    formatOptionalTime(lastMessage),
			"execution_time_sec": executionTimeSec,
		},
	})
}

func formatOptionalTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// UsageSummary returns high-level aggregated usage.
// GET /api/token-usage/summary
func (h *TokenUsageHandler) UsageSummary(w http.ResponseWriter, r *http.Request) {
	since := r.URL.Query().Get("since")

	summaries, err := h.ts.GetUsageSummary(r.Context(), since)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}

	byAgent, err := h.ts.GetUsageSummaryByAgent(r.Context(), since)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}

	// Compute grand totals
	var totalInput, totalOutput, totalCacheRead, totalCacheWrite, totalTokens int64
	var totalCost float64
	var totalSessions int
	for _, s := range summaries {
		totalInput += s.InputTokens
		totalOutput += s.OutputTokens
		totalCacheRead += s.CacheReadTokens
		totalCacheWrite += s.CacheWriteTokens
		totalTokens += s.TotalTokens
		totalCost += s.CostUSD
		totalSessions += s.NumSessions
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"by_agent_type": summaries,
		"by_agent":      byAgent,
		"totals": map[string]any{
			"input_tokens":       totalInput,
			"output_tokens":      totalOutput,
			"cache_read_tokens":  totalCacheRead,
			"cache_write_tokens": totalCacheWrite,
			"total_tokens":       totalTokens,
			"cost_usd":           totalCost,
			"num_sessions":       totalSessions,
		},
	})
}

// SessionTurns returns per-turn cost data for a session.
// GET /api/token-usage/session/{sessionID}/turns
func (h *TokenUsageHandler) SessionTurns(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	if sessionID == "" {
		errBadRequest(w, "session_id required")
		return
	}

	turns, err := h.ts.GetSessionTurns(r.Context(), sessionID)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}

	// Compute cumulative cost
	type turnWithCumulative struct {
		Turn           int     `json:"turn"`
		CostUSD        float64 `json:"cost_usd"`
		CumulativeCost float64 `json:"cumulative_cost"`
		Timestamp      string  `json:"timestamp"`
	}

	result := make([]turnWithCumulative, len(turns))
	var cumulative float64
	for i, t := range turns {
		cumulative += t.CostUSD
		result[i] = turnWithCumulative{
			Turn:           i + 1,
			CostUSD:        t.CostUSD,
			CumulativeCost: cumulative,
			Timestamp:      t.RecordedAt,
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"turns": result})
}

// UsageTimeSeries returns bucketed time-series cost data.
// GET /api/token-usage/timeseries?since=...&interval=5m|1h|1d
func (h *TokenUsageHandler) UsageTimeSeries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since := q.Get("since")
	interval := q.Get("interval")
	if interval == "" {
		interval = "1h"
	}

	buckets, err := h.ts.GetUsageTimeSeries(r.Context(), since, interval)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}

	// Compute cumulative cost
	type bucketWithCumulative struct {
		Bucket           string  `json:"bucket"`
		CostUSD          float64 `json:"cost_usd"`
		CumulativeCost   float64 `json:"cumulative_cost"`
		InputTokens      int64   `json:"input_tokens"`
		OutputTokens     int64   `json:"output_tokens"`
		CacheReadTokens  int64   `json:"cache_read_tokens"`
		CacheWriteTokens int64   `json:"cache_write_tokens"`
		NumRequests      int     `json:"num_requests"`
	}

	result := make([]bucketWithCumulative, len(buckets))
	var cumulative float64
	for i, b := range buckets {
		cumulative += b.CostUSD
		result[i] = bucketWithCumulative{
			Bucket:           b.Bucket,
			CostUSD:          b.CostUSD,
			CumulativeCost:   cumulative,
			InputTokens:      b.InputTokens,
			OutputTokens:     b.OutputTokens,
			CacheReadTokens:  b.CacheReadTokens,
			CacheWriteTokens: b.CacheWriteTokens,
			NumRequests:      b.NumRequests,
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"buckets": result, "interval": interval})
}

// UsageSummaryByBranch returns per-branch usage breakdown.
// GET /api/token-usage/by-branch?since=...&branch=...
func (h *TokenUsageHandler) UsageSummaryByBranch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since := q.Get("since")
	branch := q.Get("branch")

	branches, err := h.ts.GetUsageSummaryByBranch(r.Context(), since, branch)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}

	for i := range branches {
		branches[i].RepoName = gitutil.ParseRepoName(branches[i].RemoteURL)
	}

	var totalInput, totalOutput, totalCacheRead, totalCacheWrite, totalTokens int64
	var totalCost float64
	var totalAgents int
	for _, b := range branches {
		totalInput += b.InputTokens
		totalOutput += b.OutputTokens
		totalCacheRead += b.CacheReadTokens
		totalCacheWrite += b.CacheWriteTokens
		totalTokens += b.TotalTokens
		totalCost += b.CostUSD
		totalAgents += b.NumAgents
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"branches": branches,
		"totals": map[string]any{
			"input_tokens":       totalInput,
			"output_tokens":      totalOutput,
			"cache_read_tokens":  totalCacheRead,
			"cache_write_tokens": totalCacheWrite,
			"total_tokens":       totalTokens,
			"cost_usd":           totalCost,
			"num_agents":         totalAgents,
		},
	})
}

// UsageSummaryByBoard returns per-board/team usage breakdown.
// GET /api/token-usage/by-team?since=...
func (h *TokenUsageHandler) UsageSummaryByBoard(w http.ResponseWriter, r *http.Request) {
	since := r.URL.Query().Get("since")

	teams, err := h.ts.GetUsageSummaryByBoard(r.Context(), since)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"teams": teams})
}
