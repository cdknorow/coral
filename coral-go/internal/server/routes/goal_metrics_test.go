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

	"github.com/cdknorow/coral/internal/background"
	"github.com/cdknorow/coral/internal/store"
)

type fakeGoalStatus struct{ st background.GoalGeneratorStatus }

func (f fakeGoalStatus) Status(context.Context) background.GoalGeneratorStatus { return f.st }

func TestGoalMetricsEndpoint(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h := NewGoalMetricsHandler(db)
	now := time.Now()
	h.SetStatusProvider(fakeGoalStatus{background.GoalGeneratorStatus{Enabled: true, CLIFound: true, LastPoll: now.UTC().Format(store.ISOFormat)}})
	ms := store.NewGoalMetricsStore(db)
	ctx := context.Background()
	require.NoError(t, ms.Record(ctx, &store.GoalGeneration{SessionID: "s1", AgentName: "coral-go", Trigger: "first", Outcome: "stored", Goal: "Fix tests", CostUSD: 0.001, DurationMs: 900}))
	require.NoError(t, ms.Record(ctx, &store.GoalGeneration{SessionID: "s1", AgentName: "coral-go", Trigger: "manual", Outcome: "failed", Error: "exit status 1"}))

	r := chi.NewRouter()
	r.Get("/api/goals/metrics", h.Metrics)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/goals/metrics?hours=1")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var got map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, 1.0, got["window_hours"])
	assert.Equal(t, 2.0, got["cli_calls"])
	assert.Equal(t, 0.5, got["failure_rate"])
	assert.Equal(t, map[string]any{"first": 1.0, "manual": 1.0}, got["by_trigger"])
	assert.Len(t, got["recent_failures"], 1)
	assert.Equal(t, true, got["generator"].(map[string]any)["cli_found"])
	assert.Equal(t, []any{}, got["alerts"], "2 calls is below the failure-rate floor")

	bad, err := http.Get(srv.URL + "/api/goals/metrics?hours=0")
	require.NoError(t, err)
	bad.Body.Close()
	assert.Equal(t, http.StatusBadRequest, bad.StatusCode)
}

func TestGoalAlerts(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	healthy := &background.GoalGeneratorStatus{Enabled: true, CLIFound: true, LastPoll: now.Add(-10 * time.Second).Format(store.ISOFormat)}
	base := func() store.GoalMetrics { return store.GoalMetrics{ByOutcome: map[string]int{}} }

	assert.Empty(t, goalAlerts(base(), healthy, now))
	assert.Equal(t, []string{"Goal generator is not running."}, goalAlerts(base(), nil, now))

	off := *healthy
	off.Enabled = false
	assert.Contains(t, goalAlerts(base(), &off, now)[0], "turned off")

	noCLI := *healthy
	noCLI.CLIFound = false
	assert.Contains(t, goalAlerts(base(), &noCLI, now)[0], "claude CLI was not found")

	stale := *healthy
	stale.LastPoll = now.Add(-5 * time.Minute).Format(store.ISOFormat)
	assert.Contains(t, goalAlerts(base(), &stale, now)[0], "may be stuck")

	m := base()
	m.CLICalls, m.FailureRate = 10, 0.3
	m.ByOutcome[store.GoalOutcomeTimeout] = 2
	m.P95DurationMs = 50_000
	m.AvgCostUSD = 0.02
	m.ByOutcome[store.GoalOutcomeStored], m.ByOutcome[store.GoalOutcomeUnchanged] = 3, 9
	m.Sessions = []store.GoalSessionMetrics{{SessionID: "s1", AgentName: "coral-go", CLICallsPerHr: 45}}
	alerts := goalAlerts(m, healthy, now)
	require.Len(t, alerts, 6)
	assert.Contains(t, alerts[0], "30% of 10 goal calls failed")
	assert.Contains(t, alerts[1], "2 goal call(s) timed out")
	assert.Contains(t, alerts[2], "p95 50.0s")
	assert.Contains(t, alerts[3], "$0.0200")
	assert.Contains(t, alerts[4], "coral-go (s1) is refreshing 45 times an hour")
	assert.Contains(t, alerts[5], "9 of 12 refreshes returned the same goal")
}
