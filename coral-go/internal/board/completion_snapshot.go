package board

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// Completion gates can run registered checks (go test, git, ...) for up to
// two minutes. The board database has a single connection, so running them
// inside the write transaction stalled every board read and post for that
// long. Completion and review submission therefore validate and read the
// task first, run the gates with no transaction open, and then validate again
// inside a short transaction, which also confirms the task is unchanged since
// the gates were evaluated (taskSnapshot).

// taskSnapshot is what completion gates were evaluated against: the task's
// revision and its stored workflow (gates, required outputs, review state).
type taskSnapshot struct {
	revision int
	workflow string // raw task_workflows.data; "" when the task has none
	found    bool
}

func loadTaskSnapshot(ctx context.Context, q sqlx.QueryerContext, project string, id int64) (taskSnapshot, error) {
	var snap taskSnapshot
	if err := sqlx.GetContext(ctx, q, &snap.revision, "SELECT revision FROM board_tasks WHERE id=? AND board_id=?", id, project); err != nil {
		return snap, err
	}
	err := sqlx.GetContext(ctx, q, &snap.workflow, "SELECT data FROM task_workflows WHERE task_id = ?", id)
	switch {
	case err == sql.ErrNoRows:
		return snap, nil
	case err != nil:
		return snap, err
	}
	snap.found = true
	return snap, nil
}

// requireUnchanged rejects a write whose gate results were computed against
// an older version of the task.
func requireUnchanged(ctx context.Context, tx *sqlx.Tx, project string, id int64, before taskSnapshot) error {
	now, err := loadTaskSnapshot(ctx, tx, project, id)
	if err != nil {
		return err
	}
	if now != before {
		return fmt.Errorf("task #%d changed while its completion checks were running; reread task detail and retry", id)
	}
	return nil
}

// readForGates validates and snapshots the task in one short read transaction,
// so the gates are evaluated against exactly the state that is later compared
// by requireUnchanged. The transaction ends before the gates run.
func readForGates(ctx context.Context, db *sqlx.DB, project string, id int64, validate func(sqlx.QueryerContext) (TaskWorkflow, error)) (TaskWorkflow, taskSnapshot, error) {
	tx, err := db.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TaskWorkflow{}, taskSnapshot{}, err
	}
	defer tx.Rollback()
	w, err := validate(tx)
	if err != nil {
		return w, taskSnapshot{}, err
	}
	snap, err := loadTaskSnapshot(ctx, tx, project, id)
	return w, snap, err
}
