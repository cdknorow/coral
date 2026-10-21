package board

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jmoiron/sqlx"
)

// These instructions are persisted at creation, so historical tasks retain the
// contract their agents actually received even if defaults change later.
const DefaultTaskWorkflowInstructions = `Use your judgment to accomplish the task, considering its requirements and upstream results. Use task detail for context and dependency results. Complete with --message for results and --outcome failed for unsuccessful work. When outputs are required, use --artifacts manifest.json with inline content or durable links; upload shared files with coral-agent artifact upload <file>.`

type TaskArtifact struct {
	Name         string `json:"name"`
	URI          string `json:"uri,omitempty"`
	Content      string `json:"content,omitempty"`
	MediaType    string `json:"media_type,omitempty"`
	Revision     string `json:"revision,omitempty"`
	Digest       string `json:"digest,omitempty"`
	Kind         string `json:"kind,omitempty"`
	Command      string `json:"command,omitempty"`
	Runner       string `json:"runner,omitempty"`
	OutputDigest string `json:"output_digest,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	FinishedAt   string `json:"finished_at,omitempty"`
	Branch       string `json:"branch,omitempty"`
	Remote       string `json:"remote,omitempty"`
	Landed       bool   `json:"landed,omitempty"`
	// Seq and AttachedBy are the server's (#5689): the order rows were recorded in on a task, and who recorded each. A value a
	// client sends is replaced (stampArtifacts), so a patch row's author cannot be forged.
	Seq        int    `json:"seq,omitempty"`
	AttachedBy string `json:"attached_by,omitempty"`
	// LandedBy and LandedAt: who marked the patch landed, and when (mark-landed, #5689). Server-set like Seq; the queue reads these, never the
	// client-writable Landed flag.
	LandedBy string `json:"landed_by,omitempty"`
	LandedAt string `json:"landed_at,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	// Provenance is how Coral checked a managed link (coral://artifacts/<id>) when the result was
	// recorded (#4379): "uploaded", "person", "unverified", or "" (legacy rows, other entries). Set
	// by the server only; a value the client sends is cleared.
	Provenance string `json:"provenance,omitempty"`
}

const maxTaskArtifacts = 32

type TaskInput struct {
	TaskID    int64          `json:"task_id"`
	BoardID   string         `json:"board_id"`
	Outcome   string         `json:"outcome"`
	Artifacts []TaskArtifact `json:"artifacts"`
}

type TaskWorkflow struct {
	CompletionReview      *CompletionReview      `json:"completion_review,omitempty"`
	TeamMode              *WorkingMode           `json:"team_mode,omitempty"`
	Instructions          string                 `json:"instructions"`
	Name                  string                 `json:"name,omitempty"`
	Stage                 string                 `json:"stage,omitempty"`
	ParentTaskID          int64                  `json:"parent_task_id,omitempty"`
	RetryOf               int64                  `json:"retry_of,omitempty"`
	RequiredOutputs       []string               `json:"required_outputs,omitempty"`
	Inputs                []TaskInput            `json:"inputs,omitempty"`
	Artifacts             []TaskArtifact         `json:"artifacts,omitempty"`
	Outcome               string                 `json:"outcome,omitempty"`
	Amendments            []TaskAmendment        `json:"amendments,omitempty"`
	EffectiveInstructions string                 `json:"effective_instructions,omitempty"`
	AcknowledgedRevision  int                    `json:"acknowledged_revision,omitempty"`
	CompletionGates       []CompletionGate       `json:"completion_gates,omitempty"`
	GateResults           []CompletionGateResult `json:"gate_results,omitempty"`
	CandidateRevision     string                 `json:"candidate_revision,omitempty"`
	// The review flow (#3725): who did the work (a task in review is assigned to the
	// orchestrator, then to reviewers), the parent of a review task, and the review rounds.
	OriginalAuthor string        `json:"original_author,omitempty"`
	ReviewOf       int64         `json:"review_of,omitempty"`
	ReviewRound    int           `json:"review_round,omitempty"` // a review task: the round it is for (#3805)
	ReviewRounds   []ReviewRound `json:"review_rounds,omitempty"`
	// Reviewers named when the task was made (task add --reviewers, #4175): who Coral delegates
	// its review to when it enters review, if the task's creator was the orchestrator or the
	// Operator (AutoReviewers).
	Reviewers []string `json:"reviewers,omitempty"`
	// How the creator's and each amendment's names were backed (#4175, Security's caveat on
	// #4176 rule 13): "token" (an agent token whose session holds that name on this board),
	// "operator" (the Operator, with no agent token) or "claimed" (only the name the request
	// gave). Set by the server only; recorded now, for when identity is strict.
	CreatedIdentity string            `json:"created_identity,omitempty"`
	AmendIdentity   map[string]string `json:"amend_identity,omitempty"` // by revision
}

func (s *Store) initTaskWorkflows(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS board_working_modes (board_id TEXT PRIMARY KEY, data TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS board_workflow_presets (board_id TEXT NOT NULL, id TEXT NOT NULL, name TEXT NOT NULL, instructions TEXT NOT NULL, PRIMARY KEY(board_id,id));
 CREATE TABLE IF NOT EXISTS task_workflows (
	 task_id INTEGER PRIMARY KEY REFERENCES board_tasks(id), data TEXT NOT NULL);
	 CREATE TABLE IF NOT EXISTS task_dependency_rules (
	 task_id INTEGER NOT NULL, upstream_id INTEGER NOT NULL,
	 condition TEXT NOT NULL, required_artifacts TEXT NOT NULL,
	 PRIMARY KEY(task_id, upstream_id));
	 CREATE TABLE IF NOT EXISTS task_ready_notifications (
	 task_id INTEGER PRIMARY KEY REFERENCES board_tasks(id));`)
	return err
}

// resolveReadyTasks runs inside the same transaction as prerequisite completion.
// The notification marker lets the existing asynchronous notifier discover the
// transition without being responsible for making the task claimable.
func resolveReadyTasks(ctx context.Context, tx *sqlx.Tx, ids []int64) error {
	for _, id := range ids {
		_, ready, err := dependencyInputs(ctx, tx, id)
		if err != nil {
			return err
		}
		if !ready {
			continue
		}
		// By id alone: a dependent task may be on another board, and ids come from the dependency table.
		res, err := tx.ExecContext(ctx, "UPDATE board_tasks SET status = 'pending' WHERE id = ? AND status = 'blocked'", id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n > 0 {
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO task_ready_notifications(task_id) VALUES (?)", id); err != nil {
				return err
			}
		}
	}
	return nil
}

func resolveCompletedTask(ctx context.Context, tx *sqlx.Tx, id int64) error {
	var ids []int64
	if err := tx.SelectContext(ctx, &ids, "SELECT DISTINCT task_id FROM task_dependencies WHERE blocked_by_task_id = ?", id); err != nil {
		return err
	}
	return resolveReadyTasks(ctx, tx, ids)
}

// Repair tasks left blocked by older versions that resolved readiness only in
// an asynchronous callback. Keep drafts and unmet conditions unchanged.
func (s *Store) recoverTaskReadiness(ctx context.Context) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := rewireRetryDependents(ctx, tx); err != nil {
		return err
	}
	var ids []int64
	if err := tx.SelectContext(ctx, &ids, "SELECT id FROM board_tasks WHERE status = 'blocked'"); err != nil {
		return err
	}
	if err := resolveReadyTasks(ctx, tx, ids); err != nil {
		return err
	}
	return tx.Commit()
}

// rewireRetryDependents makes retry_of actionable: unstarted tasks that still
// depend on the superseded result follow the replacement task instead. This
// also repairs databases created before retry rewiring was automatic.
func rewireRetryDependents(ctx context.Context, tx *sqlx.Tx) error {
	var retries []struct {
		OldID int64 `db:"old_id"`
		NewID int64 `db:"new_id"`
	}
	if err := tx.SelectContext(ctx, &retries, `SELECT json_extract(w.data, '$.retry_of') AS old_id, w.task_id AS new_id
		FROM task_workflows w JOIN board_tasks t ON t.id=w.task_id
		WHERE CAST(json_extract(w.data, '$.retry_of') AS INTEGER) > 0`); err != nil {
		return err
	}
	for _, retry := range retries {
		var downstream []struct {
			TaskID    int64  `db:"task_id"`
			Status    string `db:"status"`
			BoardID   string `db:"board_id"`
			Condition string `db:"condition"`
		}
		if err := tx.SelectContext(ctx, &downstream, `SELECT DISTINCT d.task_id, t.status, t.board_id,
			COALESCE(r.condition, 'success') AS condition
			FROM task_dependencies d JOIN board_tasks t ON t.id=d.task_id
			LEFT JOIN task_dependency_rules r ON r.task_id=d.task_id AND r.upstream_id=d.blocked_by_task_id
			WHERE d.blocked_by_task_id=? AND t.status IN ('pending','blocked','draft')`, retry.OldID); err != nil {
			return err
		}
		for _, dependent := range downstream {
			// Failure and termination branches intentionally consume the old
			// result; only success consumers follow a replacement retry.
			if dependent.Condition != "success" {
				continue
			}
			var already int
			if err := tx.GetContext(ctx, &already, `SELECT COUNT(*) FROM task_dependencies WHERE task_id=? AND blocked_by_task_id=?`, dependent.TaskID, retry.NewID); err != nil {
				return err
			}
			if already > 0 {
				if _, err := tx.ExecContext(ctx, `DELETE FROM task_dependency_rules WHERE task_id=? AND upstream_id=?`, dependent.TaskID, retry.OldID); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM task_dependencies WHERE task_id=? AND blocked_by_task_id=?`, dependent.TaskID, retry.OldID); err != nil {
					return err
				}
			} else {
				if _, err := tx.ExecContext(ctx, `UPDATE task_dependencies SET blocked_by_task_id=? WHERE task_id=? AND blocked_by_task_id=?`, retry.NewID, dependent.TaskID, retry.OldID); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE task_dependency_rules SET upstream_id=? WHERE task_id=? AND upstream_id=?`, retry.NewID, dependent.TaskID, retry.OldID); err != nil {
					return err
				}
			}
			_, ready, err := dependencyInputs(ctx, tx, dependent.TaskID)
			if err != nil {
				return err
			}
			if dependent.Status == "draft" {
				continue
			}
			status := "blocked"
			if ready {
				status = "pending"
			}
			if _, err := tx.ExecContext(ctx, `UPDATE board_tasks SET status=? WHERE id=? AND status IN ('pending','blocked')`, status, dependent.TaskID); err != nil {
				return err
			}
			if ready {
				if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO task_ready_notifications(task_id) VALUES (?)`, dependent.TaskID); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// RewireRetryDependents repairs unstarted dependents after a retry is created.
func (s *Store) RewireRetryDependents(ctx context.Context) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := rewireRetryDependents(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func loadWorkflow(ctx context.Context, db sqlx.QueryerContext, id int64) (TaskWorkflow, error) {
	var data string
	err := sqlx.GetContext(ctx, db, &data, "SELECT data FROM task_workflows WHERE task_id = ?", id)
	if err == sql.ErrNoRows {
		return decodeWorkflow("", false)
	}
	if err != nil {
		return TaskWorkflow{Instructions: DefaultTaskWorkflowInstructions}, err
	}
	return decodeWorkflow(data, true)
}

// decodeWorkflow turns a task_workflows.data value into a TaskWorkflow; a task
// without a row gets the default instructions.
func decodeWorkflow(data string, found bool) (TaskWorkflow, error) {
	w := TaskWorkflow{Instructions: DefaultTaskWorkflowInstructions}
	if !found {
		return w, nil
	}
	err := json.Unmarshal([]byte(data), &w)
	return w, err
}

func saveWorkflow(ctx context.Context, db sqlx.ExecerContext, id int64, w TaskWorkflow) error {
	data, err := json.Marshal(w)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO task_workflows(task_id, data) VALUES (?, ?)
	 ON CONFLICT(task_id) DO UPDATE SET data = excluded.data`, id, string(data))
	return err
}

func validNames(names []string) error {
	if len(names) > maxTaskArtifacts {
		return fmt.Errorf("at most %d artifact names allowed", maxTaskArtifacts)
	}
	seen := map[string]bool{}
	for _, name := range names {
		if strings.TrimSpace(name) == "" || len(name) > 128 || seen[name] {
			return fmt.Errorf("artifact names must be nonempty, unique, and at most 128 characters")
		}
		seen[name] = true
	}
	return nil
}

// dependencyRuleRow is what a TaskDep's rule evaluation needs about one
// upstream task, as seen from one downstream task.
type dependencyRuleRow struct {
	Condition string `db:"condition"`
	Required  string `db:"required_artifacts"`
	Status    string `db:"status"`
	Data      string `db:"data"`
}

func (s *Store) loadDependencyRule(ctx context.Context, id int64, d *TaskDep) error {
	var row dependencyRuleRow
	err := s.reader().GetContext(ctx, &row, `SELECT COALESCE(r.condition,'success') AS condition, COALESCE(r.required_artifacts,'[]') AS required_artifacts, b.status, COALESCE(w.data,'{}') AS data
 FROM board_tasks b LEFT JOIN task_dependency_rules r ON r.upstream_id=b.id AND r.task_id=?
 LEFT JOIN task_workflows w ON w.task_id=b.id WHERE b.id=? AND b.board_id=?`, id, d.TaskID, d.BoardID)
	if err == sql.ErrNoRows {
		return applyDependencyRule(d, nil)
	}
	if err != nil {
		return err
	}
	return applyDependencyRule(d, &row)
}

// applyDependencyRule evaluates d against its upstream row; nil means the
// upstream task does not exist on the dependency's board.
func applyDependencyRule(d *TaskDep, row *dependencyRuleRow) error {
	if row == nil {
		d.Satisfied = false
		d.BlockedReason = "upstream task not found"
		return nil
	}
	d.Condition = row.Condition
	if err := json.Unmarshal([]byte(row.Required), &d.RequiredArtifacts); err != nil {
		return err
	}
	var workflow TaskWorkflow
	if err := json.Unmarshal([]byte(row.Data), &workflow); err != nil {
		return err
	}
	d.Outcome, d.MissingArtifacts, d.BlockedReason = dependencyStatus(row.Status, workflow, d.Condition, d.RequiredArtifacts)
	d.Satisfied = d.BlockedReason == ""
	return nil
}

func saveDependencyRule(ctx context.Context, db sqlx.ExecerContext, id int64, d TaskDep) error {
	data, err := json.Marshal(d.RequiredArtifacts)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "INSERT INTO task_dependency_rules(task_id, upstream_id, condition, required_artifacts) VALUES (?, ?, ?, ?)", id, d.TaskID, d.Condition, string(data))
	return err
}

func (s *Store) prepareWorkflow(ctx context.Context, project string, w TaskWorkflow) (TaskWorkflow, error) {
	// Inputs and results are assigned by Coral, never by a create request.
	w.Inputs, w.Artifacts, w.Outcome = nil, nil, ""
	w.TeamMode = nil
	w.CompletionReview = nil
	custom := strings.TrimSpace(w.Instructions)
	w.Instructions = DefaultTaskWorkflowInstructions
	if custom != "" {
		w.Instructions += "\n\nWorkflow-specific instructions:\n" + custom
	}
	if err := validNames(w.RequiredOutputs); err != nil {
		return w, err
	}
	if err := validateCompletionGateConfiguration(w.CompletionGates, s.completionChecksEnabled); err != nil {
		return w, err
	}
	for _, id := range []int64{w.ParentTaskID, w.RetryOf} {
		if id == 0 {
			continue
		}
		var status string
		if err := s.reader().GetContext(ctx, &status, "SELECT status FROM board_tasks WHERE id = ? AND board_id = ?", id, project); err != nil {
			return w, fmt.Errorf("related task #%d not found on this board", id)
		}
		if id == w.RetryOf && status != "completed" && status != "skipped" {
			return w, fmt.Errorf("retry_of must reference a finished task")
		}
	}
	return w, nil
}

func (s *Store) hydrateWorkflow(ctx context.Context, t *Task) error {
	w, err := loadWorkflow(ctx, s.reader(), t.ID)
	if err != nil {
		return err
	}
	w.Amendments, err = s.loadTaskAmendments(ctx, t.ID)
	if err != nil {
		return err
	}
	finishWorkflow(t, w)
	if t.Status == "pending" {
		inputs, _, err := dependencyInputs(ctx, s.reader(), t.ID)
		if err != nil {
			return err
		}
		t.Workflow.Inputs = inputs
	}
	return nil
}

// finishWorkflow derives the workflow fields computed at read time (effective
// instructions, implied outcome) and attaches w to t.
func finishWorkflow(t *Task, w TaskWorkflow) {
	w.EffectiveInstructions = w.Instructions
	for _, amendment := range w.Amendments {
		if value, ok := amendment.Changes["workflow_instructions"].(string); ok && strings.TrimSpace(value) != "" {
			w.EffectiveInstructions += "\n\nTask amendment (revision " + fmt.Sprint(amendment.Revision) + "):\n" + value
		}
	}
	if w.Outcome == "" {
		if t.Status == "completed" {
			w.Outcome = "success"
		}
		if t.Status == "skipped" {
			w.Outcome = "cancelled"
		}
	}
	t.Workflow = w
	t.Verification = verificationOf(t)
}

// dependencyInputRow is one upstream of a downstream task, for dependencyInputs.
type dependencyInputRow struct {
	TaskID    int64  `db:"task_id"`
	ID        int64  `db:"id"`
	Board     string `db:"board_id"`
	Status    string `db:"status"`
	Condition string `db:"condition"`
	Required  string `db:"required_artifacts"`
}

func dependencyInputs(ctx context.Context, db sqlx.QueryerContext, taskID int64) ([]TaskInput, bool, error) {
	var rows []dependencyInputRow
	err := sqlx.SelectContext(ctx, db, &rows, `SELECT d.task_id, b.id, b.board_id, b.status,
	 COALESCE(r.condition, 'success') AS condition, COALESCE(r.required_artifacts, '[]') AS required_artifacts
	 FROM task_dependencies d JOIN board_tasks b ON b.id = d.blocked_by_task_id AND b.board_id = d.blocked_by_board_id
	 LEFT JOIN task_dependency_rules r ON r.task_id = d.task_id AND r.upstream_id = b.id
	 WHERE d.task_id = ? ORDER BY b.id`, taskID)
	if err != nil {
		return nil, false, err
	}
	return buildDependencyInputs(rows, func(id int64) (TaskWorkflow, error) { return loadWorkflow(ctx, db, id) })
}

// buildDependencyInputs resolves one task's upstream rows (ordered by upstream
// id) into inputs; false means some upstream does not satisfy its rule yet.
func buildDependencyInputs(rows []dependencyInputRow, workflow func(id int64) (TaskWorkflow, error)) ([]TaskInput, bool, error) {
	var inputs []TaskInput
	for _, row := range rows {
		w, err := workflow(row.ID)
		if err != nil {
			return nil, false, err
		}
		var required []string
		if err := json.Unmarshal([]byte(row.Required), &required); err != nil {
			return nil, false, err
		}
		outcome, _, reason := dependencyStatus(row.Status, w, row.Condition, required)
		if reason != "" {
			return nil, false, nil
		}
		inputs = append(inputs, TaskInput{TaskID: row.ID, BoardID: row.Board, Outcome: outcome, Artifacts: w.Artifacts})
	}
	return inputs, true, nil
}

// CompleteTaskWithArtifacts commits the outcome and evidence atomically. A
// terminal task cannot be edited or reassigned, so downstream inputs stay pinned.
func (s *Store) CompleteTaskWithArtifacts(ctx context.Context, project string, taskID int64, subscriberID string, message *string, outcome string, artifacts []TaskArtifact) (*Task, error) {
	return s.CompleteTaskWithArtifactsAtRevisionAndCandidate(ctx, project, taskID, subscriberID, message, outcome, artifacts, nil, "")
}

// CompleteTaskWithArtifactsAtRevisionAndCandidate records the exact submitted candidate
// revision and enforces all orchestrator-declared completion gates; a non-nil expectedRevision
// rejects stale work after an amendment.
func (s *Store) CompleteTaskWithArtifactsAtRevisionAndCandidate(ctx context.Context, project string, taskID int64, subscriberID string, message *string, outcome string, artifacts []TaskArtifact, expectedRevision *int, candidateRevision string) (*Task, error) {
	clearStamps(artifacts) // the server's fields (Seq, AttachedBy) are never a client's (#5689)
	return s.completeTaskWithArtifacts(ctx, project, taskID, subscriberID, message, outcome, artifacts, expectedRevision, candidateRevision, false)
}

// CompleteApprovedReview completes a task whose review every reviewer approved, from the candidate the board itself kept on the task:
// its rows keep their revisions (the same name more than once, each with its own Seq), which a client's rows may not.
func (s *Store) CompleteApprovedReview(ctx context.Context, project string, taskID int64, subscriberID string, expectedRevision *int) (*Task, error) {
	w, err := loadWorkflow(ctx, s.db, taskID)
	if err != nil {
		return nil, err
	}
	cr := w.CompletionReview
	if cr == nil {
		return nil, fmt.Errorf("task #%d has no candidate to complete", taskID)
	}
	msg := cr.Message
	return s.completeTaskWithArtifacts(ctx, project, taskID, subscriberID, &msg, cr.ProposedOutcome, cr.Artifacts, expectedRevision, cr.CandidateRevision, true)
}

func (s *Store) completeTaskWithArtifacts(ctx context.Context, project string, taskID int64, subscriberID string, message *string, outcome string, artifacts []TaskArtifact, expectedRevision *int, candidateRevision string, kept bool) (*Task, error) {
	if outcome == "" {
		outcome = "success"
	}
	if outcome != "success" && outcome != "failed" {
		return nil, fmt.Errorf("outcome must be success or failed")
	}
	names, err := validateArtifactRows(artifacts, kept)
	if err != nil {
		return nil, err
	}
	reviewerErr := s.RequireTaskReviewer(ctx, project, subscriberID)
	// On a team with no orchestrator nobody can review a candidate: its author completes it
	// (#3725, submit-review there completes the task).
	_, orchErr := s.TeamOrchestrator(ctx, project)
	noOrchestrator := errors.Is(orchErr, ErrNoOrchestrator)
	// validate is run twice: before the completion gates (no transaction, so
	// slow registered checks never hold the board database) and again inside
	// the write transaction. See completion_snapshot.go.
	validate := func(q sqlx.QueryerContext) (TaskWorkflow, error) {
		w, err := loadWorkflow(ctx, q, taskID)
		if err != nil {
			return w, err
		}
		var revision int
		if err := sqlx.GetContext(ctx, q, &revision, "SELECT revision FROM board_tasks WHERE id=? AND board_id=?", taskID, project); err != nil {
			return w, err
		}
		if revision > 1 && (expectedRevision == nil || *expectedRevision != revision) {
			return w, fmt.Errorf("task #%d has revision %d; reread task detail before completing, then retry with --revision %d (API: expected_revision=%d). Reading task detail does not set this value automatically. --candidate-revision identifies the source/build revision and does not replace --revision", taskID, revision, revision, revision)
		}
		if w.CompletionReview != nil && reviewerErr != nil && !noOrchestrator {
			return w, reviewerErr
		}
		// An explicit assignment is exclusive for completion too: another worker
		// must not be able to finish a task that is assigned to someone else, for
		// instance a pending task they were never allowed to claim. The assignee,
		// the Operator identity and an active registered orchestrator may complete.
		var assignee sql.NullString
		if err := sqlx.GetContext(ctx, q, &assignee, "SELECT assigned_to FROM board_tasks WHERE id=? AND board_id=?", taskID, project); err != nil {
			return w, err
		}
		if assignee.Valid && assignee.String != "" && assignee.String != subscriberID && subscriberID != "Operator" && reviewerErr != nil {
			return w, fmt.Errorf("task #%d is assigned to %s; only the assignee, Operator or an active registered orchestrator may complete it", taskID, assignee.String)
		}
		if outcome == "success" {
			for _, required := range w.RequiredOutputs {
				found := false
				for _, name := range names {
					if name == required {
						found = true
					}
				}
				if !found {
					return w, fmt.Errorf("required output %q is missing", required)
				}
			}
		}
		return w, nil
	}
	w, snapshot, err := readForGates(ctx, s.db, project, taskID, validate)
	if err != nil {
		return nil, err
	}
	gateResults, gateErr := checkCompletionGatesWithPolicy(w.CompletionGates, artifacts, candidateRevision, s.completionCheckRunner, s.completionChecksEnabled)
	if gateErr != nil && outcome == "success" {
		return nil, gateErr
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if w, err = validate(tx); err != nil {
		return nil, err
	}
	if err := requireUnchanged(ctx, tx, project, taskID, snapshot); err != nil {
		return nil, err
	}
	inputs, ready, err := dependencyInputs(ctx, tx, taskID)
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, fmt.Errorf("task dependencies are not satisfied")
	}
	res, err := tx.ExecContext(ctx, `UPDATE board_tasks SET status = 'completed', completed_by = ?, completion_message = ?, completed_at = ?
	 WHERE id = ? AND board_id = ? AND status IN ('pending', 'in_progress', 'review_pending')`, subscriberID, message, nowUTC(), taskID, project)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return nil, fmt.Errorf("task #%d cannot be completed (already finished, blocked, or not found)", taskID)
	}
	w.Outcome, w.Artifacts, w.Inputs = outcome, artifacts, inputs
	w.CandidateRevision, w.GateResults = candidateRevision, gateResults
	if err := saveWorkflow(ctx, tx, taskID, w); err != nil {
		return nil, err
	}
	if err := resolveCompletedTask(ctx, tx, taskID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.computeAndStoreTaskCost(ctx, project, taskID)
	return s.getTaskByID(ctx, project, taskID)
}

func isLocalArtifactPath(uri string) bool {
	u := strings.TrimSpace(uri)
	return strings.HasPrefix(u, "/") || strings.HasPrefix(u, "~/") ||
		strings.HasPrefix(u, "./") || strings.HasPrefix(u, "../") ||
		strings.HasPrefix(strings.ToLower(u), "file://")
}

// ValidLabel reports whether s is a label a client may set on something the dashboard shows and agents are told about (an artifact's
// name, media type, kind...): at most maxBytes bytes of valid UTF-8 with no control character (C0, DEL, C1, a line or paragraph separator)
// and no format character (a direction override, a zero-width mark). Empty is fine: whether a field is required is its own rule.
func ValidLabel(s string, maxBytes int) bool {
	if len(s) > maxBytes || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if badIdentityRune(r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}

// validCommand is an evidence command line: one line (a tab is fine), no other control character.
func validCommand(s string, maxBytes int) bool {
	if len(s) > maxBytes || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if (r < 0x20 && r != '\t') || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}

func validateTaskArtifacts(artifacts []TaskArtifact) ([]string, error) {
	return validateArtifactRows(artifacts, false)
}

// validateArtifactRows checks the rows of a candidate. revisions is true only for the rows the server itself kept on a review
// (CompleteApprovedReview): there a name may repeat across revisions (a later Seq) and the cap counts the newest row of each name.
// A client's rows never get that (clearStamps at every entry): the names are unique and every row counts.
func validateArtifactRows(artifacts []TaskArtifact, revisions bool) ([]string, error) {
	counted := len(artifacts)
	if revisions {
		counted = len(newestByName(artifacts))
	}
	if counted > maxTaskArtifacts {
		return nil, fmt.Errorf("at most %d artifacts allowed", maxTaskArtifacts)
	}
	names := []string{}
	unique := []string{} // a row attached later (Seq > 0, #5689) is a newer revision of a name: it may repeat one
	for _, a := range artifacts {
		names = append(names, a.Name)
		if !revisions || a.Seq == 0 {
			unique = append(unique, a.Name)
		} else if strings.TrimSpace(a.Name) == "" || len(a.Name) > 128 {
			return nil, fmt.Errorf("artifact names must be nonempty, unique, and at most 128 characters")
		}
		if strings.TrimSpace(a.URI) == "" && strings.TrimSpace(a.Content) == "" {
			return nil, fmt.Errorf("artifact %q requires uri or content", a.Name)
		}
		if isLocalArtifactPath(a.URI) {
			return nil, fmt.Errorf("artifact %q uses a local filesystem path; provide inline content or a durable URI", a.Name)
		}
		if len(a.Content) > 65536 || len(a.URI) > 4096 {
			return nil, fmt.Errorf("artifact %q exceeds size limit; use a durable URI for large files", a.Name)
		}
		if len(a.Kind) > 64 || len(a.Command) > 4096 || len(a.Runner) > 256 || len(a.OutputDigest) > 256 || len(a.StartedAt) > 64 || len(a.FinishedAt) > 64 || len(a.Branch) > 256 || len(a.Remote) > 256 {
			return nil, fmt.Errorf("artifact %q evidence metadata exceeds size limits", a.Name)
		}
		// What the Team Artifacts panel, the Inbox and a cross-team message show of an artifact is what its writer sent: bounded, and plain
		// text (#4901). The name, media type, revision and digest had no check beyond the name's length.
		for _, f := range []struct {
			what, v string
			max     int
		}{{"name", a.Name, 128}, {"media_type", a.MediaType, 127}, {"revision", a.Revision, 256}, {"digest", a.Digest, 256}, {"kind", a.Kind, 64},
			{"runner", a.Runner, 256}, {"output_digest", a.OutputDigest, 256}, {"started_at", a.StartedAt, 64}, {"finished_at", a.FinishedAt, 64},
			{"branch", a.Branch, 256}, {"remote", a.Remote, 256}} {
			if !ValidLabel(f.v, f.max) {
				return nil, fmt.Errorf("artifact %q: %s must be plain text of at most %d bytes (no control or formatting characters)", a.Name, f.what, f.max)
			}
		}
		if !validCommand(a.Command, 4096) {
			return nil, fmt.Errorf("artifact %q: command must be one line of plain text of at most 4096 bytes", a.Name)
		}
	}
	if err := validNames(unique); err != nil {
		return nil, err
	}
	return names, nil
}

// RecordAmendIdentity records how the name behind amendment revision of task id was backed.
func (s *Store) RecordAmendIdentity(ctx context.Context, project string, id int64, revision int, identity string) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.GetContext(ctx, &n, `SELECT COUNT(*) FROM board_tasks WHERE id = ? AND board_id = ?`, id, project); err != nil || n == 0 {
		return err
	}
	w, err := loadWorkflow(ctx, tx, id)
	if err != nil {
		return err
	}
	if w.AmendIdentity == nil {
		w.AmendIdentity = map[string]string{}
	}
	w.AmendIdentity[fmt.Sprint(revision)] = identity
	if err := saveWorkflow(ctx, tx, id, w); err != nil {
		return err
	}
	return tx.Commit()
}
