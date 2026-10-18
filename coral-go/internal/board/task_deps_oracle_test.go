package board

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AddTaskDependencies, split into its checks and its transaction (#4669 part 2, #4599), does what
// addTaskDependenciesBefore4599 (the function at v1.4.91, below) does: the same error, the same defaults filled into the
// caller's deps, and the same tables after. Two stores start from one seeded file and take the same random calls in step,
// old on A, new on B, their tables compared after each. Each outcome (an error kind, or the task left blocked, pending or
// as it was) has a floor, so none goes unexercised.
//
// The re-checks inside the transaction (a claim, or a new cycle, racing the checks before it) are the same calls as the
// checks before it, so a sequential driver never reaches them.

var depOracleOutcomes = []string{"no rows", "can only change before", "duplicate dependency", "condition must be",
	"artifact names must be", "not found on board", "cannot require artifact", "circular dependency", "cannot depend on itself",
	"exceed maximum depth"}

// seedDepsOracle writes a board of tasks on two boards (every status, workflows with and without an output contract and
// with outcomes and artifacts) and an acyclic graph of dependencies with a long chain, and returns the task count.
func seedDepsOracle(t *testing.T, s *Store, rng *rand.Rand) int {
	t.Helper()
	ctx := context.Background()
	n := 30 + rng.Intn(10)
	for i := 1; i <= n; i++ {
		board := "team"
		if rng.Intn(5) == 0 {
			board = "other"
		}
		_, err := s.db.ExecContext(ctx, `INSERT INTO board_tasks (id, board_id, title, status, priority, created_by, created_at) VALUES (?, ?, ?, ?, 'medium', 'lead', '2026-10-01T09:00:00Z')`,
			i, board, fmt.Sprintf("task %d", i), taskStatuses[rng.Intn(len(taskStatuses))])
		require.NoError(t, err)
		if rng.Intn(3) > 0 {
			var w TaskWorkflow
			if rng.Intn(2) == 0 {
				w.RequiredOutputs = []string{"report", "patch"}[:1+rng.Intn(2)]
			}
			w.Outcome = []string{"", "success", "failed", "cancelled"}[rng.Intn(4)]
			if rng.Intn(2) == 0 {
				w.Artifacts = []TaskArtifact{{Name: "report", Content: "x"}}
			}
			require.NoError(t, saveWorkflow(ctx, s.db, int64(i), w))
		}
	}
	edge := func(task, up int) {
		var board string
		require.NoError(t, s.db.GetContext(ctx, &board, `SELECT board_id FROM board_tasks WHERE id = ?`, up))
		d := TaskDep{TaskID: int64(up), BoardID: board, Condition: []string{"success", "failure", "termination"}[rng.Intn(3)]}
		_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO task_dependencies (task_id, blocked_by_task_id, blocked_by_board_id) VALUES (?, ?, ?)`, task, up, board)
		require.NoError(t, err)
		_, _ = s.db.ExecContext(ctx, `DELETE FROM task_dependency_rules WHERE task_id = ? AND upstream_id = ?`, task, up)
		require.NoError(t, saveDependencyRule(ctx, s.db, int64(task), d))
	}
	for i := 2; i <= 8; i++ { // a chain 8 -> 7 -> ... -> 1, for the depth limit
		edge(i, i-1)
	}
	for i := 9; i <= n; i++ {
		for k := rng.Intn(3); k > 0; k-- {
			edge(i, 1+rng.Intn(i-1))
		}
	}
	return n
}

// randomDeps: dependents are the tasks that depend on taskID directly (one of them as a blocker is a cycle).
func randomDeps(rng *rand.Rand, n int, taskID int64, dependents []int64) []TaskDep {
	var deps []TaskDep
	for k := rng.Intn(4); k > 0; k-- {
		d := TaskDep{TaskID: int64(1 + rng.Intn(n+2))}
		switch rng.Intn(10) {
		case 0:
			d.TaskID = taskID
		case 1:
			if len(deps) > 0 {
				d.TaskID = deps[0].TaskID
			}
		case 2, 3:
			if len(dependents) > 0 {
				d.TaskID = dependents[rng.Intn(len(dependents))]
			}
		case 4: // the top of the long chain: over a small depth limit
			d.TaskID = int64(6 + rng.Intn(3))
		}
		d.Condition = []string{"", "", "", "success", "success", "failure", "failure", "termination", "termination", "", "", "later"}[rng.Intn(12)]
		if rng.Intn(25) == 0 {
			d.Condition = "' OR 1=1 --"
		}
		d.BoardID = []string{"", "", "", "team", "other", "nope"}[rng.Intn(6)]
		switch rng.Intn(20) {
		case 0, 1:
			d.RequiredArtifacts = []string{"report"}
		case 2, 3:
			d.RequiredArtifacts = []string{"undeclared"}
		case 4:
			d.RequiredArtifacts = []string{"report", "report"}
		case 5:
			d.RequiredArtifacts = []string{" "}
		case 6:
			d.RequiredArtifacts = []string{strings.Repeat("x", 129)}
		}
		deps = append(deps, d)
	}
	return deps
}

func copyDeps(deps []TaskDep) []TaskDep {
	if deps == nil {
		return nil
	}
	out := make([]TaskDep, len(deps))
	for i, d := range deps {
		out[i] = d
		if d.RequiredArtifacts != nil {
			out[i].RequiredArtifacts = append([]string(nil), d.RequiredArtifacts...)
		}
	}
	return out
}

// dumpDepsTables is every row of the tables AddTaskDependencies reads or writes.
func dumpDepsTables(t *testing.T, s *Store) string {
	t.Helper()
	var b strings.Builder
	for _, q := range []string{`SELECT id, board_id, status FROM board_tasks ORDER BY id`,
		`SELECT task_id, blocked_by_task_id, blocked_by_board_id FROM task_dependencies ORDER BY task_id, blocked_by_task_id`,
		`SELECT task_id, upstream_id, condition, required_artifacts FROM task_dependency_rules ORDER BY task_id, upstream_id, condition, required_artifacts`,
		`SELECT task_id, data FROM task_workflows ORDER BY task_id`} {
		rows, err := s.db.Queryx(q)
		require.NoError(t, err)
		for rows.Next() {
			vals, err := rows.SliceScan()
			require.NoError(t, err)
			fmt.Fprintln(&b, vals...)
		}
		require.NoError(t, rows.Err())
		rows.Close()
		b.WriteString("--\n")
	}
	return b.String()
}

func depsOutcome(t *testing.T, s *Store, err error, taskID int64, project, before string) string {
	if err != nil {
		for _, k := range depOracleOutcomes {
			if strings.Contains(err.Error(), k) {
				return k
			}
		}
		return "other error: " + err.Error()
	}
	var after string
	require.NoError(t, s.db.Get(&after, `SELECT status FROM board_tasks WHERE id = ? AND board_id = ?`, taskID, project))
	if after == before {
		return "ok, left " + after
	}
	return "ok, now " + after
}

func TestAddTaskDependenciesAgreesWithBefore4599(t *testing.T) {
	ctx := context.Background()
	seeds := 80
	if testing.Short() {
		seeds = 16
	}
	outcomes := map[string]int{}
	for seed := int64(1); seed <= int64(seeds); seed++ {
		rng := rand.New(rand.NewSource(seed))
		dir := t.TempDir()
		pathA, pathB := filepath.Join(dir, "a.db"), filepath.Join(dir, "b.db")
		seedStore, err := NewStoreWithKey(pathA, "")
		require.NoError(t, err)
		n := seedDepsOracle(t, seedStore, rng)
		require.NoError(t, seedStore.Close()) // checkpoints the WAL into the main file
		data, err := os.ReadFile(pathA)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(pathB, data, 0o600))
		a, err := NewStoreWithKey(pathA, "")
		require.NoError(t, err)
		b, err := NewStoreWithKey(pathB, "")
		require.NoError(t, err)
		require.Equal(t, dumpDepsTables(t, a), dumpDepsTables(t, b))
		for step := 0; step < 120; step++ {
			taskID := int64(1 + rng.Intn(n+2))
			if rng.Intn(4) > 0 { // mostly a task that hasn't started, so the checks after the first are reached
				var open []int64
				require.NoError(t, a.db.Select(&open, `SELECT id FROM board_tasks WHERE status IN ('pending', 'blocked', 'draft') ORDER BY id`))
				if len(open) > 0 {
					taskID = open[rng.Intn(len(open))]
				}
			}
			project := "team"
			if rng.Intn(10) > 0 {
				_ = a.db.Get(&project, `SELECT board_id FROM board_tasks WHERE id = ?`, taskID)
			} else if rng.Intn(2) == 0 {
				project = "other"
			}
			var dependents []int64
			require.NoError(t, a.db.Select(&dependents, `SELECT task_id FROM task_dependencies WHERE blocked_by_task_id = ? ORDER BY task_id`, taskID))
			deps := randomDeps(rng, n, taskID, dependents)
			maxDepth := []int{0, 3, 5, 10}[rng.Intn(4)]
			var before string
			_ = a.db.Get(&before, `SELECT status FROM board_tasks WHERE id = ? AND board_id = ?`, taskID, project)
			oldDeps, newDeps := copyDeps(deps), copyDeps(deps)
			oldErr := addTaskDependenciesBefore4599(a, ctx, project, taskID, oldDeps, maxDepth)
			newErr := b.AddTaskDependencies(ctx, project, taskID, newDeps, maxDepth)
			where := fmt.Sprintf("seed %d step %d: task %d on %s, deps %+v, depth %d", seed, step, taskID, project, deps, maxDepth)
			require.Equal(t, fmt.Sprint(oldErr), fmt.Sprint(newErr), where)
			require.True(t, reflect.DeepEqual(oldDeps, newDeps), "%s: deps filled in as %+v, before %+v", where, newDeps, oldDeps)
			require.Equal(t, dumpDepsTables(t, a), dumpDepsTables(t, b), where)
			outcomes[depsOutcome(t, b, newErr, taskID, project, before)]++
		}
		require.NoError(t, a.Close())
		require.NoError(t, b.Close())
	}
	t.Logf("calls compared, per outcome: %v", outcomes)
	for _, k := range append(depOracleOutcomes, "ok, now blocked", "ok, now pending", "ok, left blocked", "ok, left pending", "ok, left draft") {
		assert.GreaterOrEqual(t, outcomes[k], 30, "%q went (almost) unexercised", k)
	}
	for k := range outcomes {
		assert.False(t, strings.HasPrefix(k, "other error"), "an error no outcome names: %s", k)
	}
}

// addTaskDependenciesBefore4599 is AddTaskDependencies before #4599 split it (store.go at v1.4.91), kept to compare with.
func addTaskDependenciesBefore4599(s *Store, ctx context.Context, project string, taskID int64, deps []TaskDep, maxDepth int) error {
	// Fill in default board_id
	var taskStatus string
	if err := s.db.GetContext(ctx, &taskStatus, "SELECT status FROM board_tasks WHERE id = ? AND board_id = ?", taskID, project); err != nil {
		return err
	}
	if taskStatus == "review_pending" || taskStatus == "in_progress" || taskStatus == "completed" || taskStatus == "skipped" {
		return fmt.Errorf("dependencies can only change before a task starts")
	}
	seen := map[int64]bool{}
	for i := range deps {
		if seen[deps[i].TaskID] {
			return fmt.Errorf("duplicate dependency #%d", deps[i].TaskID)
		}
		seen[deps[i].TaskID] = true
		if deps[i].Condition == "" {
			deps[i].Condition = "success"
		}
		switch deps[i].Condition {
		case "success", "failure", "termination":
		default:
			return fmt.Errorf("dependency condition must be success, failure, or termination")
		}
		if err := validNames(deps[i].RequiredArtifacts); err != nil {
			return err
		}
		if deps[i].BoardID == "" {
			deps[i].BoardID = project
		}
	}

	// Validate all blocked_by tasks exist
	for _, d := range deps {
		var exists int
		err := s.db.GetContext(ctx, &exists,
			"SELECT COUNT(*) FROM board_tasks WHERE id = ? AND board_id = ?", d.TaskID, d.BoardID)
		if err != nil || exists == 0 {
			return fmt.Errorf("blocker task #%d not found on board %s", d.TaskID, d.BoardID)
		}
		// When the producer has declared an output contract, reject a consumer
		// dependency that asks for a name the producer never promised to publish.
		// Older tasks with no declared outputs remain compatible and are checked
		// against their immutable completion artifacts at readiness time.
		if len(d.RequiredArtifacts) > 0 {
			upstream, err := loadWorkflow(ctx, s.db, d.TaskID)
			if err != nil {
				return err
			}
			if len(upstream.RequiredOutputs) > 0 {
				declared := make(map[string]bool, len(upstream.RequiredOutputs))
				for _, name := range upstream.RequiredOutputs {
					declared[name] = true
				}
				for _, name := range d.RequiredArtifacts {
					if !declared[name] {
						return fmt.Errorf("dependency #%d cannot require artifact %q: upstream declares outputs [%s]", d.TaskID, name, strings.Join(upstream.RequiredOutputs, ", "))
					}
				}
			}
		}
	}

	// Validate no circular dependencies
	if err := s.validateNoCycles(ctx, taskID, deps); err != nil {
		return err
	}

	// Validate depth limit
	if err := s.ValidateDependencyDepth(ctx, taskID, deps, maxDepth); err != nil {
		return err
	}

	// Replace dependencies and their conditions atomically.
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A claim may have raced the validation before this transaction.
	if err := tx.GetContext(ctx, &taskStatus, "SELECT status FROM board_tasks WHERE id = ? AND board_id = ?", taskID, project); err != nil {
		return err
	}
	if taskStatus == "review_pending" || taskStatus == "in_progress" || taskStatus == "completed" || taskStatus == "skipped" {
		return fmt.Errorf("dependencies can only change before a task starts")
	}
	if err := validateTaskCycles(ctx, tx, taskID, deps); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM task_dependency_rules WHERE task_id = ?", taskID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM task_dependencies WHERE task_id = ?", taskID); err != nil {
		return err
	}
	for _, d := range deps {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO task_dependencies (task_id, blocked_by_task_id, blocked_by_board_id) VALUES (?, ?, ?)",
			taskID, d.TaskID, d.BoardID); err != nil {
			return err
		}
		if err := saveDependencyRule(ctx, tx, taskID, d); err != nil {
			return err
		}
	}

	_, ready, err := dependencyInputs(ctx, tx, taskID)
	if err != nil {
		return err
	}
	status := "blocked"
	if ready {
		status = "pending"
	}
	if _, err := tx.ExecContext(ctx, "UPDATE board_tasks SET status = ? WHERE id = ? AND board_id = ? AND status IN ('pending', 'blocked')", status, taskID, project); err != nil {
		return err
	}
	return tx.Commit()
}
