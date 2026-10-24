package board

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setRequireGoal(t *testing.T, s *Store, board string, on bool) {
	t.Helper()
	_, err := s.UpdateBoardSettings(context.Background(), board, BoardSettingsUpdate{RequireGoal: &on})
	require.NoError(t, err)
}

func taskCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	require.NoError(t, s.db.Get(&n, `SELECT COUNT(*) FROM board_tasks`))
	return n
}

// #7116: the setting is off by default and round-trips through the board settings.
func TestRequireGoalIsOffByDefaultAndStored(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	got, err := s.GetBoardSettings(ctx, "team")
	require.NoError(t, err)
	assert.False(t, got.RequireGoal)
	setRequireGoal(t, s, "team", true)
	got, _ = s.GetBoardSettings(ctx, "team")
	assert.True(t, got.RequireGoal)
	other, _ := s.GetBoardSettings(ctx, "other")
	assert.False(t, other.RequireGoal, "per board")
	setRequireGoal(t, s, "team", false)
	got, _ = s.GetBoardSettings(ctx, "team")
	assert.False(t, got.RequireGoal)
}

// On: a task with no goal is refused and nothing is stored (drafts too); with a goal it is made; off: as before.
func TestARequireGoalBoardRefusesATaskWithNoGoalAndStoresNothing(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g := newGoal(t, s, "team", "Ship")
	setRequireGoal(t, s, "team", true)

	for _, opts := range []*CreateTaskOpts{nil, {Draft: true}, {Workflow: TaskWorkflow{Name: "Build"}}} {
		_, err := s.CreateTaskWithOpts(ctx, "team", "no goal", "", "medium", "Lead", opts)
		require.ErrorIs(t, err, ErrGoalRequired)
	}
	assert.Zero(t, taskCount(t, s), "nothing was stored")
	task, err := s.CreateTaskWithOpts(ctx, "team", "with goal", "", "medium", "Lead", &CreateTaskOpts{GoalID: &g.ID})
	require.NoError(t, err)
	require.NotNil(t, task.GoalID)

	// Another board is unaffected, and so is this one once it is off.
	_, err = s.CreateTaskWithOpts(ctx, "other", "free", "", "medium", "Lead", nil)
	require.NoError(t, err)
	setRequireGoal(t, s, "team", false)
	_, err = s.CreateTaskWithOpts(ctx, "team", "free again", "", "medium", "Lead", nil)
	require.NoError(t, err)
}

// A task is never moved to no goal on a board that requires one; moving to another goal is fine; off, unlinking is as before; tasks that had no goal stay as they are.
func TestARequireGoalBoardNeverMovesATaskToNoGoal(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	old, err := s.CreateTaskWithOpts(ctx, "team", "made before", "", "medium", "Lead", nil)
	require.NoError(t, err)
	g1, g2 := newGoal(t, s, "team", "One"), newGoal(t, s, "team", "Two")
	linked, err := s.CreateTaskWithOpts(ctx, "team", "linked", "", "medium", "Lead", &CreateTaskOpts{GoalID: &g1.ID})
	require.NoError(t, err)
	setRequireGoal(t, s, "team", true)

	require.ErrorIs(t, s.SetTaskGoal(ctx, "team", linked.ID, nil), ErrGoalRequired)
	require.NoError(t, s.SetTaskGoal(ctx, "team", linked.ID, &g2.ID))
	got, err := s.getTaskByID(ctx, "team", linked.ID)
	require.NoError(t, err)
	assert.Equal(t, g2.ID, *got.GoalID)
	require.NoError(t, s.SetTaskGoal(ctx, "team", old.ID, &g1.ID), "a task made before may be given one")
	again, _ := s.getTaskByID(ctx, "team", old.ID)
	require.NotNil(t, again.GoalID)

	setRequireGoal(t, s, "team", false)
	require.NoError(t, s.SetTaskGoal(ctx, "team", linked.ID, nil))
	// An existing task with no goal was never touched by turning the rule on: it lists as it was.
	other, err := s.CreateTaskWithOpts(ctx, "team", "never had one", "", "medium", "Lead", nil)
	require.NoError(t, err)
	setRequireGoal(t, s, "team", true)
	still, _ := s.getTaskByID(ctx, "team", other.ID)
	assert.Nil(t, still.GoalID)
}

// A retry of a task that has a goal inherits it, so a board that requires goals does not break its own retries; a retry of a task with no goal (or a closed goal) is refused like any task with none; Coral's own review
// tasks are exempt, or such a board could not review its work.
func TestRequireGoalRetriesInheritAndReviewTasksAreExempt(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	g := newGoal(t, s, "team", "Retry goal")
	build, err := s.CreateTaskWithOpts(ctx, "team", "Build", "", "medium", "Lead", &CreateTaskOpts{GoalID: &g.ID})
	require.NoError(t, err)
	_, err = s.ClaimTask(ctx, "team", "builder", build.ID)
	require.NoError(t, err)
	_, err = s.CompleteTaskWithArtifacts(ctx, "team", build.ID, "builder", nil, "failed", nil)
	require.NoError(t, err)
	setRequireGoal(t, s, "team", true)

	retry, err := s.CreateTaskWithOpts(ctx, "team", "Build retry", "", "medium", "Lead", &CreateTaskOpts{Workflow: TaskWorkflow{RetryOf: build.ID}})
	require.NoError(t, err)
	require.NotNil(t, retry.GoalID)
	assert.Equal(t, g.ID, *retry.GoalID, "the retry inherits")

	_, _, err = s.CloseGoal(ctx, "team", g.ID, "done", "")
	require.NoError(t, err)
	_, err = s.CreateTaskWithOpts(ctx, "team", "Build retry 2", "", "medium", "Lead", &CreateTaskOpts{Workflow: TaskWorkflow{RetryOf: build.ID}})
	require.ErrorIs(t, err, ErrGoalRequired, "a closed goal is not inherited: the retry has none")

	// A review task is Coral's own: exempt.
	rt, err := s.CreateTaskWithOpts(ctx, "team", "Review #1: x", "", "medium", "Lead", &CreateTaskOpts{Workflow: TaskWorkflow{ReviewOf: build.ID, ReviewRound: 1}}, "QA")
	require.NoError(t, err)
	assert.Nil(t, rt.GoalID)
}

// A setting that cannot be read is an error, never "off": the check fails closed.
func TestRequireGoalFailsClosedWhenTheSettingCannotBeRead(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, err := s.db.ExecContext(ctx, `DROP TABLE board_settings`)
	require.NoError(t, err)
	_, err = s.CreateTaskWithOpts(ctx, "team", "x", "", "medium", "Lead", nil)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrGoalRequired)
	assert.Zero(t, taskCount(t, s))
}

// #7116: the projects list carries each board's goals as a count of the open ones and the change counter, no text.
func TestProjectsListCarriesTheGoalsBrief(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, err := s.PostMessage(ctx, "team", "Operator", "hello", nil)
	require.NoError(t, err)
	list, err := s.ListProjects(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.NotNil(t, list[0].Goals)
	assert.Equal(t, GoalsBrief{}, *list[0].Goals, "no goals: 0 and 0")
	g := newGoal(t, s, "team", "Ship")
	h := newGoal(t, s, "team", "Later")
	_, _, err = s.CloseGoal(ctx, "team", h.ID, "no longer needed", "dropped")
	require.NoError(t, err)
	_ = g
	list, _ = s.ListProjects(ctx)
	open, seq, err := s.GoalSummary(ctx, "team")
	require.NoError(t, err)
	assert.Equal(t, GoalsBrief{Open: open, Seq: seq}, *list[0].Goals)
	assert.Equal(t, 1, list[0].Goals.Open)
	assert.Positive(t, list[0].Goals.Seq)
	raw, err := json.Marshal(list[0])
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"goals_brief":{"open":1,"seq":`)
	assert.NotContains(t, string(raw), "Ship", "no text")
	assert.NotContains(t, string(raw), "goals_open")
}
