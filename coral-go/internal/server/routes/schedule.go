package routes

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/robfig/cron/v3"

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

// ListJobs returns all scheduled jobs enriched with last_run and next_fire_at.
// GET /api/scheduled/jobs
func (h *ScheduleHandler) ListJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := h.sched.ListScheduledJobs(r.Context(), false)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	jobs = emptyIfNil(jobs)

	// Enrich each job with last_run and next_fire_at (matches Python behavior)
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	type enrichedJob struct {
		store.ScheduledJob
		LastRun    *store.ScheduledRun `json:"last_run"`
		NextFireAt *string             `json:"next_fire_at"`
		// ConsecutiveFailures counts failed runs back from the most recent.
		// A job whose scheduling works but whose every run fails is otherwise
		// indistinguishable from a healthy one in the jobs list — it shows as
		// enabled, with a next fire time, accumulating failures nobody sees.
		ConsecutiveFailures int `json:"consecutive_failures"`
	}
	enriched := make([]enrichedJob, 0, len(jobs))
	for _, job := range jobs {
		ej := enrichedJob{ScheduledJob: job}
		lastRun, err := h.sched.GetLastRunForJob(r.Context(), job.ID)
		if err == nil {
			ej.LastRun = lastRun
		}
		ej.ConsecutiveFailures = h.countConsecutiveFailures(r.Context(), job.ID)
		s, err := parser.Parse(job.CronExpr)
		if err == nil {
			loc := time.UTC
			if job.Timezone != "" {
				if tz, err := time.LoadLocation(job.Timezone); err == nil {
					loc = tz
				}
			}
			next := s.Next(time.Now().In(loc))
			if !next.IsZero() {
				ts := next.In(loc).Format("2006-01-02T15:04:05+00:00")
				ej.NextFireAt = &ts
			}
		}
		enriched = append(enriched, ej)
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": enriched})
}

// GetJob returns a single scheduled job.
// GET /api/scheduled/jobs/{jobID}
func (h *ScheduleHandler) GetJob(w http.ResponseWriter, r *http.Request) {
	jobID, _ := strconv.ParseInt(chi.URLParam(r, "jobID"), 10, 64)
	job, err := h.sched.GetScheduledJob(r.Context(), jobID)
	if err != nil {
		errNotFound(w, "job not found")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// CreateJob creates a new scheduled job.
// POST /api/scheduled/jobs
func (h *ScheduleHandler) CreateJob(w http.ResponseWriter, r *http.Request) {
	// Use pointer types for int fields so we can distinguish "not provided" (nil)
	// from "explicitly set to 0". Python defaults enabled=1 and cleanup_worktree=1.
	var body struct {
		Name            string `json:"name"`
		Description     string `json:"description"`
		CronExpr        string `json:"cron_expr"`
		Timezone        string `json:"timezone"`
		AgentType       string `json:"agent_type"`
		RepoPath        string `json:"repo_path"`
		BaseBranch      string `json:"base_branch"`
		Prompt          string `json:"prompt"`
		Enabled         *int   `json:"enabled"`
		MaxDurationS    *int   `json:"max_duration_s"`
		CleanupWorktree *int   `json:"cleanup_worktree"`
		Flags           string `json:"flags"`
		JobType         string `json:"job_type"`
		WorkflowID      *int64 `json:"workflow_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}

	// Validate required fields
	if body.Name == "" {
		errBadRequest(w, "'name' is required")
		return
	}
	if body.CronExpr == "" {
		errBadRequest(w, "'cron_expr' is required")
		return
	}

	// Validate job_type-specific requirements
	jobType := body.JobType
	if jobType == "" {
		jobType = "prompt"
	}
	if jobType == "workflow" {
		if body.WorkflowID == nil {
			errBadRequest(w, "'workflow_id' is required for workflow jobs")
			return
		}
	} else {
		if body.RepoPath == "" {
			errBadRequest(w, "'repo_path' is required")
			return
		}
		if body.Prompt == "" {
			errBadRequest(w, "'prompt' is required")
			return
		}
	}

	// Validate cron expression
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	if _, err := parser.Parse(body.CronExpr); err != nil {
		errBadRequest(w, "Invalid cron expression: "+body.CronExpr)
		return
	}

	// Build ScheduledJob with defaults
	job := store.ScheduledJob{
		Name:            body.Name,
		Description:     body.Description,
		CronExpr:        body.CronExpr,
		Timezone:        body.Timezone,
		AgentType:       body.AgentType,
		RepoPath:        body.RepoPath,
		BaseBranch:      body.BaseBranch,
		Prompt:          body.Prompt,
		Flags:           body.Flags,
		JobType:         jobType,
		WorkflowID:      body.WorkflowID,
		Enabled:         intPtrOr(body.Enabled, 1),
		MaxDurationS:    intPtrOr(body.MaxDurationS, 3600),
		CleanupWorktree: intPtrOr(body.CleanupWorktree, 1),
	}
	if job.Timezone == "" {
		job.Timezone = "UTC"
	}
	if job.AgentType == "" {
		job.AgentType = "claude"
	}
	if job.BaseBranch == "" {
		job.BaseBranch = "main"
	}

	created, err := h.sched.CreateScheduledJob(r.Context(), &job)
	if err != nil {
		errInternalServer(w, err.Error())
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
		errBadRequest(w, "invalid JSON")
		return
	}

	// Validate cron expression if provided (matches Python behavior)
	if cronExpr, ok := fields["cron_expr"]; ok {
		if expr, ok := cronExpr.(string); ok {
			parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
			if _, err := parser.Parse(expr); err != nil {
				errBadRequest(w, "Invalid cron expression: "+expr)
				return
			}
		}
	}

	updated, err := h.sched.UpdateScheduledJob(r.Context(), jobID, fields)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	if updated == nil {
		errNotFound(w, "Job not found")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// DeleteJob deletes a scheduled job and its run history.
// DELETE /api/scheduled/jobs/{jobID}
func (h *ScheduleHandler) DeleteJob(w http.ResponseWriter, r *http.Request) {
	jobID, _ := strconv.ParseInt(chi.URLParam(r, "jobID"), 10, 64)
	if err := h.sched.DeleteScheduledJob(r.Context(), jobID); err != nil {
		errInternalServer(w, err.Error())
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
		errNotFound(w, "job not found")
		return
	}
	newEnabled := 1
	if job.Enabled == 1 {
		newEnabled = 0
	}
	updated, err := h.sched.UpdateScheduledJob(r.Context(), jobID, map[string]interface{}{"enabled": newEnabled})
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// GetJobRuns returns run history for a job.
// GET /api/scheduled/jobs/{jobID}/runs
func (h *ScheduleHandler) GetJobRuns(w http.ResponseWriter, r *http.Request) {
	jobID, _ := strconv.ParseInt(chi.URLParam(r, "jobID"), 10, 64)
	limit := queryInt(r, "limit", 20)
	runs, err := h.sched.GetRunsForJob(r.Context(), jobID, limit)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": emptyIfNil(runs)})
}

// GetRecentRuns returns recent runs across all jobs.
// GET /api/scheduled/runs/recent
func (h *ScheduleHandler) GetRecentRuns(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)
	runs, err := h.sched.ListAllRecentRuns(r.Context(), limit)
	if err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": emptyIfNil(runs)})
}

// ValidateCron validates a cron expression and returns next fire times.
// POST /api/scheduled/validate-cron
func (h *ScheduleHandler) ValidateCron(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CronExpr string `json:"cron_expr"`
		Timezone string `json:"timezone"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if body.CronExpr == "" {
		errBadRequest(w, "cron_expr is required")
		return
	}

	// Parse the cron expression (5-field format)
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse(body.CronExpr)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"valid": false, "error": "Invalid cron expression"})
		return
	}

	// Determine timezone
	loc := time.UTC
	if body.Timezone != "" {
		if tz, err := time.LoadLocation(body.Timezone); err == nil {
			loc = tz
		}
	}

	// Compute next 5 fire times
	now := time.Now().In(loc)
	var nextFires []string
	t := now
	for i := 0; i < 5; i++ {
		t = sched.Next(t)
		if t.IsZero() {
			break
		}
		nextFires = append(nextFires, t.In(loc).Format("2006-01-02T15:04:05+00:00"))
	}

	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "next_fire_times": nextFires})
}

// consecutiveFailureWindow is how many recent runs are examined when deciding
// whether a job is failing. Enough to be confident it is not a one-off, small
// enough to stay cheap for every job on every list.
const consecutiveFailureWindow = 20

// countConsecutiveFailures counts failed runs back from the most recent,
// stopping at the first run that did not fail. Pending and running runs stop
// the count without contributing to it: a job that is currently trying again
// is not yet a job that failed again.
func (h *ScheduleHandler) countConsecutiveFailures(ctx context.Context, jobID int64) int {
	runs, err := h.sched.GetRunsForJob(ctx, jobID, consecutiveFailureWindow)
	if err != nil {
		return 0
	}
	return countConsecutiveFailedRuns(runs)
}

// countConsecutiveFailedRuns counts failed runs from the front of a
// newest-first list, stopping at the first run that did not fail.
func countConsecutiveFailedRuns(runs []store.ScheduledRun) int {
	n := 0
	for _, run := range runs {
		if run.Status != "failed" {
			break
		}
		n++
	}
	return n
}
