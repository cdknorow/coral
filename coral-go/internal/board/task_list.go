package board

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The board task list (#3269). A long-lived board has thousands of finished tasks, and
// the full Task carries its body, completion message and workflow (team instructions
// repeated per task): one board's full list was 17.6 MB, polled every 10 s by each open
// dashboard. The list endpoint now returns TaskListItem pages; the full task stays at the
// task-detail endpoint.

// Task list filters.
const (
	TaskListDefault   = ""          // every open task, plus the first page of done tasks
	TaskListOpen      = "open"      // open tasks only, paged by id
	TaskListCompleted = "completed" // done tasks only (completed or skipped), newest first
	TaskListAll       = "all"       // every task, paged by id, newest first
)

const (
	// TaskListDefaultLimit and TaskListMaxLimit bound a page.
	TaskListDefaultLimit = 50
	TaskListMaxLimit     = 200
)

// ErrBadTaskListQuery is returned for an unknown status filter, or a cursor this store
// did not issue (or issued for another filter): the caller's mistake, not the store's.
var ErrBadTaskListQuery = errors.New("invalid task list query")

// The open and done conditions split every status the board_tasks CHECK allows.
// The done condition is written out literally in the queries below so SQLite can use the
// partial index idx_board_tasks_done, which has the same WHERE clause.
const (
	openTaskStatusSQL = `status IN ('pending', 'in_progress', 'blocked', 'draft', 'review_pending')`
	doneTaskStatusSQL = `status IN ('completed', 'skipped')`
)

// TaskListItem is one row of the task list: what a list view shows, never the body,
// completion message or workflow text. Outcome and ReviewRequested are the two workflow
// facts a row displays.
type TaskListItem struct {
	ID              int64     `db:"id" json:"id"`
	Revision        int       `db:"revision" json:"revision"`
	BoardID         string    `db:"board_id" json:"board_id"`
	Title           string    `db:"title" json:"title"`
	BodyPreview     string    `db:"body_preview" json:"body_preview,omitempty"`
	Status          string    `db:"status" json:"status"`
	Priority        string    `db:"priority" json:"priority"`
	CreatedBy       string    `db:"created_by" json:"created_by"`
	AssignedTo      *string   `db:"assigned_to" json:"assigned_to"`
	CompletedBy     *string   `db:"completed_by" json:"completed_by,omitempty"`
	CreatedAt       string    `db:"created_at" json:"created_at"`
	ClaimedAt       *string   `db:"claimed_at" json:"claimed_at,omitempty"`
	CompletedAt     *string   `db:"completed_at" json:"completed_at,omitempty"`
	SessionID       *string   `db:"session_id" json:"session_id,omitempty"`
	GoalID          *int64    `db:"goal_id" json:"goal_id,omitempty"`
	CostUSD         *float64  `db:"cost_usd" json:"cost_usd,omitempty"`
	Outcome         string    `db:"outcome" json:"outcome,omitempty"`
	ReviewRequested bool      `db:"review_requested" json:"review_requested,omitempty"`
	BlockedBy       []TaskDep `db:"-" json:"blocked_by,omitempty"`
}

// TaskListPage is one page of the task list.
type TaskListPage struct {
	Tasks []TaskListItem `json:"tasks"`
	// NextCursor continues the paged part of the list (the done tasks, for the default
	// filter); empty when there is nothing more.
	NextCursor     string `json:"next_cursor"`
	TotalOpen      int    `json:"total_open"`
	TotalCompleted int    `json:"total_completed"`
}

// TaskListQuery selects a page. Limit is clamped to [1, TaskListMaxLimit]; 0 means the default.
type TaskListQuery struct {
	Status string
	Limit  int
	Cursor string
}

// taskListColumns selects a TaskListItem. body_preview is the first 200 characters (the
// row tooltip); the workflow JSON is read for its outcome and whether a review is pending.
const taskListColumns = `t.id, t.revision, t.board_id, t.title,
	COALESCE(SUBSTR(t.body, 1, 200), '') AS body_preview,
	t.status, t.priority, t.created_by, t.assigned_to, t.completed_by, t.created_at, t.claimed_at,
	t.completed_at, t.session_id, t.goal_id, t.cost_usd,
	COALESCE(json_extract(w.data, '$.outcome'), '') AS outcome,
	COALESCE(json_type(w.data, '$.completion_review') = 'object', 0) AS review_requested
	FROM board_tasks t LEFT JOIN task_workflows w ON w.task_id = t.id`

// ListTaskPage returns a page of the board's task list. Each page costs O(limit) rows:
// done tasks come newest first from idx_board_tasks_done, the other filters walk the
// primary key.
func (s *Store) ListTaskPage(ctx context.Context, project string, q TaskListQuery) (*TaskListPage, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = TaskListDefaultLimit
	}
	if limit > TaskListMaxLimit {
		limit = TaskListMaxLimit
	}
	page := &TaskListPage{Tasks: []TaskListItem{}}
	var err error
	switch q.Status {
	case TaskListDefault:
		// Every open task (the working set: small), then the newest done tasks.
		if q.Cursor != "" {
			return nil, fmt.Errorf("%w: a cursor continues status=%s", ErrBadTaskListQuery, TaskListCompleted)
		}
		var open []TaskListItem
		if err = s.reader().SelectContext(ctx, &open, `SELECT `+taskListColumns+`
			 WHERE t.board_id = ? AND t.`+openTaskStatusSQL+`
			 ORDER BY CASE t.priority WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 END, t.id ASC`,
			project); err != nil {
			return nil, err
		}
		done, next, err := s.doneTaskPage(ctx, project, "", limit)
		if err != nil {
			return nil, err
		}
		page.Tasks = append(append(page.Tasks, open...), done...)
		page.NextCursor = next
	case TaskListCompleted:
		page.Tasks, page.NextCursor, err = s.doneTaskPage(ctx, project, q.Cursor, limit)
	case TaskListOpen, TaskListAll:
		page.Tasks, page.NextCursor, err = s.idTaskPage(ctx, project, q.Status, q.Cursor, limit)
	default:
		return nil, fmt.Errorf("%w: unknown status %q (use open, completed or all)", ErrBadTaskListQuery, q.Status)
	}
	if err != nil {
		return nil, err
	}
	if err := s.loadListBlockedBy(ctx, page.Tasks); err != nil {
		return nil, err
	}
	if err := s.reader().QueryRowxContext(ctx, `SELECT
		 COALESCE(SUM(CASE WHEN `+openTaskStatusSQL+` THEN 1 ELSE 0 END), 0),
		 COALESCE(SUM(CASE WHEN `+doneTaskStatusSQL+` THEN 1 ELSE 0 END), 0)
		 FROM board_tasks WHERE board_id = ?`, project).Scan(&page.TotalOpen, &page.TotalCompleted); err != nil {
		return nil, err
	}
	return page, nil
}

// doneTaskPage pages the done tasks by (completed_at DESC, id DESC). A task finished while
// a client pages gets the newest completed_at, so it lands on page one and never shifts
// the pages after the cursor. Rows without completed_at (none are written today) sort last.
func (s *Store) doneTaskPage(ctx context.Context, project, cursor string, limit int) ([]TaskListItem, string, error) {
	where := `t.board_id = ? AND t.` + doneTaskStatusSQL
	args := []any{project}
	if cursor != "" {
		at, id, err := decodeTaskCursor(cursor, project, "d")
		if err != nil {
			return nil, "", err
		}
		if at == "" {
			where += ` AND t.completed_at IS NULL AND t.id < ?`
			args = append(args, id)
		} else {
			where += ` AND (t.completed_at < ? OR (t.completed_at = ? AND t.id < ?) OR t.completed_at IS NULL)`
			args = append(args, at, at, id)
		}
	}
	var rows []TaskListItem
	if err := s.reader().SelectContext(ctx, &rows, `SELECT `+taskListColumns+` WHERE `+where+`
		 ORDER BY t.completed_at DESC, t.id DESC LIMIT ?`, append(args, limit+1)...); err != nil {
		return nil, "", err
	}
	if len(rows) <= limit {
		return rows, "", nil
	}
	rows = rows[:limit]
	last := rows[limit-1]
	at := ""
	if last.CompletedAt != nil {
		at = *last.CompletedAt
	}
	return rows, encodeTaskCursor(project, "d", at, last.ID), nil
}

// idTaskPage pages open tasks oldest first, or every task newest first, by id.
func (s *Store) idTaskPage(ctx context.Context, project, status, cursor string, limit int) ([]TaskListItem, string, error) {
	kind, where, order, cmp := "a", `t.board_id = ?`, `t.id DESC`, `<`
	if status == TaskListOpen {
		kind, where, order, cmp = "o", `t.board_id = ? AND t.`+openTaskStatusSQL, `t.id ASC`, `>`
	}
	args := []any{project}
	if cursor != "" {
		_, id, err := decodeTaskCursor(cursor, project, kind)
		if err != nil {
			return nil, "", err
		}
		where += ` AND t.id ` + cmp + ` ?`
		args = append(args, id)
	}
	var rows []TaskListItem
	if err := s.reader().SelectContext(ctx, &rows, `SELECT `+taskListColumns+` WHERE `+where+`
		 ORDER BY `+order+` LIMIT ?`, append(args, limit+1)...); err != nil {
		return nil, "", err
	}
	if len(rows) <= limit {
		return rows, "", nil
	}
	rows = rows[:limit]
	return rows, encodeTaskCursor(project, kind, "", rows[limit-1].ID), nil
}

// loadListBlockedBy fills BlockedBy for list items, with the same batched loader as Task.
func (s *Store) loadListBlockedBy(ctx context.Context, items []TaskListItem) error {
	if len(items) == 0 {
		return nil
	}
	tasks := make([]Task, len(items))
	for i := range items {
		tasks[i] = Task{ID: items[i].ID, BoardID: items[i].BoardID, Status: items[i].Status}
	}
	s.loadBlockedBy(ctx, tasks)
	for i := range items {
		items[i].BlockedBy = tasks[i].BlockedBy
	}
	return nil
}

// A cursor is opaque to clients: base64url of "<kind>|<board>|<completed_at>|<id>". kind ties
// it to the filter that issued it, so a cursor from one filter is refused by another; board
// (#3342) ties it to the board, so a cursor from board A is refused on board B rather than
// offsetting B's pages. A cursor from before #3342 (no board) is refused too: the dashboard's
// next poll hands out a fresh one.
func encodeTaskCursor(project, kind, at string, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(kind + "|" + cursorBoardTag(project) + "|" + at + "|" + strconv.FormatInt(id, 10)))
}

func decodeTaskCursor(cursor, project, kind string) (string, int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", 0, fmt.Errorf("%w: bad cursor", ErrBadTaskListQuery)
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 4 || parts[0] != kind {
		return "", 0, fmt.Errorf("%w: bad cursor", ErrBadTaskListQuery)
	}
	if parts[1] != cursorBoardTag(project) {
		return "", 0, fmt.Errorf("%w: cursor is for another board", ErrBadTaskListQuery)
	}
	id, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || id <= 0 {
		return "", 0, fmt.Errorf("%w: bad cursor", ErrBadTaskListQuery)
	}
	return parts[2], id, nil
}

// cursorBoardTag names the board in a cursor: a short hash, so any board name (one with a '|'
// included) fits the format and the cursor does not spell the name out.
func cursorBoardTag(project string) string {
	sum := sha256.Sum256([]byte(project))
	return hex.EncodeToString(sum[:8])
}
