package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// A launch whose working directory does not exist is the caller's mistake:
// 400 with the field named, so the dialog can mark it (it was a 500) (#3152).
func TestLaunchWithMissingDirectoryIs400NamingTheField(t *testing.T) {
	server, _, _, _ := setupSessionsTestServerWithConfig(t, config.Load(t.TempDir()))
	for _, dir := range []string{"/definitely/not/a/real/dir", filepath.Join(t.TempDir(), "missing")} {
		resp, err := http.Post(server.URL+"/api/sessions/launch", "application/json",
			strings.NewReader(`{"agent_type":"claude","working_dir":`+jsonString(dir)+`}`))
		require.NoError(t, err)
		var body map[string]string
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		resp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, dir)
		assert.Equal(t, "working_dir", body["field"], dir)
		assert.Equal(t, "directory not found: "+dir, body["error"], dir)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// The dashboard's and hub's own plumbing routes are not tool or API calls:
// they are not recorded, and rows already stored are not shown. Through the
// hub's proxy only the remote's polls are plumbing; a proxied action (launch,
// send) is recorded like a local one (#3152).
func TestCallMetricsSkipInternalRoutes(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "metrics.db"))
	require.NoError(t, err)
	defer db.Close()

	r := chi.NewRouter()
	r.Use(RequestMetrics(db))
	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
	r.Get("/api/health", ok)
	r.Post("/api/tracking/event", ok)
	r.Get("/api/system/status", ok)
	r.HandleFunc("/api/remote/{server}/*", ok)
	r.Get("/api/board/{project}/messages", ok)
	serve := func(method, path string) { r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, path, nil)) }
	serve(http.MethodGet, "/api/health")
	serve(http.MethodGet, "/api/system/status")
	serve(http.MethodPost, "/api/tracking/event")
	serve(http.MethodGet, "/api/board/team/messages")
	// Proxied to a remote: its polls are plumbing, its actions are not.
	serve(http.MethodGet, "/api/remote/box/api/health")
	serve(http.MethodGet, "/api/remote/box/api/system/status")
	serve(http.MethodGet, "/api/remote/box/api/sessions/live")
	serve(http.MethodPost, "/api/remote/box/api/sessions/launch")
	serve(http.MethodPost, "/api/remote/box/api/sessions/live/agent/send")

	// A row recorded before this change, for an internal route, is not shown either.
	require.NoError(t, db.RecordCallMetric(context.Background(), &store.CallMetric{CallType: "api", Operation: "/api/health", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}))

	rows, err := db.CallMetricSummary(context.Background(), time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	calls := map[string]int{}
	for _, row := range rows {
		calls[row.Operation] += row.Calls
	}
	assert.Equal(t, map[string]int{"/api/board/{project}/messages": 1, "/api/remote/{server}/*": 2}, calls,
		"the agent-facing call and the two proxied actions are listed; no plumbing")

	for op, want := range map[string]bool{
		"/api/health": true, "/api/tracking/event": true, "/api/system/status": true,
		"/api/healthz": false, "/api/board/{project}/messages": false, "/api/system/statuses": false, "/api/remote/{server}/*": false,
	} {
		assert.Equal(t, want, store.IsInternalMetricOperation(op), op)
	}
	for _, c := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/api/health", true}, {"GET", "/api/system/status", true}, {"GET", "/api/sessions/live", true},
		{"POST", "/api/sessions/launch", false}, {"POST", "/api/sessions/live/a/send", false}, {"GET", "/api/sessions/live/a/capture", false},
	} {
		assert.Equal(t, c.want, store.IsInternalRemotePath(c.method, c.path), c)
	}
}
