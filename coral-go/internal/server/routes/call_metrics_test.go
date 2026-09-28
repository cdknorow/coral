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

	req := httptest.NewRequest(http.MethodPost, "/api/board/death-or-trade/tasks", bytes.NewBufferString(`{"subscriber_id":"Frontend Dev"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(httptest.NewRecorder(), req)
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/call-metrics", nil))

	rows, err := db.CallMetricSummary(context.Background(), time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "/api/board/{project}/tasks", rows[0].Operation)
	require.Equal(t, "Frontend Dev", rows[0].AgentName)
	require.Equal(t, "death-or-trade", rows[0].BoardName)
	require.Equal(t, 1, rows[0].Calls)
}
