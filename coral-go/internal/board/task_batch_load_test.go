package board

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"
	sqlite "modernc.org/sqlite"
)

// countingDriver wraps the SQLite driver and counts statements executed.
type countingDriver struct{ n *atomic.Int64 }

type countingConn struct {
	driver.Conn
	n *atomic.Int64
}

func (d countingDriver) Open(name string) (driver.Conn, error) {
	c, err := (&sqlite.Driver{}).Open(name)
	if err != nil {
		return nil, err
	}
	return countingConn{c, d.n}, nil
}

func (c countingConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.n.Add(1)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}

func (c countingConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.n.Add(1)
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
}

var (
	countingOnce    sync.Once
	countingQueries atomic.Int64
)

// countQueries points s at a counting connection to the same database file.
func countQueries(t *testing.T, s *Store, path string) *atomic.Int64 {
	t.Helper()
	countingOnce.Do(func() { sql.Register("sqlite-counting", countingDriver{&countingQueries}) })
	db, err := sql.Open("sqlite-counting", path)
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	// The store's own writer is replaced; it is closed too, or Windows can't remove the
	// test's folder with board.db still open (#3920 T2).
	orig := s.db
	t.Cleanup(func() { orig.Close() })
	s.db = sqlx.NewDb(db, "sqlite")
	return &countingQueries
}

// listTasksUnbatched is ListTasks as it was before batching: hydrateWorkflow
// per task and loadDependencyRule per dependency. It is the reference the
// batched path must match exactly.
func listTasksUnbatched(t *testing.T, s *Store, project string) []Task {
	t.Helper()
	ctx := context.Background()
	tasks, err := s.ListTasks(ctx, project)
	require.NoError(t, err)
	for i := range tasks {
		tasks[i].Workflow = TaskWorkflow{}
		tasks[i].BlockedBy = nil
		s.hydrateWorkflow(ctx, &tasks[i])
		var rows []struct {
			BlockedByTaskID  int64   `db:"blocked_by_task_id"`
			BlockedByBoardID string  `db:"blocked_by_board_id"`
			Title            *string `db:"title"`
			Status           *string `db:"status"`
		}
		require.NoError(t, s.db.SelectContext(ctx, &rows, `SELECT td.blocked_by_task_id, td.blocked_by_board_id, bt.title, bt.status
		 FROM task_dependencies td LEFT JOIN board_tasks bt ON bt.id = td.blocked_by_task_id WHERE td.task_id = ?`, tasks[i].ID))
		for _, r := range rows {
			dep := TaskDep{TaskID: r.BlockedByTaskID, BoardID: r.BlockedByBoardID}
			s.loadDependencyRule(ctx, tasks[i].ID, &dep)
			if r.Title != nil {
				dep.Title = *r.Title
			}
			if r.Status != nil {
				dep.Status = *r.Status
			}
			tasks[i].BlockedBy = append(tasks[i].BlockedBy, dep)
		}
	}
	return tasks
}

func seedBatchBoard(t *testing.T, s *Store, n int) {
	t.Helper()
	ctx := context.Background()
	var upstream []int64
	for i := 0; i < n; i++ {
		opts := &CreateTaskOpts{}
		if i%3 == 0 {
			opts.Workflow = TaskWorkflow{Instructions: fmt.Sprintf("step %d", i), RequiredOutputs: []string{"out"}}
		}
		if i%5 == 4 && len(upstream) > 1 {
			opts.BlockedBy = []TaskDep{
				{TaskID: upstream[len(upstream)-1], BoardID: "team"},
				{TaskID: upstream[len(upstream)-2], BoardID: "team", Condition: "termination"},
			}
		}
		task, err := s.CreateTaskWithOpts(ctx, "team", fmt.Sprintf("task %d", i), "", "medium", "lead", opts, "worker")
		require.NoError(t, err)
		if i%5 != 4 {
			upstream = append(upstream, task.ID)
		}
		switch {
		case i%5 == 4: // blocked: cannot be claimed; stays pending with inputs
		case i%7 == 0 || i%7 == 1: // finished with an output artifact, so downstream inputs resolve
			_, err = s.ClaimTask(ctx, "team", "worker", task.ID)
			require.NoError(t, err)
			msg := "done"
			_, err = s.CompleteTaskWithArtifacts(ctx, "team", task.ID, "worker", &msg, "success", []TaskArtifact{{Name: "out", Content: "x"}})
			require.NoError(t, err)
		case i%7 == 2:
			_, err = s.AmendTask(ctx, "team", task.ID, "lead", 1, "tweak", map[string]interface{}{"workflow_instructions": "extra"})
			require.NoError(t, err)
		}
	}
	// A dependency on another board's task and on a task that does not exist.
	other, err := s.CreateTask(ctx, "other", "elsewhere", "", "medium", "lead")
	require.NoError(t, err)
	_, err = s.CreateTaskWithOpts(ctx, "team", "cross-board", "", "low", "lead", &CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: other.ID, BoardID: "other"}}})
	require.NoError(t, err)
	orphan, err := s.CreateTaskWithOpts(ctx, "team", "orphan dep", "", "low", "lead", &CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: upstream[0], BoardID: "team"}}})
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, "UPDATE task_dependencies SET blocked_by_task_id = 999999 WHERE task_id = ?", orphan.ID)
	require.NoError(t, err)
	// A dependency requiring an artifact the finished upstream never produced.
	needs, err := s.CreateTaskWithOpts(ctx, "team", "needs missing", "", "low", "lead", &CreateTaskOpts{BlockedBy: []TaskDep{{TaskID: upstream[0], BoardID: "team", RequiredArtifacts: []string{"out"}}}})
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `UPDATE task_dependency_rules SET required_artifacts = '["never"]' WHERE task_id = ?`, needs.ID)
	require.NoError(t, err)
	// A corrupt workflow row: both paths leave that task's workflow zero.
	_, err = s.db.ExecContext(ctx, "UPDATE task_workflows SET data = '{not json' WHERE task_id = ?", upstream[1])
	require.NoError(t, err)
}

func TestListTasks_BatchedMatchesUnbatched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.db")
	s, err := NewStoreWithKey(path, "")
	require.NoError(t, err)
	defer s.Close()
	seedBatchBoard(t, s, 120)

	got, err := s.ListTasks(context.Background(), "team")
	require.NoError(t, err)
	want := listTasksUnbatched(t, s, "team")

	gotJSON, err := json.MarshalIndent(got, "", " ")
	require.NoError(t, err)
	wantJSON, err := json.MarshalIndent(want, "", " ")
	require.NoError(t, err)
	require.Equal(t, string(wantJSON), string(gotJSON))

	// The seed must actually exercise every branch being compared.
	out := string(gotJSON)
	for _, needle := range []string{`"amendments": [`, `"inputs": [`, `"upstream task not found"`, `"board_id": "other"`, `"missing_artifacts"`} {
		require.True(t, strings.Contains(out, needle), "seed does not cover %s", needle)
	}
}

func TestListTasks_QueryCountIndependentOfTaskCount(t *testing.T) {
	queriesFor := func(n int) int64 {
		path := filepath.Join(t.TempDir(), "board.db")
		s, err := NewStoreWithKey(path, "")
		require.NoError(t, err)
		defer s.Close()
		seedBatchBoard(t, s, n)
		count := countQueries(t, s, path)
		before := count.Load()
		tasks, err := s.ListTasks(context.Background(), "team")
		require.NoError(t, err)
		require.Greater(t, len(tasks), n)
		return count.Load() - before
	}
	small, large := queriesFor(40), queriesFor(400)
	t.Logf("ListTasks queries: %d for ~40 tasks, %d for ~400 tasks", small, large)
	require.Equal(t, small, large)
	require.LessOrEqual(t, large, int64(10))
}
