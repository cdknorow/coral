package board

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// TaskAmendment is an immutable, planner-authored change to task instructions.
type TaskAmendment struct {
	ID                int64                  `db:"id" json:"id"`
	Revision          int                    `db:"revision" json:"revision"`
	Actor             string                 `db:"actor" json:"actor"`
	CreatedAt         string                 `db:"created_at" json:"created_at"`
	Reason            string                 `db:"reason" json:"reason"`
	Changes           map[string]interface{} `db:"-" json:"changes"`
	PreviousSnapshot  map[string]interface{} `db:"-" json:"previous_snapshot"`
	EffectiveSnapshot map[string]interface{} `db:"-" json:"effective_snapshot"`
	ChangesJSON       string                 `db:"changes_json" json:"-"`
	PreviousJSON      string                 `db:"previous_snapshot_json" json:"-"`
	EffectiveJSON     string                 `db:"effective_snapshot_json" json:"-"`
}

// AmendTask applies the v1 body/instruction amendment contract atomically.
// Authorization is enforced by the HTTP layer using the registered planner
// identity; the store enforces status, revision, and patch invariants.
func (s *Store) AmendTask(ctx context.Context, project string, taskID int64, actor string, baseRevision int, reason string, changes map[string]interface{}) (*Task, error) {
	if baseRevision <= 0 {
		return nil, fmt.Errorf("base_revision must be positive")
	}
	if strings.TrimSpace(reason) == "" || len(reason) > 4096 {
		return nil, fmt.Errorf("amendment reason is required (maximum 4096 characters)")
	}
	if len(changes) == 0 {
		return nil, fmt.Errorf("amendment changes are required")
	}
	for key := range changes {
		if key != "body" && key != "workflow_instructions" {
			return nil, fmt.Errorf("amendment field %q is unsupported in v1; dependencies, outputs, owner, and working mode use existing mechanisms", key)
		}
	}
	if value, ok := changes["body"]; ok {
		if body, ok := value.(string); !ok || len(body) > 65536 {
			return nil, fmt.Errorf("body must be a string of at most 65536 bytes")
		}
	}
	if value, ok := changes["workflow_instructions"]; ok {
		if instructions, ok := value.(string); !ok || len(instructions) > 4096 {
			return nil, fmt.Errorf("workflow_instructions must be a string of at most 4096 bytes")
		}
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var row struct {
		Revision int     `db:"revision"`
		Status   string  `db:"status"`
		Title    string  `db:"title"`
		Body     *string `db:"body"`
	}
	if err := tx.GetContext(ctx, &row, "SELECT revision,status,title,body FROM board_tasks WHERE id=? AND board_id=?", taskID, project); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("task #%d not found", taskID)
		}
		return nil, err
	}
	if row.Status == "completed" || row.Status == "skipped" || row.Status == "review_pending" {
		return nil, fmt.Errorf("task #%d cannot be amended (status: %s)", taskID, row.Status)
	}
	if row.Revision != baseRevision {
		return nil, fmt.Errorf("task #%d has revision %d; reread task detail before amending", taskID, row.Revision)
	}
	previous := map[string]interface{}{"title": row.Title, "body": "", "revision": row.Revision}
	if row.Body != nil {
		previous["body"] = *row.Body
	}
	effective := map[string]interface{}{"title": row.Title, "body": previous["body"], "revision": row.Revision + 1}
	if value, ok := changes["body"]; ok {
		effective["body"] = value
	}
	previousJSON, _ := json.Marshal(previous)
	effectiveJSON, _ := json.Marshal(effective)
	changesJSON, _ := json.Marshal(changes)
	created := nowUTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_amendments(task_id,revision,actor,created_at,reason,changes_json,previous_snapshot_json,effective_snapshot_json) VALUES(?,?,?,?,?,?,?,?)`, taskID, row.Revision+1, actor, created, reason, string(changesJSON), string(previousJSON), string(effectiveJSON)); err != nil {
		return nil, err
	}
	var body interface{}
	if value, ok := changes["body"]; ok {
		body = value
	} else if row.Body != nil {
		body = *row.Body
	}
	if _, err := tx.ExecContext(ctx, "UPDATE board_tasks SET body=?, revision=? WHERE id=? AND board_id=? AND revision=?", body, row.Revision+1, taskID, project, baseRevision); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.getTaskByID(ctx, project, taskID)
}

func (s *Store) loadTaskAmendments(ctx context.Context, taskID int64) ([]TaskAmendment, error) {
	var rows []TaskAmendment
	if err := s.db.SelectContext(ctx, &rows, "SELECT id,revision,actor,created_at,reason,changes_json,previous_snapshot_json,effective_snapshot_json FROM task_amendments WHERE task_id=? ORDER BY revision", taskID); err != nil {
		return nil, err
	}
	for i := range rows {
		if err := json.Unmarshal([]byte(rows[i].ChangesJSON), &rows[i].Changes); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(rows[i].PreviousJSON), &rows[i].PreviousSnapshot); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(rows[i].EffectiveJSON), &rows[i].EffectiveSnapshot); err != nil {
			return nil, err
		}
	}
	return rows, nil
}
