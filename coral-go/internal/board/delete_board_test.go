package board

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Everything a board has, with the table and key column it is in: DeleteProject must leave none of it for the deleted board and all of it for another.
var boardOwnedRows = []struct{ table, col string }{
	{"board_subscribers", "project"}, {"board_messages", "project"}, {"board_reminders", "project"}, {"board_groups", "project"},
	{"board_tasks", "board_id"}, {"board_settings", "project"}, {"board_waits", "project"}, {"board_working_modes", "board_id"},
	{"board_workflow_presets", "board_id"}, {"health_counters", "project"}, {"health_items", "project"}, {"boards", "id"},
}

// Audit trails: what the Hub's operators did and what the health rules did are a record of what happened, not part of the board, so they stay.
var auditRows = []struct{ table, col string }{{"hub_operator_actions", "project"}, {"health_actions", "project"}}

// Rows that hang off a board's tasks, found through the task ids.
var taskOwnedRows = []struct{ table, sel string }{
	{"task_amendments", "task_id IN (SELECT id FROM board_tasks WHERE board_id = ?)"},
	{"task_dependencies", "task_id IN (SELECT id FROM board_tasks WHERE board_id = ?)"},
	{"task_workflows", "task_id IN (SELECT id FROM board_tasks WHERE board_id = ?)"},
	{"task_dependency_rules", "task_id IN (SELECT id FROM board_tasks WHERE board_id = ?)"},
	{"task_ready_notifications", "task_id IN (SELECT id FROM board_tasks WHERE board_id = ?)"},
}

// fillBoard gives a board one of everything a board has.
func fillBoard(t *testing.T, s *Store, project string) (taskIDs []int64) {
	t.Helper()
	ctx := context.Background()
	_, err := s.Subscribe(ctx, project, "Boss", "Orchestrator", "claude-"+project+"-boss", nil, "all")
	require.NoError(t, err)
	_, err = s.Subscribe(ctx, project, "Dev", "Developer", "claude-"+project+"-dev", nil, "all")
	require.NoError(t, err)
	_, err = s.PostMessage(ctx, project, "Boss", "hello", nil)
	require.NoError(t, err)
	first, err := s.CreateTask(ctx, project, "first", "body", "high", "Boss", "Dev")
	require.NoError(t, err)
	second, err := s.CreateTaskWithOpts(ctx, project, "second", "body", "high", "Boss", &CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: first.ID, BoardID: project}}}, "Dev")
	require.NoError(t, err)
	_, err = s.ClaimTask(ctx, project, "Dev", first.ID)
	require.NoError(t, err)
	_, err = s.AmendTask(ctx, project, first.ID, "Boss", first.Revision, "wording", map[string]interface{}{"body": "body, corrected"})
	require.NoError(t, err)
	require.NoError(t, s.SetBoardSetting(ctx, project, "health", `{"on":true}`))
	_, err = s.RegisterWait(ctx, project, "Dev", "claude-"+project+"-dev", "task", "1", "waiting", time.Hour)
	require.NoError(t, err)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO board_reminders (project, subscriber_id, message, interval_seconds) VALUES (?, 'Dev', 'remind', 60)`, []any{project}},
		{`INSERT INTO board_groups (project, group_id, session_id) VALUES (?, 'g1', 'claude-x')`, []any{project}},
		{`INSERT INTO health_actions (project, rule, target, at) VALUES (?, 'claim', 'Dev', '2026-10-19T00:00:00Z')`, []any{project}},
		{`INSERT INTO health_counters (project, rule, target, subject, count, last_at) VALUES (?, 'claim', 'Dev', '1', 1, '2026-10-19T00:00:00Z')`, []any{project}},
		{`INSERT INTO health_items (project, item, state, shown_at) VALUES (?, 'stuck:Dev', 'open', '2026-10-19T00:00:00Z')`, []any{project}},
		{`INSERT INTO hub_operator_actions (at, hub_user, method, path, project, status) VALUES ('2026-10-19T00:00:00Z', 'u', 'PUT', '/x', ?, 200)`, []any{project}},
		{`INSERT OR IGNORE INTO boards (id, label, created_at) VALUES (?, ?, '2026-10-19T00:00:00Z')`, []any{project, project}},
	} {
		_, err := s.db.ExecContext(ctx, q.sql, q.args...)
		require.NoError(t, err, q.sql)
	}
	for _, id := range []int64{first.ID, second.ID} {
		_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO task_workflows (task_id, data) VALUES (?, '{}')`, id)
		require.NoError(t, err)
		_, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO task_ready_notifications (task_id) VALUES (?)`, id)
		require.NoError(t, err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO task_dependency_rules (task_id, upstream_id, condition, required_artifacts) VALUES (?, ?, 'completed', '[]')`, second.ID, first.ID)
	require.NoError(t, err)
	_, err = s.SaveWorkflowPreset(ctx, project, "p1", "Preset", "do it", true)
	require.NoError(t, err)
	_, err = s.SetWorkingMode(ctx, project, WorkingMode{Mode: "none"})
	require.NoError(t, err)
	return []int64{first.ID, second.ID}
}

func countRows(t *testing.T, s *Store, table, where string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, s.db.GetContext(context.Background(), &n, "SELECT COUNT(*) FROM "+table+" WHERE "+where, args...), table)
	return n
}

// #4966: deleting a board takes everything: its tasks, their amendments, workflow data and dependency links, its settings, health records and its
// row in the boards table, in one go, and leaves every other board as it was.
func TestDeleteProjectTakesTheWholeBoard(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	fillBoard(t, s, "gone")
	fillBoard(t, s, "keep")

	for _, r := range boardOwnedRows {
		require.Greater(t, countRows(t, s, r.table, r.col+" = ?", "gone"), 0, "the fixture put a row in %s", r.table)
	}
	for _, r := range taskOwnedRows {
		require.Greater(t, countRows(t, s, r.table, r.sel, "gone"), 0, "the fixture put a row in %s", r.table)
	}
	keepBefore := map[string]int{}
	for _, r := range boardOwnedRows {
		keepBefore[r.table] = countRows(t, s, r.table, r.col+" = ?", "keep")
	}
	for _, r := range taskOwnedRows {
		keepBefore[r.table] = countRows(t, s, r.table, r.sel, "keep")
	}

	auditBefore := map[string]int{}
	for _, r := range auditRows {
		auditBefore[r.table] = countRows(t, s, r.table, r.col+" = ?", "gone")
		require.Greater(t, auditBefore[r.table], 0, "the fixture put a row in %s", r.table)
	}

	require.NoError(t, s.DeleteProject(ctx, "gone"))

	for _, r := range auditRows {
		assert.Equal(t, auditBefore[r.table], countRows(t, s, r.table, r.col+" = ?", "gone"), "%s: the audit trail of the deleted board stays", r.table)
	}

	for _, r := range boardOwnedRows {
		assert.Equal(t, 0, countRows(t, s, r.table, r.col+" = ?", "gone"), "%s: nothing of the deleted board is left", r.table)
		assert.Equal(t, keepBefore[r.table], countRows(t, s, r.table, r.col+" = ?", "keep"), "%s: the other board is untouched", r.table)
	}
	for _, r := range taskOwnedRows {
		assert.Equal(t, 0, countRows(t, s, r.table, "task_id NOT IN (SELECT id FROM board_tasks)"), "%s: no row is left for a task that is gone", r.table)
		assert.Equal(t, keepBefore[r.table], countRows(t, s, r.table, r.sel, "keep"), "%s: the other board's tasks keep theirs", r.table)
	}
	// A board made again under the name starts empty.
	_, err := s.Subscribe(ctx, "gone", "New", "Dev", "claude-new", nil, "all")
	require.NoError(t, err)
	assert.Equal(t, 0, countRows(t, s, "board_tasks", "board_id = ?", "gone"), "and has no tasks")
	assert.Equal(t, 0, countRows(t, s, "board_settings", "project = ?", "gone"), "and no settings")
}

// A task on another board that was blocked by a task of the deleted board is not left pointing at a task that no longer exists.
func TestDeleteProjectUnblocksTasksOnOtherBoards(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, err := s.Subscribe(ctx, "a", "Boss", "Orchestrator", "claude-a", nil, "all")
	require.NoError(t, err)
	_, err = s.Subscribe(ctx, "b", "Boss", "Orchestrator", "claude-b", nil, "all")
	require.NoError(t, err)
	upstream, err := s.CreateTask(ctx, "a", "upstream", "", "high", "Boss")
	require.NoError(t, err)
	downstream, err := s.CreateTaskWithOpts(ctx, "b", "downstream", "", "high", "Boss", &CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: upstream.ID, BoardID: "a"}}})
	require.NoError(t, err)
	require.Equal(t, 1, countRows(t, s, "task_dependencies", "task_id = ?", downstream.ID))

	require.NoError(t, s.DeleteProject(ctx, "a"))

	assert.Equal(t, 0, countRows(t, s, "task_dependencies", "task_id = ? OR blocked_by_task_id = ?", downstream.ID, upstream.ID), "no dependency row points at a deleted task")
	still, err := s.GetTask(ctx, "b", downstream.ID)
	require.NoError(t, err)
	require.NotNil(t, still, "the other board's task is still there")
}

// #5762: a team launch that started nobody takes back the board it made, but only while nothing else has used it: a member, a message, a task, a group,
// a reminder or a wait is someone else's, and the board stays with it. A board with only what the launch writes (settings, a working mode, presets) goes.
func TestDeleteProjectIfUnusedKeepsABoardSomethingElseUsed(t *testing.T) {
	ctx := context.Background()
	for _, use := range []struct {
		name string
		do   func(t *testing.T, s *Store)
	}{
		{"a member joined", func(t *testing.T, s *Store) {
			_, err := s.Subscribe(ctx, "made", "Dev", "Developer", "claude-made-dev", nil, "all")
			require.NoError(t, err)
		}},
		{"a message was posted", func(t *testing.T, s *Store) {
			_, err := s.Subscribe(ctx, "made", "Dev", "Developer", "claude-made-dev", nil, "all")
			require.NoError(t, err)
			_, err = s.Unsubscribe(ctx, "made", "Dev") // the member left again: the message stays
			require.NoError(t, err)
			_, err = s.PostMessage(ctx, "made", "Dev", "hello", nil)
			require.NoError(t, err)
		}},
		{"a task was made", func(t *testing.T, s *Store) {
			_, err := s.CreateTask(ctx, "made", "a task", "", "medium", "Boss")
			require.NoError(t, err)
		}},
		{"a group", func(t *testing.T, s *Store) {
			_, err := s.db.ExecContext(ctx, `INSERT INTO board_groups (project, group_id, session_id) VALUES ('made', 'g1', 'claude-x')`)
			require.NoError(t, err)
		}},
		{"a reminder", func(t *testing.T, s *Store) {
			_, err := s.db.ExecContext(ctx, `INSERT INTO board_reminders (project, subscriber_id, message, interval_seconds) VALUES ('made', 'Dev', 'remind', 60)`)
			require.NoError(t, err)
		}},
		{"a wait", func(t *testing.T, s *Store) {
			_, err := s.RegisterWait(ctx, "made", "Dev", "claude-made-dev", "task", "1", "waiting", time.Hour)
			require.NoError(t, err)
		}},
	} {
		t.Run(use.name, func(t *testing.T) {
			s := testStore(t)
			require.NoError(t, s.SetBoardSetting(ctx, "made", "health", `{"on":true}`)) // what the launch wrote
			_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO boards (id, label, created_at) VALUES ('made', 'made', '2026-10-19T00:00:00Z')`)
			require.NoError(t, err)
			use.do(t, s)
			deleted, err := s.DeleteProjectIfUnused(ctx, "made")
			require.NoError(t, err)
			assert.False(t, deleted, "the board is kept")
			assert.Equal(t, 1, countRows(t, s, "boards", "id = ?", "made"))
			assert.Equal(t, 1, countRows(t, s, "board_settings", "project = ?", "made"), "with everything on it")
		})
	}
}

func TestDeleteProjectIfUnusedTakesABoardNobodyUsed(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	require.NoError(t, s.SetBoardSetting(ctx, "made", "health", `{"on":true}`))
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO boards (id, label, created_at) VALUES ('made', 'made', '2026-10-19T00:00:00Z')`)
	require.NoError(t, err)
	fillBoard(t, s, "other")
	deleted, err := s.DeleteProjectIfUnused(ctx, "made")
	require.NoError(t, err)
	assert.True(t, deleted)
	assert.Equal(t, 0, countRows(t, s, "boards", "id = ?", "made"))
	assert.Equal(t, 0, countRows(t, s, "board_settings", "project = ?", "made"))
	assert.Greater(t, countRows(t, s, "board_tasks", "board_id = ?", "other"), 0, "another board is untouched")
}
