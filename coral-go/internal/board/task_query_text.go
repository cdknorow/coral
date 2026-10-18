package board

import (
	"context"
	"slices"
	"strings"
)

// Searching the task query's text (#4745): q is a plain, case-insensitive substring of a task's title, body or completion message.
// It is matched here, on the rows as the query's other filters leave them, and never by SQL (no LIKE pattern, no escaping to get
// wrong: % and _ are ordinary characters), and on what a viewer sees (TaskQuery.Shown) so that through Coral Hub the match tells a
// viewer nothing the redacted reply does not. The scan is bounded by rows and by text, so a hostile q or a huge board costs a bounded
// amount.

// TaskQueryMaxTextLen is the longest q.
const TaskQueryMaxTextLen = 200

// taskTextScanRows and taskTextScanBytes bound one search's scan: the rows read and the text of them (title, body, completion message).
// Past either the scan stops and the reply says Truncated. Variables so a test can lower them.
var (
	taskTextScanRows  = 50000
	taskTextScanBytes = 64 << 20
)

// parseQueryText reads q: at most one, trimmed, TaskQueryMaxTextLen characters at most, no control characters; lower-cased. An empty q
// is no search.
func parseQueryText(vals []string) (string, error) {
	v, ok, err := single("q", vals)
	if err != nil || !ok {
		return "", err
	}
	v = strings.TrimSpace(v)
	if len(v) > TaskQueryMaxTextLen || strings.ContainsAny(v, "\x00\r\n\t") {
		return "", taskQueryErr("q is at most %d characters, without control characters", TaskQueryMaxTextLen)
	}
	return strings.ToLower(v), nil
}

// taskTextMatches reports whether q.Text is in the title, the body or the completion message as a viewer sees them.
func taskTextMatches(q *TaskQuery, title, body, completion string) bool {
	for _, t := range [3]string{title, body, completion} {
		if t == "" {
			continue
		}
		if q.Shown != nil {
			t = q.Shown(t)
		}
		if strings.Contains(strings.ToLower(t), q.Text) {
			return true
		}
	}
	return false
}

// after reports whether a row with sort value val and id comes after the cursor in the query's order.
func (c *queryTaskCursor) after(val string, id int64, desc bool) bool {
	if desc {
		return val < c.Val || (val == c.Val && id < c.ID)
	}
	return val > c.Val || (val == c.Val && id > c.ID)
}

// queryTasksText answers a query that has a q: the rows the other filters leave, in the query's order, are read once (without the
// cursor: the total counts every match), each is tested, and the page is the first Limit matches after the cursor.
func (s *Store) queryTasksText(ctx context.Context, q *TaskQuery) (*TaskQueryResult, error) {
	sortExpr, sortName := taskQuerySort(q)
	cols, wantVerification := taskQueryColumnList(q.Fields, sortExpr)
	cols = append(cols, `title AS "_qt"`, `COALESCE(body, '') AS "_qb"`, `COALESCE(completion_message, '') AS "_qc"`)
	noCursor := *q
	noCursor.cursor = nil
	join, where, args := taskQueryWhere(&noCursor, sortExpr)
	dir := "DESC"
	if !q.Desc {
		dir = "ASC"
	}
	stmt := "SELECT " + strings.Join(cols, ", ") + " FROM board_tasks" + join
	if len(where) > 0 {
		stmt += " WHERE " + strings.Join(where, " AND ")
	}
	stmt += " ORDER BY " + sortExpr + " " + dir + ", id " + dir
	rows, err := s.reader().QueryxContext(ctx, stmt, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	page := &taskQueryPage{res: &TaskQueryResult{Tasks: []map[string]any{}}}
	scannedRows, scannedBytes := 0, 0
	for rows.Next() {
		m := map[string]any{}
		if err := rows.MapScan(m); err != nil {
			return nil, err
		}
		for k, v := range m {
			if b, ok := v.([]byte); ok {
				m[k] = string(b)
			}
		}
		title, _ := m["_qt"].(string)
		body, _ := m["_qb"].(string)
		completion, _ := m["_qc"].(string)
		delete(m, "_qt")
		delete(m, "_qb")
		delete(m, "_qc")
		if scannedRows++; scannedRows > taskTextScanRows || scannedBytes+len(title)+len(body)+len(completion) > taskTextScanBytes {
			page.res.Truncated = true
			break
		}
		scannedBytes += len(title) + len(body) + len(completion)
		if !taskTextMatches(q, title, body, completion) {
			continue
		}
		page.res.Total++
		id, _ := m["_id"].(int64)
		sortVal, _ := m["_sort"].(string)
		if q.cursor != nil && !q.cursor.after(sortVal, id, q.Desc) {
			continue
		}
		if len(page.res.Tasks) == q.Limit {
			page.more = true // one more than the page says there is another; the scan goes on to count
			continue
		}
		page.last = queryTaskCursor{Sort: sortName, Desc: q.Desc, Val: sortVal, ID: id}
		board, _ := m["_board"].(string)
		page.boards = append(page.boards, board)
		if wantVerification {
			t := Task{ID: id}
			t.Status, _ = m["_status"].(string)
			if c, ok := m["_completed"].(string); ok {
				t.CompletedAt = &c
			}
			page.vTasks = append(page.vTasks, t)
		}
		for _, hidden := range []string{"_id", "_board", "_sort", "_status", "_completed"} {
			delete(m, hidden)
		}
		page.res.Tasks = append(page.res.Tasks, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if slices.Contains(q.Fields, "board_label") {
		s.addBoardLabels(ctx, page.res.Tasks, page.boards)
	}
	if wantVerification {
		if err := s.addVerification(ctx, page.res.Tasks, page.vTasks); err != nil {
			return nil, err
		}
	}
	if page.more {
		page.res.NextCursor = page.last.encode()
	}
	return page.res, nil
}

// CountTaskQuery is how many tasks the query's filters match, the cursor and the text left out: the total a reply without a search
// gives.
func (s *Store) CountTaskQuery(ctx context.Context, q *TaskQuery) (int, error) {
	noCursor := *q
	noCursor.cursor = nil
	sortExpr, _ := taskQuerySort(q)
	join, where, args := taskQueryWhere(&noCursor, sortExpr)
	stmt := "SELECT COUNT(*) FROM board_tasks" + join
	if len(where) > 0 {
		stmt += " WHERE " + strings.Join(where, " AND ")
	}
	var n int
	if err := s.reader().GetContext(ctx, &n, stmt, args...); err != nil {
		return 0, err
	}
	return n, nil
}
