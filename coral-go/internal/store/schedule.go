package store

import (
	"context"
	"database/sql"

	at "github.com/subgentic/coral/internal/agenttypes"
)

// ScheduledJob represents a cron job definition.
type ScheduledJob struct {
	ID              int64  `db:"id" json:"id"`
	Name            string `db:"name" json:"name"`
	Description     string `db:"description" json:"description"`
	CronExpr        string `db:"cron_expr" json:"cron_expr"`
	Timezone        string `db:"timezone" json:"timezone"`
	AgentType       string `db:"agent_type" json:"agent_type"`
	RepoPath        string `db:"repo_path" json:"repo_path"`
	BaseBranch      string `db:"base_branch" json:"base_branch"`
	Prompt          string `db:"prompt" json:"prompt"`
	Enabled         int    `db:"enabled" json:"enabled"`
	MaxDurationS    int    `db:"max_duration_s" json:"max_duration_s"`
	CleanupWorktree int    `db:"cleanup_worktree" json:"cleanup_worktree"`
	Flags           string `db:"flags" json:"flags"`
	JobType         string `db:"job_type" json:"job_type"`
	WorkflowID      *int64 `db:"workflow_id" json:"workflow_id,omitempty"`
	CreatedAt       string `db:"created_at" json:"created_at"`
	UpdatedAt       string `db:"updated_at" json:"updated_at"`
	// PausedReason says why the scheduler paused the job itself; empty
	// otherwise (#3171).
	PausedReason string `db:"paused_reason" json:"paused_reason"`
	// ResumedAt is when the job was last enabled again after being off; the
	// scheduler's auto-pause counts only runs scheduled after it (#3171).
	ResumedAt string `db:"resumed_at" json:"resumed_at,omitempty"`
}

// ScheduledRun represents a job execution record.
type ScheduledRun struct {
	ID                int64   `db:"id" json:"id"`
	JobID             int64   `db:"job_id" json:"job_id"`
	SessionID         *string `db:"session_id" json:"session_id"`
	WorktreePath      *string `db:"worktree_path" json:"worktree_path"`
	Status            string  `db:"status" json:"status"`
	ScheduledAt       string  `db:"scheduled_at" json:"scheduled_at"`
	StartedAt         *string `db:"started_at" json:"started_at"`
	FinishedAt        *string `db:"finished_at" json:"finished_at"`
	ExitReason        *string `db:"exit_reason" json:"exit_reason"`
	ErrorMsg          *string `db:"error_msg" json:"error_msg"`
	ErrorCode         *string `db:"error_code" json:"error_code,omitempty"` // a fixed word for why the run failed ("not_signed_in", #5469)
	AgentType         *string `db:"agent_type" json:"agent_type,omitempty"` // the agent CLI the error code is about
	TriggerType       *string `db:"trigger_type" json:"trigger_type"`
	WebhookURL        *string `db:"webhook_url" json:"-"` // unused: webhooks were removed (#3646); kept for SELECT *, wiped at start
	DisplayName       *string `db:"display_name" json:"display_name"`
	CreatedAt         string  `db:"created_at" json:"created_at"`
	JobName           *string `db:"job_name" json:"job_name,omitempty"` // populated by JOIN queries
	ProxyCostUSD      float64 `db:"-" json:"proxy_cost_usd"`
	ProxyRequestCount int     `db:"-" json:"proxy_request_count"`
	ProxyInputTokens  int     `db:"-" json:"proxy_input_tokens"`
	ProxyOutputTokens int     `db:"-" json:"proxy_output_tokens"`
}

// ScheduleStore provides CRUD for scheduled jobs and runs.
type ScheduleStore struct {
	db *DB
}

// NewScheduleStore creates a new ScheduleStore.
func NewScheduleStore(db *DB) *ScheduleStore {
	return &ScheduleStore{db: db}
}

// ── Scheduled Jobs ─────────────────────────────────────────────────────

// ListScheduledJobs returns all jobs (excluding __oneshot__ sentinel).
func (s *ScheduleStore) ListScheduledJobs(ctx context.Context, enabledOnly bool) ([]ScheduledJob, error) {
	var jobs []ScheduledJob
	query := "SELECT * FROM scheduled_jobs WHERE name != '__oneshot__'"
	if enabledOnly {
		query += " AND enabled = 1"
	}
	query += " ORDER BY name"
	err := s.db.SelectContext(ctx, &jobs, query)
	return jobs, err
}

// GetScheduledJob returns a job by ID.
func (s *ScheduleStore) GetScheduledJob(ctx context.Context, jobID int64) (*ScheduledJob, error) {
	var job ScheduledJob
	err := s.db.GetContext(ctx, &job, "SELECT * FROM scheduled_jobs WHERE id = ?", jobID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &job, err
}

// CreateScheduledJob creates a new scheduled job.
func (s *ScheduleStore) CreateScheduledJob(ctx context.Context, job *ScheduledJob) (*ScheduledJob, error) {
	now := nowUTC()
	job.CreatedAt = now
	job.UpdatedAt = now
	if job.Timezone == "" {
		job.Timezone = "UTC"
	}
	if job.AgentType == "" {
		job.AgentType = at.Claude
	}
	// An empty base branch stays empty: the repository's current branch, found when the job runs (#6701).
	if job.MaxDurationS == 0 {
		job.MaxDurationS = 3600
	}
	if job.JobType == "" {
		job.JobType = "prompt"
	}

	result, err := s.db.ExecContext(ctx,
		`INSERT INTO scheduled_jobs
		 (name, description, cron_expr, timezone, agent_type, repo_path,
		  base_branch, prompt, enabled, max_duration_s, cleanup_worktree,
		  flags, job_type, workflow_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.Name, job.Description, job.CronExpr, job.Timezone, job.AgentType,
		job.RepoPath, job.BaseBranch, job.Prompt, job.Enabled, job.MaxDurationS,
		job.CleanupWorktree, job.Flags, job.JobType, job.WorkflowID, now, now)
	if err != nil {
		return nil, err
	}
	job.ID, _ = result.LastInsertId()
	return job, nil
}

// UpdateScheduledJob updates allowed fields on a job.
func (s *ScheduleStore) UpdateScheduledJob(ctx context.Context, jobID int64, fields map[string]interface{}) (*ScheduledJob, error) {
	// Enabled again after being off (a resume, by any path): note when, so the
	// failures before it no longer count towards an auto-pause (#3171).
	if en, ok := fields["enabled"]; ok && enabledValue(en) {
		var was int
		if err := s.db.GetContext(ctx, &was, "SELECT enabled FROM scheduled_jobs WHERE id = ?", jobID); err == nil && was == 0 {
			fields["resumed_at"] = nowUTC()
		}
	}
	err := dynamicUpdate(ctx, s.db, "scheduled_jobs", jobID, fields, map[string]bool{
		"name": true, "description": true, "cron_expr": true, "timezone": true,
		"agent_type": true, "repo_path": true, "base_branch": true, "prompt": true,
		"enabled": true, "max_duration_s": true, "cleanup_worktree": true, "flags": true,
		"job_type": true, "workflow_id": true, "paused_reason": true, "resumed_at": true,
	}, map[string]bool{
		"enabled": true, "cleanup_worktree": true,
	}, true)
	if err != nil {
		return nil, err
	}
	return s.GetScheduledJob(ctx, jobID)
}

// enabledValue reads an "enabled" field as the routes and the scheduler send it.
func enabledValue(v interface{}) bool {
	switch x := v.(type) {
	case bool:
		return x
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	}
	return false
}

// DeleteScheduledJob deletes a job (cascades to runs via FK).
func (s *ScheduleStore) DeleteScheduledJob(ctx context.Context, jobID int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM scheduled_jobs WHERE id = ?", jobID)
	return err
}

// ── Scheduled Runs ─────────────────────────────────────────────────────

// CreateScheduledRun creates a new run record.
func (s *ScheduleStore) CreateScheduledRun(ctx context.Context, jobID int64, scheduledAt, status string) (int64, error) {
	now := nowUTC()
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO scheduled_runs (job_id, status, scheduled_at, created_at)
		 VALUES (?, ?, ?, ?)`,
		jobID, status, scheduledAt, now)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// UpdateScheduledRun updates allowed fields on a run.
func (s *ScheduleStore) UpdateScheduledRun(ctx context.Context, runID int64, fields map[string]interface{}) error {
	return dynamicUpdate(ctx, s.db, "scheduled_runs", runID, fields, map[string]bool{
		"session_id": true, "worktree_path": true, "status": true,
		"started_at": true, "finished_at": true, "exit_reason": true, "error_msg": true,
		"error_code": true, "agent_type": true,
	}, nil, false)
}

// GetRunsForJob returns recent runs for a specific job.
func (s *ScheduleStore) GetRunsForJob(ctx context.Context, jobID int64, limit int) ([]ScheduledRun, error) {
	var runs []ScheduledRun
	err := s.db.SelectContext(ctx, &runs,
		"SELECT * FROM scheduled_runs WHERE job_id = ? ORDER BY scheduled_at DESC, id DESC LIMIT ?",
		jobID, limit)
	return runs, err
}

// GetLastRunForJob returns the most recent run for a job.
func (s *ScheduleStore) GetLastRunForJob(ctx context.Context, jobID int64) (*ScheduledRun, error) {
	var run ScheduledRun
	err := s.db.GetContext(ctx, &run,
		"SELECT * FROM scheduled_runs WHERE job_id = ? ORDER BY scheduled_at DESC, id DESC LIMIT 1", jobID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &run, err
}

// GetActiveRunForJob returns the currently pending/running run for a job.
func (s *ScheduleStore) GetActiveRunForJob(ctx context.Context, jobID int64) (*ScheduledRun, error) {
	var run ScheduledRun
	err := s.db.GetContext(ctx, &run,
		`SELECT * FROM scheduled_runs WHERE job_id = ? AND status IN ('pending', 'running')
		 ORDER BY scheduled_at DESC, id DESC LIMIT 1`, jobID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &run, err
}

// ListAllRecentRuns returns recent runs across all jobs with job name.
func (s *ScheduleStore) ListAllRecentRuns(ctx context.Context, limit int) ([]ScheduledRun, error) {
	var runs []ScheduledRun
	err := s.db.SelectContext(ctx, &runs,
		`SELECT r.*, `+runJobNameSQL+` as job_name
		 FROM scheduled_runs r JOIN scheduled_jobs j ON j.id = r.job_id
		 ORDER BY r.scheduled_at DESC, r.id DESC LIMIT ?`, limit)
	return runs, err
}

// GetScheduledRun returns a single run by ID.
func (s *ScheduleStore) GetScheduledRun(ctx context.Context, runID int64) (*ScheduledRun, error) {
	var run ScheduledRun
	err := s.db.GetContext(ctx, &run, "SELECT * FROM scheduled_runs WHERE id = ?", runID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &run, err
}

// ── One-shot / Live Jobs ──────────────────────────────────────────────

// runJobNameSQL is a run's job name for listings: a one-off run belongs to
// the internal __oneshot__ sentinel, whose name is never shown (#3171).
const runJobNameSQL = `CASE WHEN j.name = '__oneshot__' THEN NULL ELSE j.name END`

// GetOrCreateSentinelJob returns the __oneshot__ sentinel job ID.
func (s *ScheduleStore) GetOrCreateSentinelJob(ctx context.Context) (int64, error) {
	var id int64
	err := s.db.GetContext(ctx, &id, "SELECT id FROM scheduled_jobs WHERE name = '__oneshot__'")
	if err == nil {
		return id, nil
	}

	now := nowUTC()
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO scheduled_jobs
		 (name, cron_expr, timezone, agent_type, repo_path, prompt,
		  enabled, max_duration_s, cleanup_worktree, created_at, updated_at)
		 VALUES ('__oneshot__', '0 0 31 2 *', 'UTC', 'claude', '/dev/null',
		         'sentinel', 0, 3600, 1, ?, ?)`,
		now, now)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// CreateOneshotRun creates a run record for a one-shot API task.
func (s *ScheduleStore) CreateOneshotRun(ctx context.Context, scheduledAt string, displayName *string) (int64, error) {
	sentinelID, err := s.GetOrCreateSentinelJob(ctx)
	if err != nil {
		return 0, err
	}
	now := nowUTC()
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO scheduled_runs
		 (job_id, status, scheduled_at, trigger_type, display_name, created_at)
		 VALUES (?, 'pending', ?, 'api', ?, ?)`,
		sentinelID, scheduledAt, displayName, now)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// ListActiveRuns returns all pending/running runs with job name.
func (s *ScheduleStore) ListActiveRuns(ctx context.Context) ([]ScheduledRun, error) {
	var runs []ScheduledRun
	err := s.db.SelectContext(ctx, &runs,
		`SELECT r.*, `+runJobNameSQL+` as job_name
		 FROM scheduled_runs r LEFT JOIN scheduled_jobs j ON j.id = r.job_id
		 WHERE r.status IN ('pending', 'running')
		 ORDER BY r.scheduled_at DESC, r.id DESC`)
	return runs, err
}

// GetRunningCount returns the count of pending/running runs.
func (s *ScheduleStore) GetRunningCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.GetContext(ctx, &count,
		"SELECT COUNT(*) FROM scheduled_runs WHERE status IN ('pending', 'running')")
	return count, err
}

// GetAllJobSessionIDs returns session_ids for all runs (for filtering from live sessions).
func (s *ScheduleStore) GetAllJobSessionIDs(ctx context.Context) (map[string]bool, error) {
	var ids []string
	err := s.db.SelectContext(ctx, &ids,
		"SELECT session_id FROM scheduled_runs WHERE session_id IS NOT NULL")
	if err != nil {
		return nil, err
	}
	result := make(map[string]bool, len(ids))
	for _, id := range ids {
		result[id] = true
	}
	return result, nil
}

// ListOneshotRuns returns recent one-shot (API-triggered) runs.
func (s *ScheduleStore) ListOneshotRuns(ctx context.Context, limit int, status *string) ([]ScheduledRun, error) {
	var runs []ScheduledRun
	query := "SELECT * FROM scheduled_runs WHERE trigger_type = 'api'"
	args := []interface{}{}
	if status != nil {
		query += " AND status = ?"
		args = append(args, *status)
	}
	query += " ORDER BY scheduled_at DESC, id DESC LIMIT ?"
	args = append(args, limit)
	err := s.db.SelectContext(ctx, &runs, query, args...)
	return runs, err
}
