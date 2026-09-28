package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

func TestRequestMetricsRecordsAPIIdentityAndExcludesMetricsRoutes(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "metrics.db"))
	require.NoError(t, err)
	defer db.Close()

	r := chi.NewRouter()
	r.Use(RequestMetrics(db))
	r.Post("/api/board/{project}/tasks", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "Frontend Dev", body["subscriber_id"])
		w.WriteHeader(http.StatusCreated)
	})
	r.Post("/api/call-metrics", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	r.Post("/proxy/{sessionID}/v1/responses", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/board/death-or-trade/tasks", bytes.NewBufferString(`{"subscriber_id":"Frontend Dev"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(httptest.NewRecorder(), req)
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/call-metrics", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/proxy/claude-session/v1/responses", nil))

	rows, err := db.CallMetricSummary(context.Background(), time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, rows, 2)
	var apiRow, proxyRow store.CallMetricSummary
	for _, row := range rows {
		if row.CallType == "api" {
			apiRow = row
		} else {
			proxyRow = row
		}
	}
	require.Equal(t, "/api/board/{project}/tasks", apiRow.Operation)
	require.Equal(t, "Frontend Dev", apiRow.AgentName)
	require.Equal(t, "death-or-trade", apiRow.BoardName)
	require.Equal(t, 1, apiRow.Calls)
	require.Equal(t, "proxy", proxyRow.CallType)
	require.Equal(t, "/proxy/{sessionID}/v1/responses", proxyRow.Operation)
	require.Equal(t, "claude-session", proxyRow.AgentName)
}
