// Task dependencies: blockers, cycles, depth and what a result unblocks (moved out of store.go, #4998; no behaviour change).

package board

import (
	"context"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
)

// StallDownstreamTasks marks pending descendants of a terminal prerequisite as
// blocked. Ownership is preserved so the orchestrator can rewire or retry the
// work; blocked rows are never claimable.
func (s *Store) StallDownstreamTasks(ctx context.Context, project string, upstreamID int64) ([]Task, error) {
	queue := []int64{upstreamID}
	seen := map[int64]bool{upstreamID: true}
	var stalled []Task
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		var downstream []struct {
			ID        int64  `db:"task_id"`
			Condition string `db:"condition"`
		}
		if err := s.db.SelectContext(ctx, &downstream, `SELECT DISTINCT d.task_id,
			COALESCE(r.condition, 'success') AS condition FROM task_dependencies d
			JOIN board_tasks t ON t.id = d.task_id
			LEFT JOIN task_dependency_rules r ON r.task_id = d.task_id AND r.upstream_id = d.blocked_by_task_id
			WHERE d.blocked_by_task_id = ? AND d.blocked_by_board_id = ?`, id, project); err != nil {
			return nil, err
		}
		for _, child := range downstream {
			childID := child.ID
			if !seen[childID] {
				seen[childID] = true
				queue = append(queue, childID)
			}
			if child.Condition == "termination" {
				continue
			}
			var status string
			if err := s.db.GetContext(ctx, &status, "SELECT status FROM board_tasks WHERE id = ? AND board_id = ?", childID, project); err != nil {
				continue
			}
			if status != "pending" {
				continue
			}
			if _, err := s.db.ExecContext(ctx, "UPDATE board_tasks SET status = 'blocked', edited_at = ? WHERE id = ? AND board_id = ? AND status = 'pending'", nowUTC(), childID, project); err != nil {
				return nil, err
			}
			if task, err := s.getTaskByID(ctx, project, childID); err == nil {
				stalled = append(stalled, *task)
			}
		}
	}
	return stalled, nil
}

// GetTaskDependencies returns the blocked_by deps for a task with title/status populated.
func (s *Store) GetTaskDependencies(ctx context.Context, taskID int64) ([]TaskDep, error) {
	var deps []struct {
		BlockedByTaskID  int64   `db:"blocked_by_task_id"`
		BlockedByBoardID string  `db:"blocked_by_board_id"`
		Title            *string `db:"title"`
		Status           *string `db:"status"`
	}
	err := s.reader().SelectContext(ctx, &deps,
		`SELECT td.blocked_by_task_id, td.blocked_by_board_id, bt.title, bt.status
		 FROM task_dependencies td
		 LEFT JOIN board_tasks bt ON bt.id = td.blocked_by_task_id
		 WHERE td.task_id = ?`, taskID)
	if err != nil {
		return nil, err
	}
	result := make([]TaskDep, len(deps))
	for i, d := range deps {
		result[i] = TaskDep{
			TaskID:  d.BlockedByTaskID,
			BoardID: d.BlockedByBoardID,
		}
		if d.Title != nil {
			result[i].Title = *d.Title
		}
		if d.Status != nil {
			result[i].Status = *d.Status
		}
		if err := s.loadDependencyRule(ctx, taskID, &result[i]); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// ListDownstreamTasks returns tasks that directly depend on an upstream task.
// It is used for cancellation diagnostics and does not change readiness.
func (s *Store) ListDownstreamTasks(ctx context.Context, project string, upstreamID int64) ([]Task, error) {
	var rows []struct {
		ID      int64  `db:"id"`
		BoardID string `db:"board_id"`
	}
	if err := s.reader().SelectContext(ctx, &rows, `SELECT DISTINCT b.id, b.board_id
		FROM task_dependencies d JOIN board_tasks b ON b.id = d.task_id
		WHERE d.blocked_by_task_id = ? AND d.blocked_by_board_id = ?`, upstreamID, project); err != nil {
		return nil, err
	}
	result := make([]Task, 0, len(rows))
	for _, row := range rows {
		task, err := s.getTaskByID(ctx, row.BoardID, row.ID)
		if err != nil {
			return nil, err
		}
		if task != nil {
			result = append(result, *task)
		}
	}
	return result, nil
}

// AddTaskDependencies inserts dependency rows and sets status to 'blocked'
// if any blockers are unresolved. Validates circular deps and depth limit.
func (s *Store) AddTaskDependencies(ctx context.Context, project string, taskID int64, deps []TaskDep, maxDepth int) error {
	if err := checkDependenciesChangeable(ctx, s.db, project, taskID); err != nil {
		return err
	}
	if err := normalizeTaskDeps(project, deps); err != nil {
		return err
	}
	if err := s.checkBlockers(ctx, deps); err != nil {
		return err
	}
	if err := s.validateNoCycles(ctx, taskID, deps); err != nil {
		return err
	}
	if err := s.ValidateDependencyDepth(ctx, taskID, deps, maxDepth); err != nil {
		return err
	}

	// Replace dependencies and their conditions atomically.
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A claim may have raced the validation before this transaction.
	if err := checkDependenciesChangeable(ctx, tx, project, taskID); err != nil {
		return err
	}
	if err := validateTaskCycles(ctx, tx, taskID, deps); err != nil {
		return err
	}
	if err := replaceTaskDependencies(ctx, tx, project, taskID, deps); err != nil {
		return err
	}
	return tx.Commit()
}

// checkDependenciesChangeable refuses a task that has started (or finished): its dependencies are fixed by then.
func checkDependenciesChangeable(ctx context.Context, db sqlx.QueryerContext, project string, taskID int64) error {
	var taskStatus string
	if err := sqlx.GetContext(ctx, db, &taskStatus, "SELECT status FROM board_tasks WHERE id = ? AND board_id = ?", taskID, project); err != nil {
		return err
	}
	if taskStatus == "review_pending" || taskStatus == "in_progress" || taskStatus == "completed" || taskStatus == "skipped" {
		return fmt.Errorf("dependencies can only change before a task starts")
	}
	return nil
}

// normalizeTaskDeps checks each dependency (no duplicates, a known condition, valid artifact names) and fills in, in place,
// the defaults: the success condition and the task's own board.
func normalizeTaskDeps(project string, deps []TaskDep) error {
	seen := map[int64]bool{}
	for i := range deps {
		if seen[deps[i].TaskID] {
			return fmt.Errorf("duplicate dependency #%d", deps[i].TaskID)
		}
		seen[deps[i].TaskID] = true
		if deps[i].Condition == "" {
			deps[i].Condition = "success"
		}
		switch deps[i].Condition {
		case "success", "failure", "termination":
		default:
			return fmt.Errorf("dependency condition must be success, failure, or termination")
		}
		if err := validNames(deps[i].RequiredArtifacts); err != nil {
			return err
		}
		if deps[i].BoardID == "" {
			deps[i].BoardID = project
		}
	}
	return nil
}

// checkBlockers refuses a blocker that isn't on its board, or one asked for an artifact its output contract doesn't
// promise.
func (s *Store) checkBlockers(ctx context.Context, deps []TaskDep) error {
	for _, d := range deps {
		var exists int
		err := s.db.GetContext(ctx, &exists,
			"SELECT COUNT(*) FROM board_tasks WHERE id = ? AND board_id = ?", d.TaskID, d.BoardID)
		if err != nil || exists == 0 {
			return fmt.Errorf("blocker task #%d not found on board %s", d.TaskID, d.BoardID)
		}
		if err := s.checkRequiredArtifactsDeclared(ctx, d); err != nil {
			return err
		}
	}
	return nil
}

// checkRequiredArtifactsDeclared: when the producer has declared an output contract, reject a consumer
// dependency that asks for a name the producer never promised to publish.
// Older tasks with no declared outputs remain compatible and are checked
// against their immutable completion artifacts at readiness time.
func (s *Store) checkRequiredArtifactsDeclared(ctx context.Context, d TaskDep) error {
	if len(d.RequiredArtifacts) == 0 {
		return nil
	}
	upstream, err := loadWorkflow(ctx, s.db, d.TaskID)
	if err != nil {
		return err
	}
	if len(upstream.RequiredOutputs) == 0 {
		return nil
	}
	declared := make(map[string]bool, len(upstream.RequiredOutputs))
	for _, name := range upstream.RequiredOutputs {
		declared[name] = true
	}
	for _, name := range d.RequiredArtifacts {
		if !declared[name] {
			return fmt.Errorf("dependency #%d cannot require artifact %q: upstream declares outputs [%s]", d.TaskID, name, strings.Join(upstream.RequiredOutputs, ", "))
		}
	}
	return nil
}

// replaceTaskDependencies swaps the task's dependencies and their rules for deps, then sets it pending when every
// blocker already satisfies its rule, else blocked (only from pending or blocked).
func replaceTaskDependencies(ctx context.Context, tx *sqlx.Tx, project string, taskID int64, deps []TaskDep) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM task_dependency_rules WHERE task_id = ?", taskID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM task_dependencies WHERE task_id = ?", taskID); err != nil {
		return err
	}
	for _, d := range deps {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO task_dependencies (task_id, blocked_by_task_id, blocked_by_board_id) VALUES (?, ?, ?)",
			taskID, d.TaskID, d.BoardID); err != nil {
			return err
		}
		if err := saveDependencyRule(ctx, tx, taskID, d); err != nil {
			return err
		}
	}

	_, ready, err := dependencyInputs(ctx, tx, taskID)
	if err != nil {
		return err
	}
	status := "blocked"
	if ready {
		status = "pending"
	}
	_, err = tx.ExecContext(ctx, "UPDATE board_tasks SET edited_at = CASE WHEN status <> ? THEN ? ELSE edited_at END, status = ? WHERE id = ? AND board_id = ? AND status IN ('pending', 'blocked')", status, nowUTC(), status, taskID, project) // only a real change of status is an edit: a new task is not
	return err
}

func (s *Store) validateNoCycles(ctx context.Context, taskID int64, deps []TaskDep) error {
	return validateTaskCycles(ctx, s.db, taskID, deps)
}

func validateTaskCycles(ctx context.Context, db sqlx.QueryerContext, taskID int64, deps []TaskDep) error {
	visited := map[int64]bool{taskID: true}
	var walk func(id int64) error
	walk = func(id int64) error {
		var upstreamDeps []struct {
			BlockedByTaskID int64 `db:"blocked_by_task_id"`
		}
		if err := sqlx.SelectContext(ctx, db, &upstreamDeps,
			"SELECT blocked_by_task_id FROM task_dependencies WHERE task_id = ?", id); err != nil {
			return err
		}
		for _, u := range upstreamDeps {
			if visited[u.BlockedByTaskID] {
				return fmt.Errorf("circular dependency detected: task #%d appears in its own dependency chain", taskID)
			}
			visited[u.BlockedByTaskID] = true
			if err := walk(u.BlockedByTaskID); err != nil {
				return err
			}
			delete(visited, u.BlockedByTaskID)
		}
		return nil
	}

	for _, d := range deps {
		if d.TaskID == taskID {
			return fmt.Errorf("task cannot depend on itself")
		}
		visited[d.TaskID] = true
		if err := walk(d.TaskID); err != nil {
			return err
		}
		delete(visited, d.TaskID)
	}
	return nil
}

// ResolveDownstreamTasks consumes readiness notifications for completedTaskID.
// Readiness itself is committed atomically with completion or cancellation.
// A zero ID drains recovered notifications from all boards after startup.
func (s *Store) ResolveDownstreamTasks(ctx context.Context, project string, completedTaskID int64) ([]Task, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var rows []struct {
		ID      int64  `db:"id"`
		BoardID string `db:"board_id"`
		Status  string `db:"status"`
	}
	if err := tx.SelectContext(ctx, &rows, `SELECT b.id, b.board_id, b.status
	 FROM task_ready_notifications n JOIN board_tasks b ON b.id = n.task_id
	 WHERE ? = 0 OR EXISTS (SELECT 1 FROM task_dependencies d WHERE d.task_id = b.id AND d.blocked_by_task_id = ?)`, completedTaskID, completedTaskID); err != nil {
		return nil, err
	}
	for _, row := range rows {
		if _, err := tx.ExecContext(ctx, "DELETE FROM task_ready_notifications WHERE task_id = ?", row.ID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	var unblocked []Task
	for _, row := range rows {
		if row.Status != "pending" {
			continue
		}
		t, err := s.getTaskByID(ctx, row.BoardID, row.ID)
		if err != nil {
			return nil, err
		}
		if t.Status == "pending" {
			unblocked = append(unblocked, *t)
		}
	}
	return unblocked, nil
}

// ReblockDownstreamTasks checks all tasks that depend on regressedTaskID.
// If the task is pending and has an unresolved blocker, sets it back to blocked.
func (s *Store) ReblockDownstreamTasks(ctx context.Context, project string, regressedTaskID int64) ([]Task, error) {
	var downstreamIDs []int64
	if err := s.db.SelectContext(ctx, &downstreamIDs,
		"SELECT DISTINCT task_id FROM task_dependencies WHERE blocked_by_task_id = ?", regressedTaskID); err != nil {
		return nil, err
	}

	var reblocked []Task
	for _, tid := range downstreamIDs {
		// Check if this task has any unresolved blockers
		_, ready, err := dependencyInputs(ctx, s.db, tid)
		if err != nil {
			continue
		}
		if ready {
			continue
		}

		// Has unresolved blockers — set back to blocked if currently pending. By id alone: a
		// downstream task may be on another board, and tid comes from the dependency table.
		result, err := s.db.ExecContext(ctx,
			"UPDATE board_tasks SET status = 'blocked', edited_at = ? WHERE id = ? AND status = 'pending'", nowUTC(), tid)
		if err != nil {
			continue
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			continue
		}

		var boardID string
		if err := s.db.GetContext(ctx, &boardID,
			"SELECT board_id FROM board_tasks WHERE id = ?", tid); err != nil {
			continue
		}
		if t, err := s.getTaskByID(ctx, boardID, tid); err == nil {
			reblocked = append(reblocked, *t)
		}
	}
	return reblocked, nil
}

// ValidateDependencyDepth walks the dependency graph and returns an error
// if adding the proposed deps would exceed maxDepth.
func (s *Store) ValidateDependencyDepth(ctx context.Context, taskID int64, deps []TaskDep, maxDepth int) error {
	if maxDepth <= 0 {
		return nil
	}

	// Compute the max upstream depth from each proposed blocker
	var maxUpstream func(id int64, depth int, visited map[int64]bool) (int, error)
	maxUpstream = func(id int64, depth int, visited map[int64]bool) (int, error) {
		if visited[id] {
			return depth, nil
		}
		visited[id] = true

		var upstreamIDs []int64
		if err := s.reader().SelectContext(ctx, &upstreamIDs,
			"SELECT blocked_by_task_id FROM task_dependencies WHERE task_id = ?", id); err != nil {
			return depth, err
		}
		best := depth
		for _, uid := range upstreamIDs {
			d, err := maxUpstream(uid, depth+1, visited)
			if err != nil {
				return 0, err
			}
			if d > best {
				best = d
			}
		}
		return best, nil
	}

	for _, d := range deps {
		visited := map[int64]bool{taskID: true}
		depth, err := maxUpstream(d.TaskID, 1, visited)
		if err != nil {
			return err
		}
		if depth > maxDepth {
			return fmt.Errorf("dependency chain would exceed maximum depth of %d", maxDepth)
		}
	}
	return nil
}
