package board

import (
	"context"
	"database/sql"
	"errors"
)

// The per-board "require a goal" setting (#7116): off by default; when on, a task is created with a goal and a task is never moved to none. The rule lives in the store, in the two places a task's goal
// is set (CreateTaskWithOpts, SetTaskGoal), so every writer is covered, not only the HTTP handler. It governs new tasks and moves only: tasks that already have no goal stay as they are.
const settingRequireGoal = "require_goal"

// ErrGoalRequired is the refusal of a task with no goal on a board that requires one (HTTP 400, code goal_required).
var ErrGoalRequired = &GoalError{"goal_required", "this board requires a goal for every task: create or pick one with coral-board goal"}

// requiresGoal reports whether the board requires a goal for every task. A setting that cannot be read is an error, never "off": the check fails closed.
func (s *Store) requiresGoal(ctx context.Context, project string) (bool, error) {
	var v string
	err := s.db.GetContext(ctx, &v, `SELECT value FROM board_settings WHERE project = ? AND key = ?`, project, settingRequireGoal)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return v == "true", nil
}
