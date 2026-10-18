package board

import (
	"context"
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// QueryTasks, split into its statement, scan, label and verification parts (#4669 part 2, #4599), answers every query as
// queryTasksBefore4599 (the function at v1.4.91, below) does: the same rows, fields, labels, verification states and next
// cursor, page after page. The board holds every verification state (through the real completion and review paths),
// random rows sharing timestamps (so pages break ties on id), parked and unparked rows, labelled, unlabelled and unknown
// boards, and a workflow Go cannot decode. Each filter, sort, field and outcome has a floor, so none goes unexercised.

var qtOracleTimes = []string{"2026-10-01T09:00:00Z", "2026-10-01T09:00:00Z", "2026-10-02T10:30:00Z", "2026-10-03T08:15:00Z", "2026-10-03T08:15:00Z", "2026-10-05T12:00:00Z", "2026-10-07T18:45:00Z"}

// qtOracleBoards: team (verEnv's), two labelled boards, one with an empty label, and one with no row at all.
var qtOracleBoards = []string{"team", "b-one", "b-two", "b-empty", "b-none"}

var qtOracleNames = []string{"lead", "worker", "rev1", "rev2", "ops", "o'brien"}

// seedTaskQueryOracle fills a board for the oracle.
func seedTaskQueryOracle(t *testing.T, rng *rand.Rand) *verEnv {
	t.Helper()
	e := newVerEnv(t)
	art := []TaskArtifact{{Name: "report", Content: "x"}}
	e.done("plain", "success", nil, nil)
	e.done("evidence", "success", art, nil)
	e.done("failed", "failed", art, nil)
	e.reviewed("reviewed", art, "rev1")
	e.done("attested", "success", art, &CreateTaskOpts{Workflow: TaskWorkflow{CompletionGates: reportGate}})
	e.done("checked", "success", art, &CreateTaskOpts{Workflow: TaskWorkflow{CompletionGates: checkGate}})
	var blobs []string
	require.NoError(t, e.s.db.SelectContext(e.ctx, &blobs, `SELECT data FROM task_workflows ORDER BY task_id`))
	require.NotEmpty(t, blobs)
	blobs = append(blobs, `{"instructions": 7}`) // JSON the filters read, but not a workflow Go can decode: no state
	for id, label := range map[string]string{"b-one": "Board One", "b-two": "Board Two", "b-empty": ""} {
		_, err := e.s.db.ExecContext(e.ctx, `INSERT INTO boards (id, label, created_at) VALUES (?, ?, ?)`, id, label, qtOracleTimes[0])
		require.NoError(t, err)
	}
	opt := func(p float64, vals []string) any {
		if rng.Float64() < p {
			return nil
		}
		return vals[rng.Intn(len(vals))]
	}
	day := 24 * time.Hour
	for i := 0; i < 120+rng.Intn(60); i++ {
		status := taskStatuses[rng.Intn(len(taskStatuses))]
		if rng.Intn(5) < 2 {
			status = "completed"
		}
		var parkedReason, parkedUntil any
		switch rng.Intn(4) {
		case 0:
			parkedReason = "waiting"
		case 1:
			parkedReason, parkedUntil = "later", time.Now().Add(day).UTC().Format(time.RFC3339)
		case 2:
			parkedReason, parkedUntil = "was", time.Now().Add(-day).UTC().Format(time.RFC3339)
		}
		res, err := e.s.db.ExecContext(e.ctx, `INSERT INTO board_tasks (board_id, title, body, status, priority, created_by, assigned_to,
			completed_by, completion_message, created_at, claimed_at, completed_at, last_activity_at, parked_reason, parked_until, remind_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			qtOracleBoards[rng.Intn(len(qtOracleBoards))], fmt.Sprintf("task %d", i), opt(0.3, []string{"a body", "' OR 1=1 --", ""}), status,
			taskPriorities[rng.Intn(len(taskPriorities))], qtOracleNames[rng.Intn(len(qtOracleNames))], opt(0.25, qtOracleNames),
			opt(0.5, qtOracleNames), opt(0.5, []string{"done", "landed"}), qtOracleTimes[rng.Intn(len(qtOracleTimes))],
			opt(0.4, qtOracleTimes), opt(0.4, qtOracleTimes), opt(0.5, qtOracleTimes), parkedReason, parkedUntil, opt(0.7, qtOracleTimes))
		require.NoError(t, err)
		if rng.Intn(10) < 6 {
			id, _ := res.LastInsertId()
			_, err = e.s.db.ExecContext(e.ctx, `INSERT INTO task_workflows (task_id, data) VALUES (?, ?)`, id, blobs[rng.Intn(len(blobs))])
			require.NoError(t, err)
		}
	}
	return e
}

// some is up to n distinct values from vals, or nil.
func some(rng *rand.Rand, vals []string, n int) []string {
	if rng.Intn(2) == 0 {
		return nil
	}
	var out []string
	for _, i := range rng.Perm(len(vals))[:1+rng.Intn(min(n, len(vals)))] {
		out = append(out, vals[i])
	}
	return []string{strings.Join(out, ",")}
}

func randomValidTaskQuery(rng *rand.Rand) TaskQueryParams {
	var p TaskQueryParams
	fieldNames := append(TaskQueryFieldNames(), "summary")
	switch rng.Intn(4) {
	case 0: // none: the hidden columns only
	case 1:
		p.Fields = []string{"summary"}
	default:
		p.Fields = some(rng, fieldNames, 6)
		if rng.Intn(3) == 0 {
			p.Fields = []string{strings.Join([]string{"verification", "board_label", "id"}[:1+rng.Intn(3)], ",")}
		}
	}
	p.Status = some(rng, taskStatuses, 3)
	p.Priority = some(rng, taskPriorities, 2)
	p.Verification = some(rng, []string{VerificationVerified, VerificationEvidence, VerificationUnverified}, 2)
	p.AssignedTo = some(rng, qtOracleNames, 3)
	p.Board = some(rng, qtOracleBoards, 3)
	p.CreatedBy = some(rng, qtOracleNames, 3)
	if rng.Intn(3) == 0 {
		p.Parked = []string{[]string{"true", "false"}[rng.Intn(2)]}
	}
	if rng.Intn(4) == 0 {
		p.CreatedSince = []string{qtOracleTimes[rng.Intn(len(qtOracleTimes))]}
	}
	if rng.Intn(4) == 0 {
		p.UpdatedSince = []string{qtOracleTimes[rng.Intn(len(qtOracleTimes))]}
	}
	if rng.Intn(2) == 0 {
		p.Sort = []string{[]string{"created_at", "updated_at"}[rng.Intn(2)]}
	}
	if rng.Intn(2) == 0 {
		p.Order = []string{[]string{"asc", "desc"}[rng.Intn(2)]}
	}
	if rng.Intn(4) != 0 {
		p.Limit = []string{fmt.Sprint(1 + rng.Intn(25))}
	}
	return p
}

// qtUses names what a query exercises, for the floors.
func qtUses(q *TaskQuery, res *TaskQueryResult) []string {
	var uses []string
	add := func(on bool, name string) {
		if on {
			uses = append(uses, name)
		}
	}
	add(len(q.Statuses) > 0, "status")
	add(len(q.Priorities) > 0, "priority")
	add(len(q.AssignedTo) > 0, "assigned_to")
	add(len(q.Boards) > 0, "board")
	add(len(q.CreatedBy) > 0, "created_by")
	for _, v := range q.Verifications {
		uses = append(uses, "verification="+v)
	}
	add(q.Parked != nil && *q.Parked, "parked")
	add(q.Parked != nil && !*q.Parked, "unparked")
	add(q.CreatedSince != "", "created_since")
	add(q.UpdatedSince != "", "updated_since")
	add(q.SortUpdated, "sort updated_at")
	add(!q.Desc, "asc")
	add(q.cursor != nil, "a later page")
	add(q.Fields == nil, "no fields")
	add(res.NextCursor != "", "a next cursor")
	return uses
}

func TestQueryTasksAgreesWithBefore4599(t *testing.T) {
	ctx := context.Background()
	seeds := 5
	if testing.Short() {
		seeds = 2
	}
	nonEmpty := map[string]int{} // a query using this, that returned rows
	rowsWith := map[string]int{} // rows that came back with this
	pages := 0
	for seed := int64(1); seed <= int64(seeds); seed++ {
		rng := rand.New(rand.NewSource(seed))
		e := seedTaskQueryOracle(t, rng)
		for i := 0; i < 400; i++ {
			p := randomValidTaskQuery(rng)
			for page := 0; page < 6; page++ {
				q, err := ParseTaskQuery(p)
				require.NoError(t, err, "%+v", p)
				old, oldErr := queryTasksBefore4599(e.s, ctx, q)
				q2, err := ParseTaskQuery(p)
				require.NoError(t, err)
				got, gotErr := e.s.QueryTasks(ctx, q2)
				require.Equal(t, fmt.Sprint(oldErr), fmt.Sprint(gotErr), "seed %d query %d page %d: %+v", seed, i, page, p)
				require.NoError(t, gotErr)
				if !reflect.DeepEqual(old, got) {
					require.Equal(t, fmt.Sprintf("%#v", old), fmt.Sprintf("%#v", got), "seed %d query %d page %d: %+v", seed, i, page, p)
					t.Fatalf("seed %d query %d page %d differ: %+v", seed, i, page, p)
				}
				pages++
				if len(got.Tasks) > 0 {
					for _, u := range qtUses(q2, got) {
						nonEmpty[u]++
					}
				} else {
					nonEmpty["an empty page"]++
				}
				for _, m := range got.Tasks {
					if l, ok := m["board_label"]; ok {
						rowsWith["board_label "+fmt.Sprint(l)]++
					} else if slices.Contains(q2.Fields, "board_label") {
						rowsWith["board_label left out"]++
					}
					if v, ok := m["verification"]; ok {
						if tv, isV := v.(*Verification); isV {
							rowsWith["verification "+tv.State]++
						} else {
							rowsWith["verification none"]++
						}
					}
				}
				if got.NextCursor == "" || rng.Intn(4) == 0 {
					break
				}
				p.Cursor = []string{got.NextCursor}
			}
		}
	}
	t.Logf("%d pages compared; queries with rows, per use: %v; rows, per value: %v", pages, nonEmpty, rowsWith)
	for _, u := range []string{"status", "priority", "assigned_to", "board", "created_by", "verification=" + VerificationVerified,
		"verification=" + VerificationEvidence, "verification=" + VerificationUnverified, "parked", "unparked", "created_since",
		"updated_since", "sort updated_at", "asc", "a later page", "no fields", "a next cursor", "an empty page"} {
		assert.GreaterOrEqual(t, nonEmpty[u], 40, "%q went (almost) unexercised", u)
	}
	for _, r := range []string{"board_label Board One", "board_label Board Two", "board_label team", "board_label b-none",
		"board_label left out", "verification " + VerificationVerified, "verification " + VerificationEvidence,
		"verification " + VerificationUnverified, "verification none"} {
		assert.GreaterOrEqual(t, rowsWith[r], 40, "%q went (almost) unexercised", r)
	}
}

// queryTasksBefore4599 is QueryTasks before #4599 split it (task_query.go at v1.4.91), kept to compare with.
func queryTasksBefore4599(s *Store, ctx context.Context, q *TaskQuery) (*TaskQueryResult, error) {
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
