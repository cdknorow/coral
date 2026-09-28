package routes

import (
	"github.com/cdknorow/coral/internal/store"
	"net/http"
	"strconv"
	"time"
)

type CallMetricsHandler struct{ db *store.DB }

func NewCallMetricsHandler(db *store.DB) *CallMetricsHandler { return &CallMetricsHandler{db: db} }

// Summary returns grouped tool/API call counts for the requested window.
func (h *CallMetricsHandler) Summary(w http.ResponseWriter, r *http.Request) {
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	if hours <= 0 {
		hours = 24
	}
	if hours > 336 {
		hours = 336
	}
	rows, err := h.db.CallMetricSummary(r.Context(), time.Now().Add(-time.Duration(hours)*time.Hour))
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hours": hours, "rows": rows})
}

func (h *CallMetricsHandler) Record(w http.ResponseWriter, r *http.Request) {
	var metric store.CallMetric
	if err := decodeJSON(r, &metric); err != nil || metric.CallType == "" || metric.Operation == "" {
		errBadRequest(w, "call_type and operation are required")
		return
	}
	if err := h.db.RecordCallMetric(r.Context(), &metric); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, metric)
}
