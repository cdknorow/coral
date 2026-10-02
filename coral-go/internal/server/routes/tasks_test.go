package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/proxy"
	"github.com/cdknorow/coral/internal/store"
)

func TestTaskStatusIncludesProxyCosts(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/tasks_test.db")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	cfg := &config.Config{}
	h := NewTasksHandler(db, cfg)
	ps := proxy.NewStore(db.DB)
	h.SetProxyStore(ps)

	ctx := t.Context()
	runID, err := h.sched.CreateOneshotRun(ctx, "2025-03-11T10:00:00Z", nil, nil)
	require.NoError(t, err)
	sessionID := "task-session-123"
	err = h.sched.UpdateScheduledRun(ctx, runID, map[string]interface{}{"session_id": sessionID})
	require.NoError(t, err)

	usage := proxy.TokenUsage{InputTokens: 1000, OutputTokens: 250}
	breakdown := proxy.CalculateCostBreakdown("claude-sonnet-4-20250514", usage)
	_, err = db.DB.ExecContext(ctx, `INSERT INTO proxy_requests
		(request_id, session_id, provider, model_requested, model_used, started_at, completed_at,
		 input_tokens, output_tokens, total_tokens, input_cost_usd, output_cost_usd, cost_usd, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"req-1", sessionID, "anthropic", "claude-sonnet-4-20250514", "claude-sonnet-4-20250514",
		"2025-03-11T10:00:00Z", "2025-03-11T10:00:01Z", usage.InputTokens, usage.OutputTokens,
		usage.InputTokens+usage.OutputTokens, breakdown.InputCostUSD, breakdown.OutputCostUSD, breakdown.TotalCostUSD, "success")
	require.NoError(t, err)

	r := chi.NewRouter()
	r.Get("/api/tasks/runs/{runID}", h.GetTaskStatus)

	req := httptest.NewRequest(http.MethodGet, "/api/tasks/runs/1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, float64(1), body["proxy_request_count"])
	assert.Equal(t, float64(1000), body["proxy_input_tokens"])
	assert.Equal(t, float64(250), body["proxy_output_tokens"])
	assert.Greater(t, body["proxy_cost_usd"].(float64), 0.0)
}
