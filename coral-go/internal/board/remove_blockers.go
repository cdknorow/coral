package board

import (
	"context"
	"fmt"
)

// RemoveTaskBlockers removes only the selected dependency (zero means all),
// preserving other dependency rules and calculating readiness in one transaction.
func (s *Store) RemoveTaskBlockers(ctx context.Context, project string, id, blocker int64) (*Task, string, error) {
	if blocker < 0 {
		return nil, "", fmt.Errorf("blocker ID must be positive")
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	var status string
	if err := tx.GetContext(ctx, &status, "SELECT status FROM board_tasks WHERE id=? AND board_id=?", id, project); err != nil {
		return nil, "", err
	}
	if status != "pending" && status != "blocked" && status != "draft" {
		return nil, "", fmt.Errorf("dependencies can only change before a task starts (status: %s)", status)
	}
	if blocker > 0 {
		result, err := tx.ExecContext(ctx, "DELETE FROM task_dependencies WHERE task_id=? AND blocked_by_task_id=?", id, blocker)
		if err != nil {
			return nil, "", err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return nil, "", err
		}
		if n == 0 {
			return nil, "", fmt.Errorf("task #%d is not blocked by #%d", id, blocker)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM task_dependency_rules WHERE task_id=? AND upstream_id=?", id, blocker); err != nil {
			return nil, "", err
		}
	} else {
		if _, err := tx.ExecContext(ctx, "DELETE FROM task_dependencies WHERE task_id=?", id); err != nil {
			return nil, "", err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM task_dependency_rules WHERE task_id=?", id); err != nil {
			return nil, "", err
		}
	}
	_, ready, err := dependencyInputs(ctx, tx, id)
	if err != nil {
		return nil, "", err
	}
	if status != "draft" {
		next := "blocked"
		if ready {
			next = "pending"
		}
		if _, err := tx.ExecContext(ctx, "UPDATE board_tasks SET status=? WHERE id=? AND board_id=?", next, id, project); err != nil {
			return nil, "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}
	task, err := s.getTaskByID(ctx, project, id)
	return task, status, err
}
