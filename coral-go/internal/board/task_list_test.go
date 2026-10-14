package board

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// doneTask creates, claims and completes a task.
func doneTask(t *testing.T, s *Store, project, title string) *Task {
	t.Helper()
	ctx := context.Background()
	task, err := s.CreateTask(ctx, project, title, "", "medium", "lead", "worker")
	require.NoError(t, err)
	_, err = s.ClaimTask(ctx, project, "worker", task.ID)
	require.NoError(t, err)
	_, err = s.CompleteTask(ctx, project, task.ID, "worker", nil)
	require.NoError(t, err)
	return task
}

func ids(items []TaskListItem) []int64 {
	out := make([]int64, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

// collect pages a filter to the end and returns every id, in order.
func collect(t *testing.T, s *Store, project, status string, limit int) []int64 {
	t.Helper()
	var all []int64
	cursor := ""
	for pages := 0; ; pages++ {
		require.Less(t, pages, 1000, "paging must end")
		page, err := s.ListTaskPage(context.Background(), project, TaskListQuery{Status: status, Limit: limit, Cursor: cursor})
		require.NoError(t, err)
		require.LessOrEqual(t, len(page.Tasks), limit)
		all = append(all, ids(page.Tasks)...)
		if page.NextCursor == "" {
			return all
		}
		cursor = page.NextCursor
	}
}

// #3269: a list item carries list fields only. The 17.6 MB list was mostly workflow text
// repeated per task; the body and completion message stay at the task-detail endpoint.
func TestListTaskPage_ItemsCarryListFieldsOnly(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	long := strings.Repeat("b", 5000)
	upstream, err := s.CreateTask(ctx, "team", "Upstream", long, "high", "lead", "worker")
	require.NoError(t, err)
	_, err = s.ClaimTask(ctx, "team", "worker", upstream.ID)
	require.NoError(t, err)
	msg := strings.Repeat("m", 5000)
	_, err = s.CompleteTaskWithArtifacts(ctx, "team", upstream.ID, "worker", &msg, "failed", nil)
	require.NoError(t, err)
	_, err = s.CreateTaskWithOpts(ctx, "team", "Downstream", long, "low", "lead",
		&CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: upstream.ID, BoardID: "team"}}})
	require.NoError(t, err)
	review, err := s.CreateTask(ctx, "team", "Under review", "short body", "medium", "lead", "worker")
	require.NoError(t, err)
	_, err = s.ClaimTask(ctx, "team", "worker", review.ID)
	require.NoError(t, err)
	_, err = s.SubmitCompletionReview(ctx, "team", review.ID, "worker", "please check", "success", "needs a second look", nil)
	require.NoError(t, err)

	page, err := s.ListTaskPage(ctx, "team", TaskListQuery{})
	require.NoError(t, err)
	byTitle := map[string]TaskListItem{}
	for _, it := range page.Tasks {
		byTitle[it.Title] = it
	}
	require.Len(t, byTitle, 3)

	assert.Equal(t, "failed", byTitle["Upstream"].Outcome, "the row shows a failed outcome")
	assert.Len(t, byTitle["Upstream"].BodyPreview, 200, "the body is cut to the tooltip preview")
	assert.True(t, byTitle["Under review"].ReviewRequested)
	assert.Equal(t, "short body", byTitle["Under review"].BodyPreview)
	require.Len(t, byTitle["Downstream"].BlockedBy, 1)
	assert.Equal(t, upstream.ID, byTitle["Downstream"].BlockedBy[0].TaskID)
	assert.False(t, byTitle["Downstream"].ReviewRequested)

	raw, err := json.Marshal(page)
	require.NoError(t, err)
	for _, field := range []string{`"body"`, `"completion_message"`, `"workflow"`, `"instructions"`} {
		assert.NotContains(t, string(raw), field)
	}
	assert.NotContains(t, string(raw), long[:300])
	assert.NotContains(t, string(raw), msg[:300])
}

// With no filter: every open task (priority order), then the newest done tasks; the
// cursor continues the done tasks; totals count the whole board, never another board.
func TestListTaskPage_DefaultIsOpenTasksThenNewestDone(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	var done []int64
	for i := 0; i < 7; i++ {
		done = append(done, doneTask(t, s, "team", fmt.Sprintf("done %d", i)).ID)
	}
	low, err := s.CreateTask(ctx, "team", "low", "", "low", "lead")
	require.NoError(t, err)
	crit, err := s.CreateTask(ctx, "team", "critical", "", "critical", "lead")
	require.NoError(t, err)
	doneTask(t, s, "other", "other board")

	page, err := s.ListTaskPage(ctx, "team", TaskListQuery{Limit: 3})
	require.NoError(t, err)
	// The done tasks were finished within the same second or two; ties go newest id first.
	assert.Equal(t, []int64{crit.ID, low.ID, done[6], done[5], done[4]}, ids(page.Tasks))
	assert.Equal(t, 2, page.TotalOpen)
	assert.Equal(t, 7, page.TotalCompleted)
	require.NotEmpty(t, page.NextCursor)

	rest := []int64{}
	cursor := page.NextCursor
	for cursor != "" {
		p, err := s.ListTaskPage(ctx, "team", TaskListQuery{Status: TaskListCompleted, Limit: 3, Cursor: cursor})
		require.NoError(t, err)
		rest = append(rest, ids(p.Tasks)...)
		cursor = p.NextCursor
	}
	assert.Equal(t, []int64{done[3], done[2], done[1], done[0]}, rest)
}

// Tasks added and finished while a client pages never repeat or drop a row from the
// pages after its cursor: a newly finished task has the newest completed_at, so it is
// on page one, and new tasks have higher ids.
func TestListTaskPage_CursorIsStableWhileTasksChange(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	var done []int64
	for i := 0; i < 10; i++ {
		done = append(done, doneTask(t, s, "team", fmt.Sprintf("done %d", i)).ID)
	}
	open, err := s.CreateTask(ctx, "team", "open", "", "medium", "lead", "worker")
	require.NoError(t, err)

	first, err := s.ListTaskPage(ctx, "team", TaskListQuery{Status: TaskListCompleted, Limit: 4})
	require.NoError(t, err)
	seen := ids(first.Tasks)

	// The board changes between pages.
	_, err = s.ClaimTask(ctx, "team", "worker", open.ID)
	require.NoError(t, err)
	_, err = s.CompleteTask(ctx, "team", open.ID, "worker", nil)
	require.NoError(t, err)
	doneTask(t, s, "team", "finished later")
	_, err = s.CreateTask(ctx, "team", "new", "", "medium", "lead")
	require.NoError(t, err)

	cursor := first.NextCursor
	for cursor != "" {
		p, err := s.ListTaskPage(ctx, "team", TaskListQuery{Status: TaskListCompleted, Limit: 4, Cursor: cursor})
		require.NoError(t, err)
		seen = append(seen, ids(p.Tasks)...)
		cursor = p.NextCursor
	}
	want := make([]int64, 0, len(done))
	for i := len(done) - 1; i >= 0; i-- {
		want = append(want, done[i])
	}
	assert.Equal(t, want, seen, "every task done before paging, once, in order")

	again, err := s.ListTaskPage(ctx, "team", TaskListQuery{Status: TaskListCompleted, Limit: 4})
	require.NoError(t, err)
	assert.Contains(t, ids(again.Tasks), open.ID, "a task finished meanwhile is on page one")
}

// status=all and status=open list every task of their kind exactly once across pages.
func TestListTaskPage_AllAndOpenPageEveryTaskOnce(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	var everyID, openIDs []int64
	for i := 0; i < 12; i++ {
		if i%3 == 0 {
			everyID = append(everyID, doneTask(t, s, "team", fmt.Sprintf("done %d", i)).ID)
			continue
		}
		task, err := s.CreateTask(ctx, "team", fmt.Sprintf("open %d", i), "", "medium", "lead")
		require.NoError(t, err)
		everyID = append(everyID, task.ID)
		openIDs = append(openIDs, task.ID)
	}
	doneTask(t, s, "other", "other board")

	all := collect(t, s, "team", TaskListAll, 5)
	want := make([]int64, len(everyID))
	for i := range everyID {
		want[i] = everyID[len(everyID)-1-i]
	}
	assert.Equal(t, want, all, "all: newest id first")
	assert.Equal(t, openIDs, collect(t, s, "team", TaskListOpen, 3), "open: oldest id first")
}

func TestListTaskPage_LimitsAndBadQueries(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	for i := 0; i < TaskListMaxLimit+5; i++ {
		_, err := s.CreateTask(ctx, "team", fmt.Sprintf("t%d", i), "", "medium", "lead")
		require.NoError(t, err)
	}
	p, err := s.ListTaskPage(ctx, "team", TaskListQuery{Status: TaskListAll})
	require.NoError(t, err)
	assert.Len(t, p.Tasks, TaskListDefaultLimit)
	p, err = s.ListTaskPage(ctx, "team", TaskListQuery{Status: TaskListAll, Limit: 10000})
	require.NoError(t, err)
	assert.Len(t, p.Tasks, TaskListMaxLimit)
	openCursor := p.NextCursor
	require.NotEmpty(t, openCursor)

	for name, q := range map[string]TaskListQuery{
		"unknown status":             {Status: "done"},
		"garbage cursor":             {Status: TaskListCompleted, Cursor: "!!!"},
		"cursor of another filter":   {Status: TaskListCompleted, Cursor: openCursor},
		"cursor on the default view": {Cursor: openCursor},
		"a cursor from before #3342": {Status: TaskListAll, Cursor: base64.RawURLEncoding.EncodeToString([]byte("a||5"))},
		"forged cursor":              {Status: TaskListAll, Cursor: encodeTaskCursor("team", "a", "", -1)},
	} {
		_, err := s.ListTaskPage(ctx, "team", q)
		assert.ErrorIs(t, err, ErrBadTaskListQuery, name)
	}
}

// A page of done tasks comes from the partial index, newest first, without sorting the
// board's whole history: the page costs O(limit).
func TestListTaskPage_DonePageUsesTheIndex(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	rows, err := s.db.QueryxContext(ctx, `EXPLAIN QUERY PLAN SELECT `+taskListColumns+`
		 WHERE t.board_id = ? AND t.`+doneTaskStatusSQL+` AND (t.completed_at < ? OR (t.completed_at = ? AND t.id < ?) OR t.completed_at IS NULL)
		 ORDER BY t.completed_at DESC, t.id DESC LIMIT ?`, "team", "x", "x", 1, 51)
	require.NoError(t, err)
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &notused, &detail))
		plan = append(plan, detail)
	}
	joined := strings.Join(plan, "\n")
	assert.Contains(t, joined, "idx_board_tasks_done", joined)
	assert.NotContains(t, joined, "TEMP B-TREE", "no sort of the whole history:\n"+joined)
}

// #3342: a cursor is bound to the board that issued it. A cursor from board A is refused on
// board B (it used to offset B's pages); on A it still pages.
func TestListTaskPage_CursorIsBoundToItsBoard(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	for _, board := range []string{"alpha", "beta"} {
		for i := 0; i < 5; i++ {
			_, err := s.CreateTask(ctx, board, fmt.Sprintf("%s-%d", board, i), "", "medium", "lead")
			require.NoError(t, err)
		}
	}
	for _, status := range []string{TaskListAll, TaskListOpen} {
		p, err := s.ListTaskPage(ctx, "alpha", TaskListQuery{Status: status, Limit: 2})
		require.NoError(t, err)
		require.NotEmpty(t, p.NextCursor, status)

		_, err = s.ListTaskPage(ctx, "beta", TaskListQuery{Status: status, Limit: 2, Cursor: p.NextCursor})
		assert.ErrorIs(t, err, ErrBadTaskListQuery, status+": alpha's cursor on beta")

		next, err := s.ListTaskPage(ctx, "alpha", TaskListQuery{Status: status, Limit: 2, Cursor: p.NextCursor})
		require.NoError(t, err, status)
		assert.Len(t, next.Tasks, 2, status+": alpha's cursor still pages alpha")
	}
	// Done tasks page by completed_at: the same binding.
	_, err := s.ListTaskPage(ctx, "beta", TaskListQuery{Status: TaskListCompleted, Cursor: encodeTaskCursor("alpha", "d", "2026-10-14T06:00:00Z", 3)})
	assert.ErrorIs(t, err, ErrBadTaskListQuery, "done: alpha's cursor on beta")
	_, err = s.ListTaskPage(ctx, "alpha", TaskListQuery{Status: TaskListCompleted, Cursor: encodeTaskCursor("alpha", "d", "2026-10-14T06:00:00Z", 3)})
	assert.NoError(t, err, "done: alpha's cursor on alpha")
	// A board name with the separator in it still round-trips.
	_, id, err := decodeTaskCursor(encodeTaskCursor("a|b", "a", "", 9), "a|b", "a")
	require.NoError(t, err)
	assert.Equal(t, int64(9), id)
}
