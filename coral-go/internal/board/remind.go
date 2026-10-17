package board

import (
	"context"
	"time"
)

// A task's one-time reminder (#4175 step 3: task add --remind-at). A time named in a task (a soak
// end, a release gate) becomes a reminder at that time: Coral reminds the task's assignee (or the
// team's orchestrator when nobody is assigned) once, through the typing gate. A parked or finished
// task is not reminded; a parked one is when it comes back, if its time has passed by then.

// SetTaskRemindAt sets (or, with a zero at, clears) a task's reminder time.
func (s *Store) SetTaskRemindAt(ctx context.Context, project string, id int64, at time.Time) error {
	var v any
	if !at.IsZero() {
		v = at.UTC().Format(time.RFC3339)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE board_tasks SET remind_at = ?, reminded_at = NULL WHERE board_id = ? AND id = ?`, v, project, id)
	return err
}

// DueTaskReminders lists the open, unparked tasks on every board whose reminder time has come and
// who have not been reminded yet.
func (s *Store) DueTaskReminders(ctx context.Context, now time.Time) ([]Task, error) {
	var tasks []Task
	err := s.reader().SelectContext(ctx, &tasks, `SELECT id, board_id, title, status, priority, created_by, assigned_to, created_at, remind_at
		FROM board_tasks
		WHERE remind_at IS NOT NULL AND reminded_at IS NULL AND remind_at <= ?
		  AND status IN ('pending', 'in_progress', 'blocked', 'review_pending')
		  AND NOT `+parkedSQL+`
		ORDER BY remind_at, id`, now.UTC().Format(time.RFC3339))
	return tasks, err
}

// MarkTaskReminded records that a task's reminder went out; false when another pass already did.
func (s *Store) MarkTaskReminded(ctx context.Context, project string, id int64, now time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE board_tasks SET reminded_at = ? WHERE board_id = ? AND id = ? AND reminded_at IS NULL`,
		now.UTC().Format(time.RFC3339), project, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
