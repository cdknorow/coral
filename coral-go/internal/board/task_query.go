package board

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The task query on GET /api/board/tasks (#4540): filters, sort, paging and a choice of the fields that come back, all in SQL. Every column and expression that reaches the SQL text is a constant from this file;
// every caller value is a bound parameter (or a validated enum word), never part of the statement.

// TaskQuerySummaryFields is what fields=summary stands for: the minimal set, no long text.
var TaskQuerySummaryFields = []string{"id", "title", "status", "priority", "assigned_to", "board_id", "board_label", "parked_reason", "parked_until", "created_at", "updated_at"}

// TaskQueryLargeFields can be long: they come back only when fields names them (or with no fields at all, which is the whole row).
var TaskQueryLargeFields = []string{"body", "completion_message"}

// updatedAtSQL: a task has no updated_at column; the newest of its creation, claim, completion and last activity says when it
// last changed. All are UTC RFC 3339 text written by nowUTC, so they compare as text.
const updatedAtSQL = `MAX(created_at, COALESCE(claimed_at, created_at), COALESCE(completed_at, created_at), COALESCE(last_activity_at, created_at))`

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
	cursor        *queryTaskCursor
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
	if f, ferr := splitMulti("fields", p.Fields); ferr != nil {
		return nil, ferr
	} else if f != nil {
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
		q.Fields = names
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
	if v, ok, perr := single("parked", p.Parked); perr != nil {
		return nil, perr
	} else if ok {
		switch v {
		case "true":
			b := true
			q.Parked = &b
		case "false":
			b := false
			q.Parked = &b
		default:
			return nil, taskQueryErr("parked must be true or false")
		}
	}
	if q.CreatedSince, err = parseSince("created_since", p.CreatedSince); err != nil {
		return nil, err
	}
	if q.UpdatedSince, err = parseSince("updated_since", p.UpdatedSince); err != nil {
		return nil, err
	}
	if v, ok, serr := single("sort", p.Sort); serr != nil {
		return nil, serr
	} else if ok {
		switch v {
		case "created_at":
		case "updated_at":
			q.SortUpdated = true
		default:
			return nil, taskQueryErr("unknown sort %s (created_at or updated_at)", v)
		}
	}
	if v, ok, serr := single("order", p.Order); serr != nil {
		return nil, serr
	} else if ok {
		switch v {
		case "desc":
		case "asc":
			q.Desc = false
		default:
			return nil, taskQueryErr("unknown order %s (asc or desc)", v)
		}
	}
	large := q.Fields == nil // the whole row carries the long text
	for _, f := range q.Fields {
		if f == "body" || f == "completion_message" {
			large = true
		}
	}
	max := TaskQueryMaxLimit
	if large {
		max = TaskQueryMaxLimitLarge
	}
	q.Limit = TaskQueryDefaultLimit
	if v, ok, serr := single("limit", p.Limit); serr != nil {
		return nil, serr
	} else if ok {
		n := 0
		if _, scanErr := fmt.Sscanf(v, "%d", &n); scanErr != nil || fmt.Sprint(n) != v || n < 1 || n > max {
			return nil, taskQueryErr("limit must be a whole number from 1 to %d", max)
		}
		q.Limit = n
	} else if q.Limit > max {
		q.Limit = max
	}
	if v, ok, serr := single("cursor", p.Cursor); serr != nil {
		return nil, serr
	} else if ok {
		c, cerr := decodeQueryTaskCursor(v)
		if cerr != nil {
			return nil, taskQueryErr("cursor is not one this server gave")
		}
		sortName := "created_at"
		if q.SortUpdated {
			sortName = "updated_at"
		}
		if c.Sort != sortName || c.Desc != q.Desc {
			return nil, taskQueryErr("cursor was made for a different sort or order")
		}
		q.cursor = c
	}
	return q, nil
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

// TaskQueryResult is one page: the rows hold only the fields asked for.
type TaskQueryResult struct {
	Tasks      []map[string]any
	NextCursor string
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// QueryTasks runs q. The statement text is built only from the constants above and "?" placeholders.
func (s *Store) QueryTasks(ctx context.Context, q *TaskQuery) (*TaskQueryResult, error) {
	sortExpr := "created_at"
	sortName := "created_at"
	if q.SortUpdated {
		sortExpr, sortName = updatedAtSQL, "updated_at"
	}
	var cols []string
	wantVerification := false
	for _, f := range q.Fields {
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
	var where []string
	var args []any
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
	join := ""
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
	cmp := "<"
	dir := "DESC"
	if !q.Desc {
		cmp, dir = ">", "ASC"
	}
	if q.cursor != nil {
		where = append(where, "("+sortExpr+" "+cmp+" ? OR ("+sortExpr+" = ? AND id "+cmp+" ?))")
		args = append(args, q.cursor.Val, q.cursor.Val, q.cursor.ID)
	}
	stmt := "SELECT " + strings.Join(cols, ", ") + " FROM board_tasks" + join
	if len(where) > 0 {
		stmt += " WHERE " + strings.Join(where, " AND ")
	}
	stmt += " ORDER BY " + sortExpr + " " + dir + ", id " + dir + " LIMIT ?"
	args = append(args, q.Limit+1)

	rows, err := s.reader().QueryxContext(ctx, stmt, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	res := &TaskQueryResult{Tasks: []map[string]any{}}
	var boards []string
	var vTasks []Task
	var last queryTaskCursor
	more := false
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
		if len(res.Tasks) == q.Limit {
			more = true
			break
		}
		id, _ := m["_id"].(int64)
		sortVal, _ := m["_sort"].(string)
		last = queryTaskCursor{Sort: sortName, Desc: q.Desc, Val: sortVal, ID: id}
		board, _ := m["_board"].(string)
		boards = append(boards, board)
		if wantVerification {
			t := Task{ID: id}
			t.Status, _ = m["_status"].(string)
			if c, ok := m["_completed"].(string); ok {
				t.CompletedAt = &c
			}
			vTasks = append(vTasks, t)
			delete(m, "_status")
			delete(m, "_completed")
		}
		delete(m, "_id")
		delete(m, "_board")
		delete(m, "_sort")
		res.Tasks = append(res.Tasks, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, f := range q.Fields {
		if f == "board_label" {
			labels := s.BoardLabels(ctx, boards)
			for i, m := range res.Tasks {
				if l := labels[boards[i]]; l != "" {
					m["board_label"] = l
				}
			}
			break
		}
	}
	if wantVerification {
		ids := make([]int64, len(vTasks))
		for i := range vTasks {
			ids[i] = vTasks[i].ID
		}
		data, err := s.loadWorkflowData(ctx, ids)
		if err != nil {
			return nil, err
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
			res.Tasks[i]["verification"] = state
		}
	}
	if more {
		res.NextCursor = last.encode()
	}
	return res, nil
}

// QueryTaskRows runs q for the whole task rows (q.Fields nil), as ListAllTasks returns them: the matching ids and the next cursor
// come from the same query as the projections, the rows are then read in that order.
func (s *Store) QueryTaskRows(ctx context.Context, q *TaskQuery) ([]Task, string, error) {
	idq := *q
	idq.Fields = []string{"id"}
	res, err := s.QueryTasks(ctx, &idq)
	if err != nil {
		return nil, "", err
	}
	ids := make([]any, len(res.Tasks))
	for i, m := range res.Tasks {
		ids[i], _ = m["id"].(int64)
	}
	tasks := []Task{}
	if len(ids) == 0 {
		return tasks, res.NextCursor, nil
	}
	var rows []Task
	err = s.reader().SelectContext(ctx, &rows,
		`SELECT id, revision, board_id, title, body, status, priority, created_by, assigned_to, completed_by, completion_message, created_at, claimed_at, completed_at, session_id, cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, parked_reason, parked_until, remind_at FROM board_tasks WHERE id IN (`+placeholders(len(ids))+`)`, ids...)
	if err != nil {
		return nil, "", err
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
	return tasks, res.NextCursor, nil
}
