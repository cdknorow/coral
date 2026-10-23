package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"

	"github.com/subgentic/coral/internal/ptr"
)

// Workflow represents a reusable multi-step automation definition.
type Workflow struct {
	ID               int64  `db:"id" json:"id"`
	Name             string `db:"name" json:"name"`
	Description      string `db:"description" json:"description"`
	StepsJSON        string `db:"steps_json" json:"-"`
	DefaultAgentJSON string `db:"default_agent_json" json:"-"`
	RepoPath         string `db:"repo_path" json:"repo_path"`
	BaseBranch       string `db:"base_branch" json:"base_branch"`
	MaxDurationS     int    `db:"max_duration_s" json:"max_duration_s"`
	CleanupWorktree  int    `db:"cleanup_worktree" json:"cleanup_worktree"`
	Enabled          int    `db:"enabled" json:"enabled"`
	CreatedAt        string `db:"created_at" json:"created_at"`
	UpdatedAt        string `db:"updated_at" json:"updated_at"`

	// Computed fields (not stored in DB directly, populated by JSON unmarshal)
	Steps        []json.RawMessage `db:"-" json:"steps,omitempty"`
	DefaultAgent json.RawMessage   `db:"-" json:"default_agent,omitempty"`
	StepCount    int               `db:"-" json:"step_count,omitempty"`
	LastRun      *WorkflowRunBrief `db:"-" json:"last_run,omitempty"`
}

// WorkflowRunBrief is a summary of the most recent run, included in list responses.
type WorkflowRunBrief struct {
	ID          int64   `json:"id"`
	Status      string  `json:"status"`
	TriggerType string  `json:"trigger_type"`
	StartedAt   *string `json:"started_at"`
	FinishedAt  *string `json:"finished_at"`
}

// WorkflowRun represents a single execution of a workflow.
type WorkflowRun struct {
	ID             int64   `db:"id" json:"id"`
	WorkflowID     int64   `db:"workflow_id" json:"workflow_id"`
	TriggerType    string  `db:"trigger_type" json:"trigger_type"`
	TriggerContext *string `db:"trigger_context" json:"trigger_context,omitempty"`
	Status         string  `db:"status" json:"status"`
	CurrentStep    int     `db:"current_step" json:"current_step"`
	StepResults    string  `db:"step_results" json:"-"`
	WorktreePath   string  `db:"worktree_path" json:"worktree_path,omitempty"`
	StartedAt      *string `db:"started_at" json:"started_at,omitempty"`
	FinishedAt     *string `db:"finished_at" json:"finished_at,omitempty"`
	ErrorMsg       *string `db:"error_msg" json:"error_msg,omitempty"`
	// ErrorCode is a fixed word for why the run failed ("not_signed_in", #5469) and AgentType the agent CLI it is about; both are left out of every other run.
	ErrorCode *string `db:"error_code" json:"error_code,omitempty"`
	AgentType *string `db:"agent_type" json:"agent_type,omitempty"`
	CreatedAt string  `db:"created_at" json:"created_at"`

	// Computed fields populated by queries
	WorkflowName *string           `db:"workflow_name" json:"workflow_name,omitempty"`
	Steps        []json.RawMessage `db:"-" json:"steps,omitempty"`
}

// HydrateSteps parses StepsJSON and DefaultAgentJSON into their typed fields.
func (w *Workflow) HydrateSteps() {
	if w.StepsJSON != "" && w.StepsJSON != "[]" {
		if err := json.Unmarshal([]byte(w.StepsJSON), &w.Steps); err != nil {
			log.Printf("[store] workflow %d: failed to parse steps_json: %v", w.ID, err)
		}
	}
	if w.DefaultAgentJSON != "" {
		w.DefaultAgent = json.RawMessage(w.DefaultAgentJSON)
	}
	w.StepCount = len(w.Steps)
}

// HydrateStepResults parses the StepResults JSON into the Steps field.
func (r *WorkflowRun) HydrateStepResults() {
	if r.StepResults != "" && r.StepResults != "[]" {
		if err := json.Unmarshal([]byte(r.StepResults), &r.Steps); err != nil {
			log.Printf("[store] workflow run %d: failed to parse step_results: %v", r.ID, err)
		}
	}
}

// WorkflowStore provides CRUD for workflows and workflow runs.
type WorkflowStore struct {
	db *DB
}

// NewWorkflowStore creates a new WorkflowStore.
func NewWorkflowStore(db *DB) *WorkflowStore {
	return &WorkflowStore{db: db}
}

// DB returns the underlying database connection.
func (s *WorkflowStore) DB() *DB {
	return s.db
}

// ── Workflows ─────────────────────────────────────────────────────────

// SeedDemoWorkflow creates the daily-motivation example workflow and a scheduled
// job that would run it every weekday morning. The job is created OFF: an enabled job on a new
// install would start an agent and spend the person's CLI credit before they have asked for anything
// (#6384); the person turns it on from the Jobs tab. Uses a user_settings flag so deleted examples
// won't re-appear on restart.
func (s *WorkflowStore) SeedDemoWorkflow(ctx context.Context, schedStore *ScheduleStore) {
	var seeded string
	err := s.db.GetContext(ctx, &seeded, "SELECT value FROM user_settings WHERE key = 'demo_workflows_seeded'")
	if err == nil && seeded == "1" {
		return
	}

	// Mark as seeded before creating so we never re-seed even if creation fails
	s.db.ExecContext(ctx, "INSERT OR REPLACE INTO user_settings (key, value) VALUES ('demo_workflows_seeded', '1')")

	stepsJSON := `[{"name":"generate-message","type":"agent","prompt":"Generate a single short, unique, and inspiring motivational message (2-3 sentences max). Output ONLY the message text, nothing else — no quotes, no labels, no explanation."},{"name":"show-notification","type":"shell","command":"MSG=$(cat {{prev_stdout}}) && curl -s -X POST http://localhost:${CORAL_PORT}/api/notifications -H 'Content-Type: application/json' -d \"{\\\"title\\\":\\\"☀️ Daily Motivation\\\",\\\"message\\\":\\\"$MSG\\\",\\\"type\\\":\\\"alert\\\",\\\"level\\\":\\\"success\\\",\\\"link\\\":{\\\"label\\\":\\\"Learn about Workflows →\\\",\\\"url\\\":\\\"docs://workflow-quickstart\\\"}}\""}]`
	defaultAgentJSON := `{"agent_type":"claude"}`

	wf, err := s.CreateWorkflow(ctx, &Workflow{
		Name:             "daily-motivation",
		Description:      "Example: generates a motivational message and shows it as a popup",
		StepsJSON:        stepsJSON,
		DefaultAgentJSON: defaultAgentJSON,
		Enabled:          1,
	})
	if err != nil {
		log.Printf("Warning: failed to seed demo workflow: %v", err)
		return
	}
	log.Printf("Seeded demo workflow: daily-motivation")

	// Seed a scheduled job to run it every weekday morning at 9am
	if schedStore != nil {
		wfID := wf.ID
		_, err := schedStore.CreateScheduledJob(ctx, &ScheduledJob{
			Name:        "morning-motivation",
			Description: "Example (switched off): would run the daily-motivation workflow every weekday at 9am. Turn it on to try it.",
			CronExpr:    "0 9 * * 1-5",
			Timezone:    "Local",
			JobType:     "workflow",
			WorkflowID:  &wfID,
			Enabled:     0,
		})
		if err != nil {
			log.Printf("Warning: failed to seed demo job: %v", err)
			return
		}
		log.Printf("Seeded example job: morning-motivation (weekdays 9am, switched off)")
	}
}

// CreateWorkflow creates a new workflow definition.
func (s *WorkflowStore) CreateWorkflow(ctx context.Context, w *Workflow) (*Workflow, error) {
	now := nowUTC()
	w.CreatedAt = now
	w.UpdatedAt = now

	if w.BaseBranch == "" {
		w.BaseBranch = "main"
	}
	if w.MaxDurationS == 0 {
		w.MaxDurationS = 3600
	}
	if w.StepsJSON == "" {
		w.StepsJSON = "[]"
	}

	result, err := s.db.ExecContext(ctx,
		`INSERT INTO workflows
		 (name, description, steps_json, default_agent_json, repo_path,
		  base_branch, max_duration_s, cleanup_worktree, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.Name, w.Description, w.StepsJSON, w.DefaultAgentJSON, w.RepoPath,
		w.BaseBranch, w.MaxDurationS, w.CleanupWorktree, w.Enabled, now, now)
	if err != nil {
		return nil, err
	}
	w.ID, _ = result.LastInsertId()
	w.HydrateSteps()
	return w, nil
}

// GetWorkflow returns a workflow by ID.
func (s *WorkflowStore) GetWorkflow(ctx context.Context, id int64) (*Workflow, error) {
	var w Workflow
	err := s.db.GetContext(ctx, &w, "SELECT * FROM workflows WHERE id = ?", id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	w.HydrateSteps()
	return &w, nil
}

// GetWorkflowByName returns a workflow by its unique name.
func (s *WorkflowStore) GetWorkflowByName(ctx context.Context, name string) (*Workflow, error) {
	var w Workflow
	err := s.db.GetContext(ctx, &w, "SELECT * FROM workflows WHERE name = ?", name)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	w.HydrateSteps()
	return &w, nil
}

// workflowWithLastRun is used internally to scan the ListWorkflows JOIN query.
type workflowWithLastRun struct {
	Workflow
	LastRunID          *int64  `db:"lr_id"`
	LastRunStatus      *string `db:"lr_status"`
	LastRunTriggerType *string `db:"lr_trigger_type"`
	LastRunStartedAt   *string `db:"lr_started_at"`
	LastRunFinishedAt  *string `db:"lr_finished_at"`
}

// ListWorkflows returns all workflows with last_run summary in a single query.
func (s *WorkflowStore) ListWorkflows(ctx context.Context) ([]Workflow, error) {
	var rows []workflowWithLastRun
	err := s.db.SelectContext(ctx, &rows,
		`SELECT w.*,
		        lr.id AS lr_id, lr.status AS lr_status,
		        lr.trigger_type AS lr_trigger_type,
		        lr.started_at AS lr_started_at, lr.finished_at AS lr_finished_at
		 FROM workflows w
		 LEFT JOIN (
		     SELECT workflow_id, id, status, trigger_type, started_at, finished_at
		     FROM workflow_runs wr1
		     WHERE wr1.id = (
		         SELECT id FROM workflow_runs wr2
		         WHERE wr2.workflow_id = wr1.workflow_id
		         ORDER BY created_at DESC, id DESC LIMIT 1
		     )
		 ) lr ON lr.workflow_id = w.id
		 ORDER BY w.name`)
	if err != nil {
		return nil, err
	}

	workflows := make([]Workflow, len(rows))
	for i, row := range rows {
		workflows[i] = row.Workflow
		workflows[i].HydrateSteps()
		if row.LastRunID != nil {
			workflows[i].LastRun = &WorkflowRunBrief{
				ID:          *row.LastRunID,
				Status:      ptr.Value(row.LastRunStatus),
				TriggerType: ptr.Value(row.LastRunTriggerType),
				StartedAt:   row.LastRunStartedAt,
				FinishedAt:  row.LastRunFinishedAt,
			}
		}
	}

	return workflows, nil
}

// UpdateWorkflow updates allowed fields on a workflow.
func (s *WorkflowStore) UpdateWorkflow(ctx context.Context, id int64, fields map[string]interface{}) (*Workflow, error) {
	err := dynamicUpdate(ctx, s.db, "workflows", id, fields, map[string]bool{
		"name": true, "description": true, "steps_json": true,
		"default_agent_json": true, "repo_path": true, "base_branch": true,
		"max_duration_s": true, "cleanup_worktree": true, "enabled": true,
	}, map[string]bool{
		"cleanup_worktree": true, "enabled": true,
	}, true)
	if err != nil {
		return nil, err
	}
	return s.GetWorkflow(ctx, id)
}

// DeleteWorkflow deletes a workflow (cascades to runs via FK).
func (s *WorkflowStore) DeleteWorkflow(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM workflows WHERE id = ?", id)
	return err
}

// ── Workflow Runs ─────────────────────────────────────────────────────

// CreateWorkflowRun creates a new run record for a workflow.
func (s *WorkflowStore) CreateWorkflowRun(ctx context.Context, workflowID int64, triggerType string, triggerContext *string) (*WorkflowRun, error) {
	now := nowUTC()
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO workflow_runs
		 (workflow_id, trigger_type, trigger_context, status, current_step, step_results, created_at)
		 VALUES (?, ?, ?, 'pending', 0, '[]', ?)`,
		workflowID, triggerType, triggerContext, now)
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()
	return &WorkflowRun{
		ID:             id,
		WorkflowID:     workflowID,
		TriggerType:    triggerType,
		TriggerContext: triggerContext,
		Status:         "pending",
		CurrentStep:    0,
		StepResults:    "[]",
		CreatedAt:      now,
	}, nil
}

// GetWorkflowRun returns a run by ID with workflow name.
func (s *WorkflowStore) GetWorkflowRun(ctx context.Context, runID int64) (*WorkflowRun, error) {
	var run WorkflowRun
	err := s.db.GetContext(ctx, &run,
		`SELECT r.*, w.name as workflow_name
		 FROM workflow_runs r
		 JOIN workflows w ON w.id = r.workflow_id
		 WHERE r.id = ?`, runID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	run.HydrateStepResults()
	return &run, nil
}

// UpdateWorkflowRun updates allowed fields on a run.
func (s *WorkflowStore) UpdateWorkflowRun(ctx context.Context, runID int64, fields map[string]interface{}) error {
	return dynamicUpdate(ctx, s.db, "workflow_runs", runID, fields, map[string]bool{
		"status": true, "current_step": true, "step_results": true,
		"worktree_path": true, "started_at": true, "finished_at": true,
		"error_msg": true, "error_code": true, "agent_type": true,
	}, nil, false)
}

// ListRunsForWorkflow returns runs for a specific workflow with optional filtering.
func (s *WorkflowStore) ListRunsForWorkflow(ctx context.Context, workflowID int64, limit, offset int, status *string) ([]WorkflowRun, error) {
	var runs []WorkflowRun
	query := "SELECT * FROM workflow_runs WHERE workflow_id = ?"
	args := []interface{}{workflowID}
	if status != nil {
		query += " AND status = ?"
		args = append(args, *status)
	}
	query += " ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	err := s.db.SelectContext(ctx, &runs, query, args...)
	if err != nil {
		return nil, err
	}
	return runs, nil
}

// ListRecentRuns returns recent runs across all workflows with workflow name.
func (s *WorkflowStore) ListRecentRuns(ctx context.Context, limit, offset int, status *string) ([]WorkflowRun, error) {
	var runs []WorkflowRun
	query := `SELECT r.*, w.name as workflow_name
		 FROM workflow_runs r
		 JOIN workflows w ON w.id = r.workflow_id`
	args := []interface{}{}
	if status != nil {
		query += " WHERE r.status = ?"
		args = append(args, *status)
	}
	query += " ORDER BY r.created_at DESC, r.id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	err := s.db.SelectContext(ctx, &runs, query, args...)
	if err != nil {
		return nil, err
	}
	return runs, err
}

// WorkflowRunIDs returns the ids of every run of a workflow.
func (s *WorkflowStore) WorkflowRunIDs(ctx context.Context, workflowID int64) ([]int64, error) {
	var ids []int64
	err := s.db.SelectContext(ctx, &ids, "SELECT id FROM workflow_runs WHERE workflow_id = ? ORDER BY id", workflowID)
	return ids, err
}

// PruneWorkflowRuns deletes a workflow's runs older than its newest keep runs,
// except ones still pending or running, and returns the deleted ids so their
// run directories can be removed too.
func (s *WorkflowStore) PruneWorkflowRuns(ctx context.Context, workflowID int64, keep int) ([]int64, error) {
	const where = `workflow_id = ? AND status NOT IN ('pending', 'running')
		   AND id NOT IN (SELECT id FROM workflow_runs WHERE workflow_id = ? ORDER BY id DESC LIMIT ?)`
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var ids []int64
	if err := tx.SelectContext(ctx, &ids, "SELECT id FROM workflow_runs WHERE "+where+" ORDER BY id", workflowID, workflowID, keep); err != nil || len(ids) == 0 {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM workflow_runs WHERE "+where, workflowID, workflowID, keep); err != nil {
		return nil, err
	}
	return ids, tx.Commit()
}

// GetActiveRunsForWorkflow returns pending/running runs for a workflow.
func (s *WorkflowStore) GetActiveRunsForWorkflow(ctx context.Context, workflowID int64) ([]WorkflowRun, error) {
	var runs []WorkflowRun
	err := s.db.SelectContext(ctx, &runs,
		`SELECT * FROM workflow_runs
		 WHERE workflow_id = ? AND status IN ('pending', 'running')
		 ORDER BY created_at DESC, id DESC`, workflowID)
	return runs, err
}

// SetRunStatus is a convenience for updating just the status (and optionally error_msg) of a run.
// Timestamps are set conditionally to avoid overwriting on repeated calls.
func (s *WorkflowStore) SetRunStatus(ctx context.Context, runID int64, status string, errorMsg *string) error {
	now := nowUTC()

	switch status {
	case "running":
		// Only set started_at if not already set
		_, err := s.db.ExecContext(ctx,
			`UPDATE workflow_runs SET status = ?, started_at = COALESCE(started_at, ?), error_msg = COALESCE(?, error_msg)
			 WHERE id = ?`,
			status, now, errorMsg, runID)
		return err
	case "completed", "failed", "killed":
		// Only set finished_at if not already set
		_, err := s.db.ExecContext(ctx,
			`UPDATE workflow_runs SET status = ?, finished_at = COALESCE(finished_at, ?), error_msg = COALESCE(?, error_msg)
			 WHERE id = ?`,
			status, now, errorMsg, runID)
		return err
	default:
		fields := map[string]interface{}{"status": status}
		if errorMsg != nil {
			fields["error_msg"] = *errorMsg
		}
		return s.UpdateWorkflowRun(ctx, runID, fields)
	}
}

// Error codes of a run refused before it started because its agent CLI is signed out or did not answer the sign-in check (#5469).
const (
	ErrorCodeNotSignedIn        = "not_signed_in"
	ErrorCodeSignInCheckTimeout = "signin_check_timeout"
)

// IsSignInRefusalCode reports whether an error_code is one of those two: such a run says nothing about the job or the workflow.
func IsSignInRefusalCode(code string) bool {
	return code == ErrorCodeNotSignedIn || code == ErrorCodeSignInCheckTimeout
}

// SetRunSignInRefused fails a run that was refused because agentType's CLI is signed out or did not answer the sign-in check: status failed, the plain sentence in error_msg, the
// fixed code and the CLI named.
func (s *WorkflowStore) SetRunSignInRefused(ctx context.Context, runID int64, errorMsg, code, agentType string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE workflow_runs SET status = 'failed', finished_at = COALESCE(finished_at, ?), error_msg = ?, error_code = ?, agent_type = ? WHERE id = ?`,
		nowUTC(), errorMsg, code, agentType, runID)
	return err
}

// UpdateStepResults updates the step_results JSON and current_step for a run.
func (s *WorkflowStore) UpdateStepResults(ctx context.Context, runID int64, currentStep int, stepResults string) error {
	return s.UpdateWorkflowRun(ctx, runID, map[string]interface{}{
		"current_step": currentStep,
		"step_results": stepResults,
	})
}

// GetWorkflowRunDirect returns a run by ID without joining workflow name (for internal use).
func (s *WorkflowStore) GetWorkflowRunDirect(ctx context.Context, runID int64) (*WorkflowRun, error) {
	var run WorkflowRun
	err := s.db.GetContext(ctx, &run, "SELECT * FROM workflow_runs WHERE id = ?", runID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	run.HydrateStepResults()
	return &run, nil
}

// StepsJSONFromRaw marshals a slice of step definitions to JSON for storage.
func StepsJSONFromRaw(steps []json.RawMessage) (string, error) {
	b, err := json.Marshal(steps)
	if err != nil {
		return "", fmt.Errorf("marshal steps: %w", err)
	}
	return string(b), nil
}
