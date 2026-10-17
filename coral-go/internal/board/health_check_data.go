package board

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// What the orchestrator's check reads from the board (#4175 step 2).

// HealthProgress is what moved on a board since a time: tasks completed (reviews not counted),
// reviews done, and agents' posts (not Coral's own lines).
type HealthProgress struct {
	Completed int `db:"completed"`
	Reviewed  int `db:"reviewed"`
	Posts     int `db:"posts"`
}

// ProgressSince counts what moved on project since since.
func (s *Store) ProgressSince(ctx context.Context, project string, since time.Time) (HealthProgress, error) {
	var p HealthProgress
	at := since.UTC().Format(time.RFC3339)
	err := s.reader().GetContext(ctx, &p, `SELECT
		(SELECT COUNT(*) FROM board_tasks t LEFT JOIN task_workflows w ON w.task_id = t.id
		 WHERE t.board_id = ? AND t.status = 'completed' AND t.completed_at > ? AND COALESCE(json_extract(w.data, '$.review_of'), 0) = 0) AS completed,
		(SELECT COUNT(*) FROM board_tasks t JOIN task_workflows w ON w.task_id = t.id
		 WHERE t.board_id = ? AND t.status = 'completed' AND t.completed_at > ? AND COALESCE(json_extract(w.data, '$.review_of'), 0) > 0) AS reviewed,
		(SELECT COUNT(*) FROM board_messages
		 WHERE project = ? AND created_at > ? AND COALESCE(is_system, 0) = 0) AS posts`,
		project, at, project, at, project, at)
	return p, err
}

// OpenReviews returns, per reviewer, the ids of their open review tasks (pending or in
// progress), oldest first.
func (s *Store) OpenReviews(ctx context.Context, project string) (map[string][]int64, error) {
	var rows []struct {
		ID       int64  `db:"id"`
		Assignee string `db:"assigned_to"`
	}
	if err := s.reader().SelectContext(ctx, &rows, `SELECT t.id, t.assigned_to FROM board_tasks t JOIN task_workflows w ON w.task_id = t.id
		WHERE t.board_id = ? AND t.status IN ('pending', 'in_progress') AND t.assigned_to IS NOT NULL
		  AND COALESCE(json_extract(w.data, '$.review_of'), 0) > 0 ORDER BY t.id`, project); err != nil {
		return nil, err
	}
	out := map[string][]int64{}
	for _, r := range rows {
		out[r.Assignee] = append(out[r.Assignee], r.ID)
	}
	return out, nil
}

// Reviewers returns who has done or been given a review on project: the team's reviewers.
func (s *Store) Reviewers(ctx context.Context, project string) (map[string]bool, error) {
	var names []string
	if err := s.reader().SelectContext(ctx, &names, `SELECT DISTINCT t.assigned_to FROM board_tasks t JOIN task_workflows w ON w.task_id = t.id
		WHERE t.board_id = ? AND t.assigned_to IS NOT NULL AND COALESCE(json_extract(w.data, '$.review_of'), 0) > 0`, project); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, n := range names {
		out[n] = true
	}
	return out, nil
}

// TaskSnoozedUntil is the end of a task's reminder snooze, zero when none.
func (s *Store) TaskSnoozedUntil(ctx context.Context, project string, id int64) (time.Time, error) {
	var v sql.NullString
	if err := s.reader().GetContext(ctx, &v, `SELECT idle_snoozed_until FROM board_tasks WHERE id = ? AND board_id = ?`, id, project); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	if !v.Valid {
		return time.Time{}, nil
	}
	t, _ := time.Parse(time.RFC3339, v.String)
	return t, nil
}

// LastPostBy is a member's latest board post on project (its content and time), "" when none.
func (s *Store) LastPostBy(ctx context.Context, project, subscriberID string) (string, time.Time, error) {
	var row struct {
		Content   string `db:"content"`
		CreatedAt string `db:"created_at"`
	}
	err := s.reader().GetContext(ctx, &row, `SELECT content, created_at FROM board_messages
		WHERE project = ? AND subscriber_id = ? AND COALESCE(is_system, 0) = 0 ORDER BY id DESC LIMIT 1`, project, subscriberID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, nil
	}
	if err != nil {
		return "", time.Time{}, err
	}
	at, _ := time.Parse(time.RFC3339, row.CreatedAt)
	return row.Content, at, nil
}
