package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// #3261: the dashboard's own refresh polls are not calls anyone made and are not
// recorded; the same routes called by an agent's tooling, other methods on
// them, and pending-tool (the hook's per-tool-call signal) still are.
func TestRequestMetricsSkipsDashboardPolls(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "metrics.db"))
	require.NoError(t, err)
	defer db.Close()
	r := chi.NewRouter()
	r.Use(RequestMetrics(db))
	ok := func(w http.ResponseWriter, r *http.Request) {}
	for _, p := range []string{"/api/sessions/live/{name}/chat", "/api/sessions/live/{name}/poll",
		"/api/sessions/live/{name}/prompt-options", "/api/sessions/live/{name}/events",
		"/api/board/{project}/messages/all", "/api/sessions/live/{name}/pending-tool"} {
		r.Get(p, ok)
		r.Post(p, ok)
	}
	do := func(method, url string, hdr map[string]string) {
		req := httptest.NewRequest(method, url, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		r.ServeHTTP(httptest.NewRecorder(), req)
	}
	// The dashboard: session_id in the query, no agent identity.
	for _, u := range []string{"/api/sessions/live/a/chat?session_id=s1", "/api/sessions/live/a/poll?session_id=s1",
		"/api/sessions/live/a/prompt-options", "/api/sessions/live/a/events?limit=50", "/api/board/team/messages/all?limit=100"} {
		do("GET", u, nil)
	}
	// Kept: an agent's tooling, a POST, and pending-tool.
	do("GET", "/api/sessions/live/a/chat", map[string]string{"X-Coral-Agent": "Backend Dev"})
	do("GET", "/api/board/team/messages/all?subscriber_id=Backend+Dev", nil)
	do("POST", "/api/sessions/live/a/events", nil)
	do("GET", "/api/sessions/live/a/pending-tool", nil)

	rows, err := db.CallMetricSummary(context.Background(), time.Now().Add(-time.Hour))
	require.NoError(t, err)
	got := map[string]int{}
	for _, row := range rows {
		got[row.Operation] += row.Calls
	}
	assert.Equal(t, map[string]int{
		"/api/sessions/live/{name}/chat":         1,
		"/api/board/{project}/messages/all":      1,
		"/api/sessions/live/{name}/events":       1,
		"/api/sessions/live/{name}/pending-tool": 1,
	}, got)
}

// The summary never claims a window longer than the rows kept.
func TestCallMetricsSummaryCapsToRetention(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "metrics.db"))
	require.NoError(t, err)
	defer db.Close()
	h := NewCallMetricsHandler(db)
	hours := func(q string) float64 {
		rec := httptest.NewRecorder()
		h.Summary(rec, httptest.NewRequest("GET", "/api/call-metrics/summary"+q, nil))
		require.Equal(t, http.StatusOK, rec.Code)
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		return out["hours"].(float64)
	}
	assert.EqualValues(t, 336, hours("?hours=1000"), "default retention: 14 days")
	require.NoError(t, store.NewSessionStore(db).SetSetting(context.Background(), store.CallMetricsRetentionSetting, "2"))
	assert.EqualValues(t, 48, hours("?hours=336"))
	assert.EqualValues(t, 24, hours(""))
}
