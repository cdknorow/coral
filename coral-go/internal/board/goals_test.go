package board

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var canonicalUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func newGoal(t *testing.T, s *Store, board, title string) *Goal {
	t.Helper()
	g, err := s.CreateGoal(context.Background(), board, GoalInput{Title: title, CreatedBy: "Orchestrator"})
	require.NoError(t, err)
	return g
}

func goalSeq(t *testing.T, s *Store, board string) int64 {
	t.Helper()
	var seq int64
	err := s.db.Get(&seq, `SELECT COALESCE((SELECT seq FROM board_goal_meta WHERE board_id = ?), 0)`, board)
	require.NoError(t, err)
	return seq
}

// A goal is made with cleaned text, a uid that is a canonical lower-case UUID, and the state active unless proposed is asked for.
func TestCreateGoalCleansTextAndMakesAUID(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g, err := s.CreateGoal(ctx, "team", GoalInput{Title: "  Ship\u202e the \x1b[31mbeta\x1b[0m\u200b  ", Outcome: "line one\nline two\x07", DoneWhen: "  all\ttests   pass ", CreatedBy: "Lead"})
	require.NoError(t, err)
	assert.Equal(t, "Ship the beta", g.Title)
	assert.Equal(t, "line one\nline two", g.Outcome)
	assert.Equal(t, "all tests pass", g.DoneWhen)
	assert.Equal(t, GoalActive, g.State)
	assert.Equal(t, "Lead", g.CreatedBy)
	assert.Regexp(t, canonicalUUID, g.UID)
	assert.Nil(t, g.ClosedAt)
	assert.Equal(t, GoalRollup{}, g.Rollup)
	p, err := s.CreateGoal(ctx, "team", GoalInput{Title: "later", State: GoalProposed})
	require.NoError(t, err)
	assert.Equal(t, GoalProposed, p.State)
	assert.NotEqual(t, g.UID, p.UID)
	assert.Greater(t, p.ID, g.ID)
}

func TestCreateGoalRefusals(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, err := s.CreateGoal(ctx, "team", GoalInput{Title: "  \u200b "})
	assert.ErrorIs(t, err, ErrGoalTitleRequired)
	for _, st := range []string{GoalBlocked, GoalDone, GoalDropped, "waiting"} {
		_, err = s.CreateGoal(ctx, "team", GoalInput{Title: "x", State: st})
		assert.ErrorIs(t, err, ErrGoalBadState, st)
	}
	// The limits are in characters and refuse, never cut: the longest is accepted, one more is refused, and nothing is stored.
	for _, c := range []struct {
		field string
		max   int
		in    func(string) GoalInput
	}{
		{"title", 200, func(v string) GoalInput { return GoalInput{Title: v} }},
		{"outcome", 2000, func(v string) GoalInput { return GoalInput{Title: "t", Outcome: v} }},
		{"done_when", 300, func(v string) GoalInput { return GoalInput{Title: "t", DoneWhen: v} }},
	} {
		_, err = s.CreateGoal(ctx, "team", c.in(strings.Repeat("é", c.max)))
		assert.NoError(t, err, c.field)
		_, err = s.CreateGoal(ctx, "team", c.in(strings.Repeat("é", c.max+1)))
		require.ErrorIs(t, err, ErrGoalTextTooLong, c.field)
		assert.Contains(t, err.Error(), c.field)
	}
	var n int
	require.NoError(t, s.db.Get(&n, `SELECT COUNT(*) FROM board_goals`))
	assert.Equal(t, 3, n, "only the three longest-allowed goals were stored")
}

// A goal of another board, an unknown one and a deleted one are all the same answer.
func TestGoalsAreBoardScoped(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g := newGoal(t, s, "alpha", "A goal")
	_, err := s.GetGoal(ctx, "beta", g.ID)
	assert.ErrorIs(t, err, ErrGoalNotFound)
	_, err = s.GetGoal(ctx, "alpha", g.ID+99)
	assert.ErrorIs(t, err, ErrGoalNotFound)
	assert.Equal(t, mustErrString(s.GetGoal(ctx, "beta", g.ID)), mustErrString(s.GetGoal(ctx, "alpha", g.ID+99)), "the same words for both")
	_, err = s.UpdateGoal(ctx, "beta", g.ID, GoalUpdate{Title: gp("x")})
	assert.ErrorIs(t, err, ErrGoalNotFound)
	_, _, err = s.CloseGoal(ctx, "beta", g.ID, "because", "")
	assert.ErrorIs(t, err, ErrGoalNotFound)
	_, err = s.GoalTasks(ctx, "beta", g.ID)
	assert.ErrorIs(t, err, ErrGoalNotFound)
	got, err := s.GetGoal(ctx, "alpha", g.ID)
	require.NoError(t, err)
	assert.Equal(t, "A goal", got.Title, "the other board's attempts changed nothing")
}

func mustErrString(_ *Goal, err error) string { return err.Error() }
func gp[T any](v T) *T                        { return &v }

func TestListGoalsDefaultsToOpenAndFiltersByState(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a, b, c := newGoal(t, s, "team", "a"), newGoal(t, s, "team", "b"), newGoal(t, s, "team", "c")
	newGoal(t, s, "other", "elsewhere")
	_, err := s.UpdateGoal(ctx, "team", b.ID, GoalUpdate{State: gp(GoalBlocked), BlockedOn: gp("QA")})
	require.NoError(t, err)
	_, _, err = s.CloseGoal(ctx, "team", c.ID, "done", GoalDone)
	require.NoError(t, err)
	open, err := s.ListGoals(ctx, "team", GoalListQuery{})
	require.NoError(t, err)
	require.Len(t, open, 2)
	assert.Equal(t, []int64{a.ID, b.ID}, []int64{open[0].ID, open[1].ID}, "oldest first, the closed one and the other board's left out")
	closed, err := s.ListGoals(ctx, "team", GoalListQuery{States: []string{GoalDone, GoalDropped}})
	require.NoError(t, err)
	require.Len(t, closed, 1)
	assert.Equal(t, c.ID, closed[0].ID)
	_, err = s.ListGoals(ctx, "team", GoalListQuery{States: []string{"nonsense"}})
	assert.ErrorIs(t, err, ErrGoalBadState)
	limited, err := s.ListGoals(ctx, "team", GoalListQuery{States: []string{GoalActive, GoalBlocked, GoalDone}, Limit: 2})
	require.NoError(t, err)
	assert.Len(t, limited, 2)
}

// The rollup counts only this board's linked tasks, by what they are doing, and moves with them.
func TestGoalRollupCountsTheGoalsTasksByState(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g := newGoal(t, s, "team", "Rollup")
	other := newGoal(t, s, "team", "Other goal")
	mk := func(title string, goal *Goal) *Task {
		task, err := s.CreateTaskWithOpts(ctx, "team", title, "", "medium", "Lead", &CreateTaskOpts{GoalID: &goal.ID})
		require.NoError(t, err)
		require.NotNil(t, task.GoalID)
		assert.Equal(t, goal.ID, *task.GoalID)
		return task
	}
	queued := mk("queued", g)
	working := mk("working", g)
	finished := mk("finished", g)
	mk("another goal's", other)
	_, err := s.CreateTask(ctx, "team", "unlinked", "", "medium", "Lead")
	require.NoError(t, err)
	_, err = s.ClaimTask(ctx, "team", "Dev", working.ID)
	require.NoError(t, err)
	_, err = s.ClaimTask(ctx, "team", "Dev2", finished.ID)
	require.NoError(t, err)
	_, err = s.CompleteTask(ctx, "team", finished.ID, "Dev2", nil)
	require.NoError(t, err)
	got, err := s.GetGoal(ctx, "team", g.ID)
	require.NoError(t, err)
	assert.Equal(t, GoalRollup{Done: 1, Active: 1, Queued: 1, Total: 3}, got.Rollup)
	assert.Equal(t, queued.ID, mustTaskIDs(t, s, g.ID)[0], "the goal's task read lists them oldest first")
	goals, err := s.ListGoals(ctx, "team", GoalListQuery{})
	require.NoError(t, err)
	byID := map[int64]GoalRollup{}
	for _, x := range goals {
		byID[x.ID] = x.Rollup
	}
	assert.Equal(t, GoalRollup{Queued: 1, Total: 1}, byID[other.ID])
	_, err = s.CompleteTask(ctx, "team", working.ID, "Dev", nil)
	require.NoError(t, err)
	got, _ = s.GetGoal(ctx, "team", g.ID)
	assert.Equal(t, GoalRollup{Done: 2, Queued: 1, Total: 3}, got.Rollup)
}

func mustTaskIDs(t *testing.T, s *Store, goalID int64) []int64 {
	t.Helper()
	rows, err := s.GoalTasks(context.Background(), "team", goalID)
	require.NoError(t, err)
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
		require.NotNil(t, r.GoalID)
		assert.Equal(t, goalID, *r.GoalID)
	}
	return ids
}

func TestUpdateGoalKeepsTheStateTrue(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g := newGoal(t, s, "team", "State")
	_, err := s.UpdateGoal(ctx, "team", g.ID, GoalUpdate{State: gp(GoalBlocked)})
	assert.ErrorIs(t, err, ErrGoalBlockedOnRequired, "blocked says on whom")
	got, err := s.UpdateGoal(ctx, "team", g.ID, GoalUpdate{State: gp(GoalBlocked), BlockedOn: gp(" the\u202e operator ")})
	require.NoError(t, err)
	assert.Equal(t, GoalBlocked, got.State)
	assert.Equal(t, "the operator", got.BlockedOn)
	got, err = s.UpdateGoal(ctx, "team", g.ID, GoalUpdate{State: gp(GoalActive)})
	require.NoError(t, err)
	assert.Empty(t, got.BlockedOn, "leaving blocked clears who it waited for")
	for _, st := range []string{GoalDone, GoalDropped} {
		_, err = s.UpdateGoal(ctx, "team", g.ID, GoalUpdate{State: gp(st)})
		assert.ErrorIs(t, err, ErrGoalReasonRequired, "closing is CloseGoal, with its reason: "+st)
	}
	_, err = s.UpdateGoal(ctx, "team", g.ID, GoalUpdate{State: gp("waiting")})
	assert.ErrorIs(t, err, ErrGoalBadState)
	_, err = s.UpdateGoal(ctx, "team", g.ID, GoalUpdate{Title: gp("  ")})
	assert.ErrorIs(t, err, ErrGoalTitleRequired)
	got, err = s.UpdateGoal(ctx, "team", g.ID, GoalUpdate{Title: gp("Renamed"), Outcome: gp("It works"), DoneWhen: gp("CI is green")})
	require.NoError(t, err)
	assert.Equal(t, "Renamed", got.Title)
	assert.Equal(t, "It works", got.Outcome)
	assert.Equal(t, "CI is green", got.DoneWhen)
	assert.Equal(t, g.UID, got.UID, "the uid is never changed")
	assert.Equal(t, GoalActive, got.State)
}

// Closing needs a reason, warns by saying how many tasks are still open (and leaves them), and a closed goal can be reopened.
func TestCloseGoalWarnsOfOpenTasksAndReopens(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g := newGoal(t, s, "team", "Close")
	t1, err := s.CreateTaskWithOpts(ctx, "team", "open one", "", "medium", "Lead", &CreateTaskOpts{GoalID: &g.ID})
	require.NoError(t, err)
	t2, err := s.CreateTaskWithOpts(ctx, "team", "done one", "", "medium", "Lead", &CreateTaskOpts{GoalID: &g.ID})
	require.NoError(t, err)
	_, err = s.ClaimTask(ctx, "team", "Dev", t2.ID)
	require.NoError(t, err)
	_, err = s.CompleteTask(ctx, "team", t2.ID, "Dev", nil)
	require.NoError(t, err)
	_, _, err = s.CloseGoal(ctx, "team", g.ID, "  ", "")
	assert.ErrorIs(t, err, ErrGoalReasonRequired)
	_, _, err = s.CloseGoal(ctx, "team", g.ID, strings.Repeat("x", 301), "")
	assert.ErrorIs(t, err, ErrGoalTextTooLong)
	_, err = s.UpdateGoal(ctx, "team", g.ID, GoalUpdate{State: gp(GoalBlocked), BlockedOn: gp(strings.Repeat("x", 201))})
	assert.ErrorIs(t, err, ErrGoalTextTooLong, "blocked_on is at most 200")
	_, err = s.UpdateGoal(ctx, "team", g.ID, GoalUpdate{State: gp(GoalBlocked), BlockedOn: gp(strings.Repeat("x", 200))})
	assert.NoError(t, err)
	_, err = s.UpdateGoal(ctx, "team", g.ID, GoalUpdate{State: gp(GoalActive)})
	require.NoError(t, err)
	_, _, err = s.CloseGoal(ctx, "team", g.ID, "why", "blocked")
	assert.ErrorIs(t, err, ErrGoalBadState)
	closed, open, err := s.CloseGoal(ctx, "team", g.ID, "shipped\u202e it", "")
	require.NoError(t, err)
	assert.Equal(t, GoalDone, closed.State)
	assert.Equal(t, "shipped it", closed.ClosedReason)
	require.NotNil(t, closed.ClosedAt)
	assert.Equal(t, 1, open, "one task is still open: a warning, not a refusal")
	still, err := s.GetTask(ctx, "team", t1.ID)
	require.NoError(t, err)
	assert.Equal(t, "pending", still.Status, "the open task is as it was")
	_, _, err = s.CloseGoal(ctx, "team", g.ID, "again", "")
	assert.ErrorIs(t, err, ErrGoalAlreadyClosed)
	reopened, err := s.UpdateGoal(ctx, "team", g.ID, GoalUpdate{State: gp(GoalActive)})
	require.NoError(t, err)
	assert.Equal(t, GoalActive, reopened.State)
	assert.Nil(t, reopened.ClosedAt)
	assert.Empty(t, reopened.ClosedReason)
	dropped, _, err := s.CloseGoal(ctx, "team", g.ID, "no longer needed", GoalDropped)
	require.NoError(t, err)
	assert.Equal(t, GoalDropped, dropped.State)
}

func TestSetTaskGoalLinksOnlyWithinTheBoardAndToAnOpenGoal(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g := newGoal(t, s, "alpha", "alpha goal")
	foreign := newGoal(t, s, "beta", "beta goal")
	task, err := s.CreateTask(ctx, "alpha", "a task", "", "medium", "Lead")
	require.NoError(t, err)
	require.NoError(t, s.SetTaskGoal(ctx, "alpha", task.ID, &g.ID))
	got, _ := s.GetTask(ctx, "alpha", task.ID)
	require.NotNil(t, got.GoalID)
	assert.Equal(t, g.ID, *got.GoalID)
	assert.ErrorIs(t, s.SetTaskGoal(ctx, "alpha", task.ID, &foreign.ID), ErrGoalNotFound, "a goal of another board looks like no goal")
	missing := int64(9999)
	assert.ErrorIs(t, s.SetTaskGoal(ctx, "alpha", task.ID, &missing), ErrGoalNotFound)
	assert.ErrorIs(t, s.SetTaskGoal(ctx, "beta", task.ID, &foreign.ID), ErrGoalTaskNotFound, "a task of another board is not found")
	assert.ErrorIs(t, s.SetTaskGoal(ctx, "alpha", 424242, &g.ID), ErrGoalTaskNotFound)
	got, _ = s.GetTask(ctx, "alpha", task.ID)
	require.NotNil(t, got.GoalID)
	assert.Equal(t, g.ID, *got.GoalID, "the refusals changed nothing")
	_, _, err = s.CloseGoal(ctx, "alpha", g.ID, "done", "")
	require.NoError(t, err)
	other, _ := s.CreateTask(ctx, "alpha", "second", "", "medium", "Lead")
	assert.ErrorIs(t, s.SetTaskGoal(ctx, "alpha", other.ID, &g.ID), ErrGoalClosed)
	require.NoError(t, s.SetTaskGoal(ctx, "alpha", task.ID, nil), "unlinking is always allowed")
	got, _ = s.GetTask(ctx, "alpha", task.ID)
	assert.Nil(t, got.GoalID)
}

// A refused goal leaves no task behind.
func TestCreateTaskWithAForeignOrClosedGoalStoresNothing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	foreign := newGoal(t, s, "beta", "beta goal")
	closed := newGoal(t, s, "alpha", "closing")
	_, _, err := s.CloseGoal(ctx, "alpha", closed.ID, "done", "")
	require.NoError(t, err)
	missing := int64(777)
	for name, id := range map[string]int64{"another board's": foreign.ID, "closed": closed.ID, "unknown": missing} {
		_, err := s.CreateTaskWithOpts(ctx, "alpha", "refused", "", "medium", "Lead", &CreateTaskOpts{GoalID: &id})
		require.Error(t, err, name)
	}
	var n int
	require.NoError(t, s.db.Get(&n, `SELECT COUNT(*) FROM board_tasks`))
	assert.Zero(t, n, "no task was stored")
}

// A retry of a task that has a goal continues under it, while the goal is open; an explicit goal wins.
func TestARetryInheritsItsTasksGoal(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g := newGoal(t, s, "team", "Retry goal")
	h := newGoal(t, s, "team", "Another")
	build, err := s.CreateTaskWithOpts(ctx, "team", "Build", "", "medium", "Lead", &CreateTaskOpts{GoalID: &g.ID})
	require.NoError(t, err)
	_, err = s.ClaimTask(ctx, "team", "builder", build.ID)
	require.NoError(t, err)
	_, err = s.CompleteTaskWithArtifacts(ctx, "team", build.ID, "builder", nil, "failed", nil)
	require.NoError(t, err)
	retry, err := s.CreateTaskWithOpts(ctx, "team", "Build retry", "", "medium", "Lead", &CreateTaskOpts{Workflow: TaskWorkflow{RetryOf: build.ID}})
	require.NoError(t, err)
	require.NotNil(t, retry.GoalID)
	assert.Equal(t, g.ID, *retry.GoalID)
	explicit, err := s.CreateTaskWithOpts(ctx, "team", "Build retry 2", "", "medium", "Lead", &CreateTaskOpts{GoalID: &h.ID, Workflow: TaskWorkflow{RetryOf: build.ID}})
	require.NoError(t, err)
	require.NotNil(t, explicit.GoalID)
	assert.Equal(t, h.ID, *explicit.GoalID)
	_, _, err = s.CloseGoal(ctx, "team", g.ID, "done", "")
	require.NoError(t, err)
	late, err := s.CreateTaskWithOpts(ctx, "team", "Build retry 3", "", "medium", "Lead", &CreateTaskOpts{Workflow: TaskWorkflow{RetryOf: build.ID}})
	require.NoError(t, err)
	assert.Nil(t, late.GoalID, "a closed goal is not inherited")
}

// A board made now has a canonical lower-case UUID of its own; one made before has none (no backfill); it survives a re-key and cannot be changed.
func TestBoardUIDIsMadeAtCreateAndNeverChanges(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	b, err := s.CreateBoard(ctx, "Sprint", "Operator")
	require.NoError(t, err)
	require.NotNil(t, b.UID)
	assert.Regexp(t, canonicalUUID, *b.UID)
	b2, err := s.CreateBoard(ctx, "Sprint", "Operator")
	require.NoError(t, err)
	assert.NotEqual(t, *b.UID, *b2.UID)
	got, err := s.GetBoard(ctx, b.ID)
	require.NoError(t, err)
	require.NotNil(t, got.UID)
	assert.Equal(t, *b.UID, *got.UID)
	// A board that exists because something used its name, as every board did before ids, has no uid.
	_, err = s.CreateTask(ctx, "legacy-board", "t", "", "medium", "Lead")
	require.NoError(t, err)
	legacy, err := s.GetBoard(ctx, "legacy-board")
	require.NoError(t, err)
	assert.Nil(t, legacy.UID, "an older board is not backfilled")
	// Never changed: not by a statement, once it is set.
	_, err = s.db.Exec(`UPDATE boards SET board_uid = ? WHERE id = ?`, "00000000-0000-4000-8000-000000000000", b.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "never changed")
	// Nor by a re-key of the board's id: the uid stays and the board's goals follow the new id.
	g := newGoal(t, s, b.ID, "Goal on a board that is re-keyed")
	require.NoError(t, s.rekeyBoard(ctx, b.ID))
	var newID string
	require.NoError(t, s.db.Get(&newID, `SELECT id FROM boards WHERE board_uid = ?`, *b.UID))
	assert.NotEqual(t, b.ID, newID)
	moved, err := s.GetGoal(ctx, newID, g.ID)
	require.NoError(t, err, "the goal moved with the board's key")
	assert.Equal(t, g.UID, moved.UID)
	_, err = s.GetGoal(ctx, b.ID, g.ID)
	assert.ErrorIs(t, err, ErrGoalNotFound)
	assert.Equal(t, int64(0), goalSeq(t, s, b.ID), "the counter moved too")
	assert.Greater(t, goalSeq(t, s, newID), int64(0))
	// A uid cannot be shared between two boards.
	_, err = s.db.Exec(`UPDATE boards SET board_uid = ? WHERE id = ?`, *b2.UID, "legacy-board")
	require.Error(t, err)
}

func TestAGoalUIDIsNeverChanged(t *testing.T) {
	s := testStore(t)
	g := newGoal(t, s, "team", "uid")
	_, err := s.db.Exec(`UPDATE board_goals SET goal_uid = ? WHERE id = ?`, "00000000-0000-4000-8000-000000000000", g.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "never changed")
	h := newGoal(t, s, "team", "two")
	_, err = s.db.Exec(`INSERT INTO board_goals (goal_uid, board_id, title, created_at, updated_at) VALUES (?, 'team', 'dup', 'x', 'x')`, h.UID)
	require.Error(t, err, "goal uids are unique")
}

// The board's change counter is moved by the database on every goal write and every task change that touches a goal, and by nothing else.
func TestGoalSeqMovesOnGoalAndLinkedTaskChangesOnly(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	assert.Zero(t, goalSeq(t, s, "team"))
	g := newGoal(t, s, "team", "Seq")
	seq := goalSeq(t, s, "team")
	assert.Positive(t, seq, "creating a goal moves it")
	moved := func(what string) {
		t.Helper()
		now := goalSeq(t, s, "team")
		assert.Greater(t, now, seq, what)
		seq = now
	}
	still := func(what string) {
		t.Helper()
		assert.Equal(t, seq, goalSeq(t, s, "team"), what)
	}
	_, err := s.UpdateGoal(ctx, "team", g.ID, GoalUpdate{Title: gp("Seq2")})
	require.NoError(t, err)
	moved("a goal edit")
	plain, err := s.CreateTask(ctx, "team", "plain", "", "medium", "Lead")
	require.NoError(t, err)
	_, err = s.ClaimTask(ctx, "team", "Dev", plain.ID)
	require.NoError(t, err)
	_, err = s.CompleteTask(ctx, "team", plain.ID, "Dev", nil)
	require.NoError(t, err)
	still("a task with no goal changes without moving it")
	linked, err := s.CreateTaskWithOpts(ctx, "team", "linked", "", "medium", "Lead", &CreateTaskOpts{GoalID: &g.ID})
	require.NoError(t, err)
	moved("a task made under the goal")
	_, err = s.ClaimTask(ctx, "team", "Dev", linked.ID)
	require.NoError(t, err)
	moved("a linked task claimed")
	_, err = s.CompleteTask(ctx, "team", linked.ID, "Dev", nil)
	require.NoError(t, err)
	moved("a linked task completed")
	require.NoError(t, s.SetTaskGoal(ctx, "team", plain.ID, &g.ID))
	moved("a task linked")
	require.NoError(t, s.SetTaskGoal(ctx, "team", plain.ID, nil))
	moved("a task unlinked")
	_, _, err = s.CloseGoal(ctx, "team", g.ID, "done", "")
	require.NoError(t, err)
	moved("a goal closed")
	other := goalSeq(t, s, "another")
	newGoal(t, s, "another", "elsewhere")
	assert.Greater(t, goalSeq(t, s, "another"), other)
	still("another board's goal does not move this board's counter")
}

// Deleting a board removes its goals and its counter, and a board that holds only goals is not "unused".
func TestDeletingABoardRemovesItsGoals(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g := newGoal(t, s, "team", "goes with the board")
	keep := newGoal(t, s, "keep", "stays")
	deleted, err := s.DeleteProjectIfUnused(ctx, "team")
	require.NoError(t, err)
	assert.False(t, deleted, "a board with a goal is in use")
	require.NoError(t, s.DeleteProject(ctx, "team"))
	_, err = s.GetGoal(ctx, "team", g.ID)
	assert.ErrorIs(t, err, ErrGoalNotFound)
	assert.Zero(t, goalSeq(t, s, "team"))
	_, err = s.GetGoal(ctx, "keep", keep.ID)
	assert.NoError(t, err, "another board's goal stays")
}

// The schema step is safe to run again, and an old database (no goal_id, no board_uid, no goal tables) gains them without losing a row.
func TestGoalsSchemaIsIdempotentAndUpgradesAnOldDatabase(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	require.NoError(t, s.ensureGoalsSchema(ctx))
	require.NoError(t, s.ensureGoalsSchema(ctx))
	task, err := s.CreateTask(ctx, "oldboard", "an old task", "", "medium", "Lead")
	require.NoError(t, err)
	for _, ddl := range []string{
		`DROP TRIGGER board_tasks_goal_seq_insert`, `DROP TRIGGER board_tasks_goal_seq_update`, `DROP TRIGGER board_tasks_goal_seq_delete`,
		`DROP TRIGGER board_goals_seq_insert`, `DROP TRIGGER board_goals_seq_update`, `DROP TRIGGER board_goals_seq_delete`, `DROP TRIGGER board_goals_uid_immutable`, `DROP TRIGGER boards_board_uid_immutable`,
		`DROP INDEX idx_board_tasks_goal`, `DROP INDEX idx_boards_board_uid`, `DROP INDEX idx_board_goals_uid`, `DROP INDEX idx_board_goals_board`,
		`DROP TABLE board_goals`, `DROP TABLE board_goal_meta`,
		`ALTER TABLE board_tasks DROP COLUMN goal_id`, `ALTER TABLE boards DROP COLUMN board_uid`,
	} {
		_, err := s.db.Exec(ddl)
		require.NoError(t, err, ddl)
	}
	require.NoError(t, s.ensureGoalsSchema(ctx))
	got, err := s.getTaskByID(ctx, "oldboard", task.ID)
	require.NoError(t, err)
	assert.Equal(t, "an old task", got.Title, "the old task is untouched")
	assert.Nil(t, got.GoalID)
	legacy, err := s.GetBoard(ctx, "oldboard")
	require.NoError(t, err)
	assert.Nil(t, legacy.UID, "the old board reads a null uid, and the migration made none")
	g := newGoal(t, s, "oldboard", "after the upgrade")
	require.NoError(t, s.SetTaskGoal(ctx, "oldboard", task.ID, &g.ID))
}

// A goal's driver is a subscriber of its board; a blocked goal can say what it needs, what is recommended and what happens if no one answers: one cleaned line each, cleared with blocked.
func TestGoalDriverAndTheBlockedDecisionFields(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, err := s.CreateGoal(ctx, "team", GoalInput{Title: "no such driver", Driver: "Nobody"})
	assert.ErrorIs(t, err, ErrGoalDriverUnknown)
	_, err = sub(s, ctx, "team", "Orchestrator", "Orchestrator")
	require.NoError(t, err)
	_, err = sub(s, ctx, "other", "Elsewhere", "Dev")
	require.NoError(t, err)
	_, err = s.CreateGoal(ctx, "team", GoalInput{Title: "another board's subscriber", Driver: "Elsewhere"})
	assert.ErrorIs(t, err, ErrGoalDriverUnknown, "a subscriber of another board is not a driver here")
	g, err := s.CreateGoal(ctx, "team", GoalInput{Title: "driven", Driver: " Orchestrator\u202e "})
	require.NoError(t, err)
	assert.Equal(t, "Orchestrator", g.Driver)
	_, err = s.UpdateGoal(ctx, "team", g.ID, GoalUpdate{Driver: gp("Ghost")})
	assert.ErrorIs(t, err, ErrGoalDriverUnknown)
	got, err := s.UpdateGoal(ctx, "team", g.ID, GoalUpdate{Driver: gp("")})
	require.NoError(t, err)
	assert.Empty(t, got.Driver, "an empty driver clears it")
	got, err = s.UpdateGoal(ctx, "team", g.ID, GoalUpdate{Driver: gp("Orchestrator")})
	require.NoError(t, err)
	assert.Equal(t, "Orchestrator", got.Driver)

	got, err = s.UpdateGoal(ctx, "team", g.ID, GoalUpdate{State: gp(GoalBlocked), BlockedOn: gp("the operator"), Decision: gp("  Ship\u202e on Friday?\n"), Recommendation: gp("yes"), IfSilent: gp("we ship Monday")})
	require.NoError(t, err)
	assert.Equal(t, "Ship on Friday?", got.Decision)
	assert.Equal(t, "yes", got.Recommendation)
	assert.Equal(t, "we ship Monday", got.IfSilent)
	assert.Equal(t, "Orchestrator", got.Driver, "the driver stays")
	for _, f := range []struct {
		name string
		u    func(string) GoalUpdate
	}{
		{"decision", func(v string) GoalUpdate { return GoalUpdate{Decision: gp(v)} }},
		{"recommendation", func(v string) GoalUpdate { return GoalUpdate{Recommendation: gp(v)} }},
		{"if_silent", func(v string) GoalUpdate { return GoalUpdate{IfSilent: gp(v)} }},
	} {
		_, err = s.UpdateGoal(ctx, "team", g.ID, f.u(strings.Repeat("x", 301)))
		require.ErrorIs(t, err, ErrGoalTextTooLong, f.name)
		assert.Contains(t, err.Error(), f.name)
		_, err = s.UpdateGoal(ctx, "team", g.ID, f.u(strings.Repeat("x", 300)))
		assert.NoError(t, err, f.name)
	}
	got, err = s.UpdateGoal(ctx, "team", g.ID, GoalUpdate{State: gp(GoalActive)})
	require.NoError(t, err)
	assert.Empty(t, got.BlockedOn+got.Decision+got.Recommendation+got.IfSilent, "leaving blocked clears all four")
	// Fields given while the goal is not blocked are dropped, not kept for later.
	got, err = s.UpdateGoal(ctx, "team", g.ID, GoalUpdate{Decision: gp("stale")})
	require.NoError(t, err)
	assert.Empty(t, got.Decision)
	// Closing a blocked goal clears them too.
	_, err = s.UpdateGoal(ctx, "team", g.ID, GoalUpdate{State: gp(GoalBlocked), BlockedOn: gp("QA"), IfSilent: gp("we wait")})
	require.NoError(t, err)
	closed, _, err := s.CloseGoal(ctx, "team", g.ID, "dropped it", GoalDropped)
	require.NoError(t, err)
	assert.Empty(t, closed.BlockedOn+closed.IfSilent)
	assert.Equal(t, "Orchestrator", closed.Driver)
	listed, err := s.ListGoals(ctx, "team", GoalListQuery{States: []string{GoalDropped}})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, "Orchestrator", listed[0].Driver, "the list read carries the driver")
}

func TestGoalSummaryCountsOpenGoalsAndCarriesTheCounter(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	open, seq, err := s.GoalSummary(ctx, "team")
	require.NoError(t, err)
	assert.Zero(t, open)
	assert.Zero(t, seq)
	a, b := newGoal(t, s, "team", "a"), newGoal(t, s, "team", "b")
	newGoal(t, s, "other", "elsewhere")
	open, seq1, err := s.GoalSummary(ctx, "team")
	require.NoError(t, err)
	assert.Equal(t, 2, open)
	assert.Positive(t, seq1)
	_, _, err = s.CloseGoal(ctx, "team", b.ID, "done", "")
	require.NoError(t, err)
	open, seq2, err := s.GoalSummary(ctx, "team")
	require.NoError(t, err)
	assert.Equal(t, 1, open)
	assert.Greater(t, seq2, seq1)
	_, err = s.UpdateGoal(ctx, "team", a.ID, GoalUpdate{State: gp(GoalProposed)})
	require.NoError(t, err)
	open, _, _ = s.GoalSummary(ctx, "team")
	assert.Equal(t, 1, open, "proposed counts as open")
}
