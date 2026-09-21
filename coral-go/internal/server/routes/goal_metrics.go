package routes

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/cdknorow/coral/internal/background"
	"github.com/cdknorow/coral/internal/store"
)

// Alert thresholds for GET /api/goals/metrics. Each flags something that
// normal operation should not produce.
const (
	// A session can refresh at most every 2 min (30/h) plus manual requests.
	goalAlertCallsPerHour = 30.0
	goalAlertFailureRate  = 0.2
	goalAlertMinCalls     = 5
	// Calls run bare (~400 input tokens, ~$0.001); more means the CLI is
	// sending its full system prompt and tools again.
	goalAlertAvgCostUSD = 0.005
	// 45 s is three quarters of the 60 s CLI timeout.
	goalAlertP95DurationMs = 45_000
	// Most refreshes returning the same goal means they run too often.
	goalAlertUnchangedShare = 0.6
	// The generator polls every 30 s.
	goalAlertPollStale = 2 * time.Minute
)

// GoalStatusProvider reports the goal generator's live state.
type GoalStatusProvider interface {
	Status(ctx context.Context) background.GoalGeneratorStatus
}

// GoalMetricsHandler serves goal generation metrics.
type GoalMetricsHandler struct {
	metrics *store.GoalMetricsStore
	status  GoalStatusProvider // nil until the generator starts
	now     func() time.Time
}

// NewGoalMetricsHandler creates a GoalMetricsHandler.
func NewGoalMetricsHandler(db *store.DB) *GoalMetricsHandler {
	return &GoalMetricsHandler{metrics: store.NewGoalMetricsStore(db), now: time.Now}
}

// SetStatusProvider wires the running goal generator.
func (h *GoalMetricsHandler) SetStatusProvider(p GoalStatusProvider) {
	h.status = p
}

// goalMetricsResponse is the GET /api/goals/metrics body.
type goalMetricsResponse struct {
	store.GoalMetrics
	Generator *background.GoalGeneratorStatus `json:"generator"`
	Alerts    []string                        `json:"alerts"`
}

// Metrics summarizes goal generation over a window.
// GET /api/goals/metrics?hours=24 (1–336, default 24)
func (h *GoalMetricsHandler) Metrics(w http.ResponseWriter, r *http.Request) {
	hours := 24.0
	if v := r.URL.Query().Get("hours"); v != "" {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil || n <= 0 || n > store.GoalMetricsRetention.Hours() {
			errBadRequest(w, fmt.Sprintf("hours must be between 0 and %.0f", store.GoalMetricsRetention.Hours()))
			return
		}
		hours = n
	}
	window := time.Duration(hours * float64(time.Hour))
	since := h.now().Add(-window)
	rows, err := h.metrics.Since(r.Context(), since)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	resp := goalMetricsResponse{GoalMetrics: store.SummarizeGoalGenerations(rows, since, window)}
	if h.status != nil {
		st := h.status.Status(r.Context())
		resp.Generator = &st
	}
	resp.Alerts = goalAlerts(resp.GoalMetrics, resp.Generator, h.now())
	writeJSON(w, http.StatusOK, resp)
}

// goalAlerts lists what looks wrong, in plain words. Empty means healthy.
func goalAlerts(m store.GoalMetrics, st *background.GoalGeneratorStatus, now time.Time) []string {
	alerts := []string{}
	if st == nil {
		return append(alerts, "Goal generator is not running.")
	}
	if !st.Enabled {
		alerts = append(alerts, "Goal generation is turned off (auto_goals = \"false\").")
	}
	if !st.CLIFound || m.ByOutcome[store.GoalOutcomeNoCLI] > 0 {
		alerts = append(alerts, "claude CLI was not found; goals fall back to the first prompt.")
	}
	if st.Enabled && st.LastPoll != "" {
		if last, err := time.Parse(store.ISOFormat, st.LastPoll); err == nil && now.Sub(last) > goalAlertPollStale {
			alerts = append(alerts, fmt.Sprintf("Generator last polled %s ago; it may be stuck.", now.Sub(last).Round(time.Second)))
		}
	}
	if m.CLICalls >= goalAlertMinCalls && m.FailureRate >= goalAlertFailureRate {
		alerts = append(alerts, fmt.Sprintf("%.0f%% of %d goal calls failed; see recent_failures.", m.FailureRate*100, m.CLICalls))
	}
	if n := m.ByOutcome[store.GoalOutcomeTimeout]; n > 0 {
		alerts = append(alerts, fmt.Sprintf("%d goal call(s) timed out.", n))
	}
	if m.P95DurationMs >= goalAlertP95DurationMs {
		alerts = append(alerts, fmt.Sprintf("Goal calls are slow: p95 %.1fs against a 60s timeout.", float64(m.P95DurationMs)/1000))
	}
	if m.CLICalls > 0 && m.AvgCostUSD > goalAlertAvgCostUSD {
		alerts = append(alerts, fmt.Sprintf("Goal calls average $%.4f, above the expected ~$0.001; check the CLI flags.", m.AvgCostUSD))
	}
	for _, s := range m.Sessions {
		if s.CLICallsPerHr > goalAlertCallsPerHour {
			name := s.AgentName
			if name == "" {
				name = s.SessionID
			}
			alerts = append(alerts, fmt.Sprintf("%s (%s) is refreshing %.0f times an hour.", name, s.SessionID, s.CLICallsPerHr))
		}
	}
	answered := m.ByOutcome[store.GoalOutcomeStored] + m.ByOutcome[store.GoalOutcomeUnchanged]
	if answered >= 10 && float64(m.ByOutcome[store.GoalOutcomeUnchanged])/float64(answered) >= goalAlertUnchangedShare {
		alerts = append(alerts, fmt.Sprintf("%d of %d refreshes returned the same goal; they may run too often.", m.ByOutcome[store.GoalOutcomeUnchanged], answered))
	}
	return alerts
}
