package board

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// The task query on GET /api/board/tasks (#4540): filters, sort, paging and a choice of the fields that come back, all in SQL. Every column and expression that reaches the SQL text is a constant from this file;
// every caller value is a bound parameter (or a validated enum word), never part of the statement.

// TaskQuerySummaryFields is what fields=summary stands for: the minimal set, no long text.
var TaskQuerySummaryFields = []string{"id", "title", "status", "priority", "assigned_to", "board_id", "board_label", "parked_reason", "parked_until", "created_at", "updated_at"}

// TaskQueryLargeFields can be long: they come back only when fields names them (or with no fields at all, which is the whole row).
var TaskQueryLargeFields = []string{"body", "completion_message"}

// updatedAtSQL: a task has no updated_at column; the newest of its creation, claim, completion, last activity and last edit (its body, title, priority or assignee: edited_at,
// #5819) says when it last changed. All are UTC RFC 3339 text written by nowUTC, so they compare as text. An edit moves this and neither the revision (amendments and
// review rounds own that) nor last_activity_at (the stall timer's).
const updatedAtSQL = `MAX(created_at, COALESCE(claimed_at, created_at), COALESCE(completed_at, created_at), COALESCE(last_activity_at, created_at), COALESCE(edited_at, created_at))`

// taskQueryColumns maps each field the caller may name to its SQL. board_label is not a column: it is filled from the boards.
var taskQueryColumns = map[string]string{
	"id": "id", "revision": "revision", "board_id": "board_id", "title": "title", "status": "status", "priority": "priority",
	"created_by": "created_by", "assigned_to": "assigned_to", "completed_by": "completed_by",
	"parked_reason": "parked_reason", "parked_until": "parked_until", "remind_at": "remind_at",
	"created_at": "created_at", "claimed_at": "claimed_at", "completed_at": "completed_at", "updated_at": updatedAtSQL,
	"body": "body", "completion_message": "completion_message",
}

// TaskQueryFieldNames lists every field a caller may name (besides the word summary), sorted for the docs and the 400 message.
func TaskQueryFieldNames() []string {
	return []string{"assigned_to", "board_id", "board_label", "body", "claimed_at", "completed_at", "completed_by", "completion_message",
		"created_at", "created_by", "id", "parked_reason", "parked_until", "priority", "remind_at", "revision", "status", "title", "updated_at", "verification"}
}

var (
	taskStatuses   = []string{"pending", "in_progress", "completed", "skipped", "blocked", "draft", "review_pending"}
	taskPriorities = []string{"critical", "high", "medium", "low"}
)

const (
	TaskQueryDefaultLimit = 100
	TaskQueryMaxLimit     = 500
	// With a large field named each row can carry a long text: fewer rows per page.
	TaskQueryMaxLimitLarge = 100
	taskQueryMaxValues     = 50
)

// ErrTaskQuery is an invalid query: the message names what was wrong and is safe to show the caller.
var ErrTaskQuery = errors.New("invalid task query")

func taskQueryErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrTaskQuery, fmt.Sprintf(format, a...))
}

// TaskQuery is a validated query. Build it with ParseTaskQuery.
type TaskQuery struct {
	// Fields is nil when the caller named none: the reply is then the whole task row, as the list has always been.
	Fields        []string
	Statuses      []string
	AssignedTo    []string
	Boards        []string
	Priorities    []string
	CreatedBy     []string
	Verifications []string
	Parked        *bool
	CreatedSince  string
	UpdatedSince  string
	SortUpdated   bool
	Desc          bool
	Limit         int
	// Text is q, lower-cased: a plain substring (never a pattern) of a task's title, body or completion message (#4745). Empty: no
	// search. Shown, set by the caller and not by the parser, is what a viewer sees of a text: the match is made on it (nil: the text
	// as stored), so a search through Coral Hub cannot tell a viewer what the redaction hides.
	Text   string
	Shown  func(string) string
	cursor *queryTaskCursor
}

type queryTaskCursor struct {
	Sort string `json:"s"`
	Desc bool   `json:"d"`
	Val  string `json:"v"`
	ID   int64  `json:"i"`
}

// TaskQueryParams are the raw values of a request, one slice per parameter, as the HTTP layer read them.
type TaskQueryParams struct {
	Fields, Status, AssignedTo, Board, Priority, CreatedBy, Verification []string
	Parked, CreatedSince, UpdatedSince, Sort, Order, Limit, Cursor       []string
	Q                                                                    []string
}

// splitMulti reads a multi-value parameter: repeated (status=a&status=b) or comma separated (status=a,b), or both.
func splitMulti(name string, vals []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, v := range vals {
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				return nil, taskQueryErr("%s has an empty value", name)
			}
			if len(p) > 200 {
				return nil, taskQueryErr("%s has a value over 200 characters", name)
			}
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	if len(out) > taskQueryMaxValues {
		return nil, taskQueryErr("%s has more than %d values", name, taskQueryMaxValues)
	}
	return out, nil
}

func oneOf(name string, vals []string, allowed []string) ([]string, error) {
	got, err := splitMulti(name, vals)
	if err != nil || got == nil {
		return got, err
	}
	var bad []string
	for _, v := range got {
		ok := false
		for _, a := range allowed {
			if v == a {
				ok = true
			}
		}
		if !ok {
			bad = append(bad, v)
		}
	}
	if len(bad) > 0 {
		return nil, taskQueryErr("unknown %s %s (one of %s)", name, strings.Join(bad, ", "), strings.Join(allowed, ", "))
	}
	return got, nil
}

func single(name string, vals []string) (string, bool, error) {
	switch len(vals) {
	case 0:
		return "", false, nil
	case 1:
		return vals[0], true, nil
	}
	return "", false, taskQueryErr("%s is given more than once", name)
}

func parseSince(name string, vals []string) (string, error) {
	v, ok, err := single(name, vals)
	if err != nil || !ok {
		return "", err
	}
	t, perr := time.Parse(time.RFC3339, v)
	if perr != nil {
		return "", taskQueryErr("%s must be an RFC 3339 time, for example 2026-10-18T00:00:00Z", name)
	}
	return t.UTC().Format(time.RFC3339), nil
}

// ParseTaskQuery validates the raw parameters. Any unknown field, status, priority, sort or filter value is an error that
// names it.
func ParseTaskQuery(p TaskQueryParams) (*TaskQuery, error) {
	q := &TaskQuery{Desc: true}
	var err error
	if q.Fields, err = parseQueryFields(p.Fields); err != nil {
		return nil, err
	}
	if q.Statuses, err = oneOf("status", p.Status, taskStatuses); err != nil {
		return nil, err
	}
	if q.Priorities, err = oneOf("priority", p.Priority, taskPriorities); err != nil {
		return nil, err
	}
	if q.Verifications, err = oneOf("verification", p.Verification, []string{VerificationVerified, VerificationEvidence, VerificationUnverified}); err != nil {
		return nil, err
	}
	if q.AssignedTo, err = splitMulti("assigned_to", p.AssignedTo); err != nil {
		return nil, err
	}
	if q.Boards, err = splitMulti("board", p.Board); err != nil {
		return nil, err
	}
	if q.CreatedBy, err = splitMulti("created_by", p.CreatedBy); err != nil {
		return nil, err
	}
	if q.Parked, err = parseParked(p.Parked); err != nil {
		return nil, err
	}
	if q.CreatedSince, err = parseSince("created_since", p.CreatedSince); err != nil {
		return nil, err
	}
	if q.UpdatedSince, err = parseSince("updated_since", p.UpdatedSince); err != nil {
		return nil, err
	}
	if q.SortUpdated, q.Desc, err = parseSortOrder(p.Sort, p.Order); err != nil {
		return nil, err
	}
	if q.Limit, err = parseQueryLimit(p.Limit, q.Fields); err != nil {
		return nil, err
	}
	if q.Text, err = parseQueryText(p.Q); err != nil {
		return nil, err
	}
	if q.cursor, err = parseQueryCursor(p.Cursor, q.SortUpdated, q.Desc); err != nil {
		return nil, err
	}
	return q, nil
}

// parseQueryFields reads the fields asked for: nil when none (the whole row), "summary" standing for
// TaskQuerySummaryFields, each name once, in the order first named; an unknown one is an error naming it.
func parseQueryFields(vals []string) ([]string, error) {
	f, err := splitMulti("fields", vals)
	if err != nil || f == nil {
		return f, err
	}
	var bad, names []string
	seen := map[string]bool{}
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for _, n := range f {
		if n == "summary" {
			for _, m := range TaskQuerySummaryFields {
				add(m)
			}
			continue
		}
		if _, ok := taskQueryColumns[n]; !ok && n != "board_label" && n != "verification" {
			bad = append(bad, n)
			continue
		}
		add(n)
	}
	if len(bad) > 0 {
		return nil, taskQueryErr("unknown field %s (summary, or one of %s)", strings.Join(bad, ", "), strings.Join(TaskQueryFieldNames(), ", "))
	}
	return names, nil
}

// parseParked reads parked: nil when not given, else true or false.
func parseParked(vals []string) (*bool, error) {
	v, ok, err := single("parked", vals)
	if err != nil || !ok {
		return nil, err
	}
	switch v {
	case "true":
		b := true
		return &b, nil
	case "false":
		b := false
		return &b, nil
	}
	return nil, taskQueryErr("parked must be true or false")
}

// parseSortOrder reads sort (created_at, the default, or updated_at) and order (desc, the default, or asc).
func parseSortOrder(sortVals, orderVals []string) (sortUpdated, desc bool, err error) {
	desc = true
	if v, ok, serr := single("sort", sortVals); serr != nil {
		return false, false, serr
	} else if ok {
		switch v {
		case "created_at":
		case "updated_at":
			sortUpdated = true
		default:
			return false, false, taskQueryErr("unknown sort %s (created_at or updated_at)", v)
		}
	}
	if v, ok, serr := single("order", orderVals); serr != nil {
		return false, false, serr
	} else if ok {
		switch v {
		case "desc":
		case "asc":
			desc = false
		default:
			return false, false, taskQueryErr("unknown order %s (asc or desc)", v)
		}
	}
	return sortUpdated, desc, nil
}

// parseQueryLimit reads limit: a whole number from 1 to the cap, TaskQueryDefaultLimit when not given. The cap is
// TaskQueryMaxLimitLarge for a reply carrying the long text (the whole row, or body or completion_message asked
// for), else TaskQueryMaxLimit.
func parseQueryLimit(vals, fields []string) (int, error) {
	large := fields == nil // the whole row carries the long text
	for _, f := range fields {
		if f == "body" || f == "completion_message" {
			large = true
		}
	}
	max := TaskQueryMaxLimit
	if large {
		max = TaskQueryMaxLimitLarge
	}
	v, ok, err := single("limit", vals)
	if err != nil {
		return 0, err
	}
	if !ok {
		return min(TaskQueryDefaultLimit, max), nil
	}
	n := 0
	if _, scanErr := fmt.Sscanf(v, "%d", &n); scanErr != nil || fmt.Sprint(n) != v || n < 1 || n > max {
		return 0, taskQueryErr("limit must be a whole number from 1 to %d", max)
	}
	return n, nil
}

// parseQueryCursor reads cursor: one this server gave, made for the same sort and order (nil when not given).
func parseQueryCursor(vals []string, sortUpdated, desc bool) (*queryTaskCursor, error) {
	v, ok, err := single("cursor", vals)
	if err != nil || !ok {
		return nil, err
	}
	c, cerr := decodeQueryTaskCursor(v)
	if cerr != nil {
		return nil, taskQueryErr("cursor is not one this server gave")
	}
	sortName := "created_at"
	if sortUpdated {
		sortName = "updated_at"
	}
	if c.Sort != sortName || c.Desc != desc {
		return nil, taskQueryErr("cursor was made for a different sort or order")
	}
	return c, nil
}

func decodeQueryTaskCursor(s string) (*queryTaskCursor, error) {
	if len(s) > 512 {
		return nil, errors.New("long")
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	var c queryTaskCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	if (c.Sort != "created_at" && c.Sort != "updated_at") || c.Val == "" || len(c.Val) > 64 || c.ID <= 0 {
		return nil, errors.New("bad")
	}
	return &c, nil
}

func (c queryTaskCursor) encode() string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// TaskQueryResult is one page: the rows hold only the fields asked for. Total, with a search, is how many tasks match it (counted over
// the tasks scanned: Truncated says the scan stopped at its bound, so the count and the pages stop there too).
type TaskQueryResult struct {
	Tasks      []map[string]any
	NextCursor string
	Total      int
	Truncated  bool
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// QueryTasks runs q. The statement text is built only from the constants above and "?" placeholders.
func (s *Store) QueryTasks(ctx context.Context, q *TaskQuery) (*TaskQueryResult, error) {
	if q.Text != "" {
		return s.queryTasksText(ctx, q)
	}
	stmt, args, wantVerification := taskQueryStatement(q)
	rows, err := s.reader().QueryxContext(ctx, stmt, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page, err := scanTaskQueryPage(rows, q, wantVerification)
	if err != nil {
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

// taskQuerySort is the expression the rows are sorted (and paged) on, and its name in a cursor.
func taskQuerySort(q *TaskQuery) (expr, name string) {
	if q.SortUpdated {
		return updatedAtSQL, "updated_at"
	}
	return "created_at", "created_at"
}

// taskQueryStatement is q's SELECT and its arguments; wantVerification says the status and completion columns the
// verification state is worked out from were added.
func taskQueryStatement(q *TaskQuery) (stmt string, args []any, wantVerification bool) {
	sortExpr, _ := taskQuerySort(q)
	cols, wantVerification := taskQueryColumnList(q.Fields, sortExpr)
	join, where, args := taskQueryWhere(q, sortExpr)
	dir := "DESC"
	if !q.Desc {
		dir = "ASC"
	}
	stmt = "SELECT " + strings.Join(cols, ", ") + " FROM board_tasks" + join
	if len(where) > 0 {
		stmt += " WHERE " + strings.Join(where, " AND ")
	}
	stmt += " ORDER BY " + sortExpr + " " + dir + ", id " + dir + " LIMIT ?"
	args = append(args, q.Limit+1)
	return stmt, args, wantVerification
}

// taskQueryColumnList is the columns asked for, each under its field's name, then the hidden ones every page needs
// (_id, _board, _sort) and, for verification, _status and _completed. board_label and verification are not columns.
func taskQueryColumnList(fields []string, sortExpr string) (cols []string, wantVerification bool) {
	for _, f := range fields {
		if f == "board_label" {
			continue
		}
		if f == "verification" {
			wantVerification = true
			continue
		}
		cols = append(cols, taskQueryColumns[f]+` AS "`+f+`"`)
	}
	if wantVerification {
		cols = append(cols, `status AS "_status"`, `completed_at AS "_completed"`)
	}
	cols = append(cols, `id AS "_id"`, `board_id AS "_board"`, sortExpr+` AS "_sort"`)
	return cols, wantVerification
}

// taskQueryWhere is q's filters and cursor as WHERE terms (joined with AND) and their arguments, with the join the
// verification filter needs.
func taskQueryWhere(q *TaskQuery, sortExpr string) (join string, where []string, args []any) {
	in := func(col string, vals []string) {
		if len(vals) == 0 {
			return
		}
		where = append(where, col+" IN ("+placeholders(len(vals))+")")
		for _, v := range vals {
			args = append(args, v)
		}
	}
	in("status", q.Statuses)
	in("priority", q.Priorities)
	in("assigned_to", q.AssignedTo)
	in("board_id", q.Boards)
	in("created_by", q.CreatedBy)
	if len(q.Verifications) > 0 {
		join = " LEFT JOIN task_workflows w ON w.task_id = board_tasks.id"
		var alts []string
		for _, v := range q.Verifications {
			alts = append(alts, verificationSQL[v])
		}
		where = append(where, "("+strings.Join(alts, " OR ")+")")
	}
	if q.Parked != nil {
		if *q.Parked {
			where = append(where, parkedSQL)
		} else {
			where = append(where, "NOT "+parkedSQL)
		}
	}
	if q.CreatedSince != "" {
		where = append(where, "created_at >= ?")
		args = append(args, q.CreatedSince)
	}
	if q.UpdatedSince != "" {
		where = append(where, updatedAtSQL+" >= ?")
		args = append(args, q.UpdatedSince)
	}
	if q.cursor != nil {
		cmp := "<"
		if !q.Desc {
			cmp = ">"
		}
		where = append(where, "("+sortExpr+" "+cmp+" ? OR ("+sortExpr+" = ? AND id "+cmp+" ?))")
		args = append(args, q.cursor.Val, q.cursor.Val, q.cursor.ID)
	}
	return join, where, args
}

// taskQueryPage is a page as read: the rows (hidden columns removed), each row's board and, for verification, its
// status and completion; the cursor of the last row kept, and whether a row past the limit came.
type taskQueryPage struct {
	res    *TaskQueryResult
	boards []string
	vTasks []Task
	last   queryTaskCursor
	more   bool
}

// scanTaskQueryPage reads up to q.Limit rows; the one more the statement asks for only says there are more.
func scanTaskQueryPage(rows *sqlx.Rows, q *TaskQuery, wantVerification bool) (*taskQueryPage, error) {
	_, sortName := taskQuerySort(q)
	page := &taskQueryPage{res: &TaskQueryResult{Tasks: []map[string]any{}}}
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
		if len(page.res.Tasks) == q.Limit {
			page.more = true
			break
		}
		id, _ := m["_id"].(int64)
		sortVal, _ := m["_sort"].(string)
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
			delete(m, "_status")
			delete(m, "_completed")
		}
		delete(m, "_id")
		delete(m, "_board")
		delete(m, "_sort")
		page.res.Tasks = append(page.res.Tasks, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return page, nil
}

// addBoardLabels sets each row's board_label from its board (boards[i] is row i's); a board without a label is left out.
func (s *Store) addBoardLabels(ctx context.Context, tasks []map[string]any, boards []string) {
	labels := s.BoardLabels(ctx, boards)
	for i, m := range tasks {
		if l := labels[boards[i]]; l != "" {
			m["board_label"] = l
		}
	}
}

// addVerification sets each row's verification state from its stored workflow (vTasks[i] is row i's).
func (s *Store) addVerification(ctx context.Context, tasks []map[string]any, vTasks []Task) error {
	ids := make([]int64, len(vTasks))
	for i := range vTasks {
		ids[i] = vTasks[i].ID
	}
	data, err := s.loadWorkflowData(ctx, ids)
	if err != nil {
		return err
	}
	for i, t := range vTasks {
		// A task whose workflow cannot be read has no state, as in the whole rows.
		var state any
		if w, werr := decodeWorkflow(data[t.ID], true); werr == nil {
			finishWorkflow(&t, w)
			if t.Verification != nil {
				state = t.Verification
			}
		}
		tasks[i]["verification"] = state
	}
	return nil
}

// QueryTaskRows runs q for the whole task rows (q.Fields nil), as ListAllTasks returns them: the matching ids and the next cursor
// come from the same query as the projections, the rows are then read in that order.
func (s *Store) QueryTaskRows(ctx context.Context, q *TaskQuery) ([]Task, string, error) {
	tasks, res, err := s.QueryTaskRowsResult(ctx, q)
	if err != nil {
		return nil, "", err
	}
	return tasks, res.NextCursor, nil
}

// QueryTaskRowsResult is QueryTaskRows with the page's whole result: the next cursor and, with a search, the total and whether the scan
// stopped at its bound (#4745).
func (s *Store) QueryTaskRowsResult(ctx context.Context, q *TaskQuery) ([]Task, *TaskQueryResult, error) {
	idq := *q
	idq.Fields = []string{"id"}
	res, err := s.QueryTasks(ctx, &idq)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]any, len(res.Tasks))
	for i, m := range res.Tasks {
		ids[i], _ = m["id"].(int64)
	}
	tasks := []Task{}
	if len(ids) == 0 {
		return tasks, res, nil
	}
	var rows []Task
	err = s.reader().SelectContext(ctx, &rows,
		`SELECT id, revision, board_id, title, body, status, priority, created_by, assigned_to, completed_by, completion_message, created_at, claimed_at, completed_at, session_id, cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, parked_reason, parked_until, remind_at FROM board_tasks WHERE id IN (`+placeholders(len(ids))+`)`, ids...)
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[int64]Task, len(rows))
	for _, t := range rows {
		byID[t.ID] = t
	}
	for _, id := range ids {
		if t, ok := byID[id.(int64)]; ok { // a task deleted between the two reads is left out
			tasks = append(tasks, t)
		}
	}
	s.populateTaskDeps(ctx, tasks)
	return tasks, res, nil
}
