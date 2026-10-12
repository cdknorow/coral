package board

import (
	"context"
	"strings"
)

// Batched read-side hydration for task lists. hydrateWorkflow and
// loadDependencyRule cost one to three queries per task or dependency; on a
// long-lived board ListTasks would run thousands of queries on the board DB's
// single connection. These load the same data with a few IN (...) queries per
// chunk of ids and assemble it with the same helpers, so the output is
// identical.

// idChunkSize bounds the number of ? variables in one IN (...) list, well under
// SQLite's limit.
const idChunkSize = 500

// forEachIDChunk calls fn with "?,?,..." and the matching args for each chunk.
func forEachIDChunk(ids []int64, fn func(placeholders string, args []any) error) error {
	for start := 0; start < len(ids); start += idChunkSize {
		end := min(start+idChunkSize, len(ids))
		chunk := ids[start:end]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		if err := fn(strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ","), args); err != nil {
			return err
		}
	}
	return nil
}

// loadWorkflowData returns task_workflows.data by task id; ids without a row
// are absent.
func (s *Store) loadWorkflowData(ctx context.Context, ids []int64) (map[int64]string, error) {
	data := make(map[int64]string, len(ids))
	err := forEachIDChunk(ids, func(ph string, args []any) error {
		var rows []struct {
			TaskID int64  `db:"task_id"`
			Data   string `db:"data"`
		}
		if err := s.db.SelectContext(ctx, &rows, "SELECT task_id, data FROM task_workflows WHERE task_id IN ("+ph+")", args...); err != nil {
			return err
		}
		for _, r := range rows {
			data[r.TaskID] = r.Data
		}
		return nil
	})
	return data, err
}

// hydrateWorkflows is the batched equivalent of calling hydrateWorkflow on
// every task and ignoring its error: a task whose workflow or amendments cannot
// be loaded keeps a zero Workflow, and a pending task whose inputs cannot be
// resolved keeps a Workflow without Inputs.
func (s *Store) hydrateWorkflows(ctx context.Context, tasks []Task) {
	if len(tasks) == 0 {
		return
	}
	ids := make([]int64, len(tasks))
	var pendingIDs []int64
	for i := range tasks {
		ids[i] = tasks[i].ID
		if tasks[i].Status == "pending" {
			pendingIDs = append(pendingIDs, tasks[i].ID)
		}
	}

	workflows, err := s.loadWorkflowData(ctx, ids)
	if err != nil {
		return
	}
	amendments := make(map[int64][]TaskAmendment)
	err = forEachIDChunk(ids, func(ph string, args []any) error {
		var rows []struct {
			TaskID int64 `db:"task_id"`
			TaskAmendment
		}
		if err := s.db.SelectContext(ctx, &rows, "SELECT task_id,id,revision,actor,created_at,reason,changes_json,previous_snapshot_json,effective_snapshot_json FROM task_amendments WHERE task_id IN ("+ph+") ORDER BY task_id, revision", args...); err != nil {
			return err
		}
		for _, r := range rows {
			amendments[r.TaskID] = append(amendments[r.TaskID], r.TaskAmendment)
		}
		return nil
	})
	if err != nil {
		return
	}

	// Upstream rows for pending tasks, plus the workflows of upstreams that
	// are not already in the list.
	inputRows := make(map[int64][]dependencyInputRow)
	inputsErr := forEachIDChunk(pendingIDs, func(ph string, args []any) error {
		var rows []dependencyInputRow
		if err := s.db.SelectContext(ctx, &rows, `SELECT d.task_id, b.id, b.board_id, b.status,
	 COALESCE(r.condition, 'success') AS condition, COALESCE(r.required_artifacts, '[]') AS required_artifacts
	 FROM task_dependencies d JOIN board_tasks b ON b.id = d.blocked_by_task_id AND b.board_id = d.blocked_by_board_id
	 LEFT JOIN task_dependency_rules r ON r.task_id = d.task_id AND r.upstream_id = b.id
	 WHERE d.task_id IN (`+ph+`) ORDER BY d.task_id, b.id`, args...); err != nil {
			return err
		}
		for _, r := range rows {
			inputRows[r.TaskID] = append(inputRows[r.TaskID], r)
		}
		return nil
	})
	upstreamLoaded := make(map[int64]bool, len(ids))
	for _, id := range ids {
		upstreamLoaded[id] = true
	}
	if inputsErr == nil {
		var missing []int64
		for _, rows := range inputRows {
			for _, r := range rows {
				if !upstreamLoaded[r.ID] {
					upstreamLoaded[r.ID] = true
					missing = append(missing, r.ID)
				}
			}
		}
		extra, err := s.loadWorkflowData(ctx, missing)
		if err != nil {
			inputsErr = err
		}
		for id, data := range extra {
			workflows[id] = data
		}
	}
	upstreamWorkflow := func(id int64) (TaskWorkflow, error) {
		data, found := workflows[id]
		return decodeWorkflow(data, found)
	}

	for i := range tasks {
		t := &tasks[i]
		data, found := workflows[t.ID]
		w, err := decodeWorkflow(data, found)
		if err != nil {
			continue
		}
		w.Amendments = amendments[t.ID]
		if err := decodeAmendments(w.Amendments); err != nil {
			continue
		}
		finishWorkflow(t, w)
		if t.Status != "pending" || inputsErr != nil {
			continue
		}
		inputs, _, err := buildDependencyInputs(inputRows[t.ID], upstreamWorkflow)
		if err != nil {
			continue
		}
		t.Workflow.Inputs = inputs
	}
}

type dependencyRuleKey struct{ taskID, upstreamID int64 }

// loadDependencyRules is the batched form of loadDependencyRule for every
// dependency of the given downstream tasks. A dependency whose upstream task
// does not exist on its board has no entry.
func (s *Store) loadDependencyRules(ctx context.Context, taskIDs []int64) (map[dependencyRuleKey]*dependencyRuleRow, error) {
	rules := make(map[dependencyRuleKey]*dependencyRuleRow)
	err := forEachIDChunk(taskIDs, func(ph string, args []any) error {
		var rows []struct {
			TaskID     int64 `db:"task_id"`
			UpstreamID int64 `db:"upstream_id"`
			dependencyRuleRow
		}
		if err := s.db.SelectContext(ctx, &rows, `SELECT d.task_id, b.id AS upstream_id,
	 COALESCE(r.condition,'success') AS condition, COALESCE(r.required_artifacts,'[]') AS required_artifacts, b.status, COALESCE(w.data,'{}') AS data
	 FROM task_dependencies d JOIN board_tasks b ON b.id = d.blocked_by_task_id AND b.board_id = d.blocked_by_board_id
	 LEFT JOIN task_dependency_rules r ON r.upstream_id = b.id AND r.task_id = d.task_id
	 LEFT JOIN task_workflows w ON w.task_id = b.id
	 WHERE d.task_id IN (`+ph+`)`, args...); err != nil {
			return err
		}
		for i := range rows {
			rules[dependencyRuleKey{rows[i].TaskID, rows[i].UpstreamID}] = &rows[i].dependencyRuleRow
		}
		return nil
	})
	return rules, err
}
