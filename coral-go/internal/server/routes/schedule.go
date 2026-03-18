package routes

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store"
)

// ScheduleHandler handles scheduled jobs API endpoints.
type ScheduleHandler struct {
	sched *store.ScheduleStore
	cfg   *config.Config
}

func NewScheduleHandler(db *store.DB, cfg *config.Config) *ScheduleHandler {
	return &ScheduleHandler{
		sched: store.NewScheduleStore(db),
		cfg:   cfg,
	}
}

// ListJobs returns all scheduled jobs.
// GET /api/scheduled/jobs
func (h *ScheduleHandler) ListJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := h.sched.ListScheduledJobs(r.Context(), false)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if jobs == nil {
		jobs = []store.ScheduledJob{}
	}
	writeJSON(w, http.StatusOK, jobs)
}

// GetJob returns a single scheduled job.
// GET /api/scheduled/jobs/{jobID}
func (h *ScheduleHandler) GetJob(w http.ResponseWriter, r *http.Request) {
	jobID, _ := strconv.ParseInt(chi.URLParam(r, "jobID"), 10, 64)
	job, err := h.sched.GetScheduledJob(r.Context(), jobID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// CreateJob creates a new scheduled job.
// POST /api/scheduled/jobs
func (h *ScheduleHandler) CreateJob(w http.ResponseWriter, r *http.Request) {
	var job store.ScheduledJob
	if err := decodeJSON(r, &job); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	created, err := h.sched.CreateScheduledJob(r.Context(), &job)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, created)
}

// UpdateJob updates an existing scheduled job.
// PUT /api/scheduled/jobs/{jobID}
func (h *ScheduleHandler) UpdateJob(w http.ResponseWriter, r *http.Request) {
	jobID, _ := strconv.ParseInt(chi.URLParam(r, "jobID"), 10, 64)
	var fields map[string]interface{}
	if err := decodeJSON(r, &fields); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	updated, err := h.sched.UpdateScheduledJob(r.Context(), jobID, fields)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// DeleteJob deletes a scheduled job and its run history.
// DELETE /api/scheduled/jobs/{jobID}
func (h *ScheduleHandler) DeleteJob(w http.ResponseWriter, r *http.Request) {
	jobID, _ := strconv.ParseInt(chi.URLParam(r, "jobID"), 10, 64)
	if err := h.sched.DeleteScheduledJob(r.Context(), jobID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ToggleJob pauses or resumes a scheduled job.
// POST /api/scheduled/jobs/{jobID}/toggle
func (h *ScheduleHandler) ToggleJob(w http.ResponseWriter, r *http.Request) {
	jobID, _ := strconv.ParseInt(chi.URLParam(r, "jobID"), 10, 64)
	job, err := h.sched.GetScheduledJob(r.Context(), jobID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}
	newEnabled := 1
	if job.Enabled == 1 {
		newEnabled = 0
	}
	h.sched.UpdateScheduledJob(r.Context(), jobID, map[string]interface{}{"enabled": newEnabled})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": newEnabled == 1})
}

// GetJobRuns returns run history for a job.
// GET /api/scheduled/jobs/{jobID}/runs
func (h *ScheduleHandler) GetJobRuns(w http.ResponseWriter, r *http.Request) {
	jobID, _ := strconv.ParseInt(chi.URLParam(r, "jobID"), 10, 64)
	limit := queryInt(r, "limit", 20)
	runs, err := h.sched.GetRunsForJob(r.Context(), jobID, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if runs == nil {
		runs = []store.ScheduledRun{}
	}
	writeJSON(w, http.StatusOK, runs)
}

// GetRecentRuns returns recent runs across all jobs.
// GET /api/scheduled/runs/recent
func (h *ScheduleHandler) GetRecentRuns(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)
	runs, err := h.sched.ListAllRecentRuns(r.Context(), limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if runs == nil {
		runs = []store.ScheduledRun{}
	}
	writeJSON(w, http.StatusOK, runs)
}

// ValidateCron validates a cron expression.
// POST /api/scheduled/validate-cron
func (h *ScheduleHandler) ValidateCron(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CronExpr string `json:"cron_expr"`
		Timezone string `json:"timezone"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	// TODO: validate cron expression and return next fire times
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "next_fires": []string{}})
}
