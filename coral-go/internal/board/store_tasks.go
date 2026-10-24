// Creating, listing, claiming, completing and updating tasks (moved out of store.go, #4998; no behaviour change).

package board

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/naming"
)

// GetTask returns one task on a board.
func (s *Store) GetTask(ctx context.Context, project string, taskID int64) (*Task, error) {
	return s.getTaskByID(ctx, project, taskID)
}

// getTaskByID fetches a task by ID and board_id.
func (s *Store) getTaskByID(ctx context.Context, project string, taskID int64) (*Task, error) {
	var t Task
	err := s.reader().GetContext(ctx, &t,
		"SELECT id, revision, board_id, title, body, status, priority, created_by, assigned_to, completed_by, completion_message, created_at, claimed_at, completed_at, session_id, cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, parked_reason, parked_until, remind_at, goal_id FROM board_tasks WHERE id = ? AND board_id = ?", taskID, project)
	if err != nil {
		return nil, err
	}
	if err := s.hydrateWorkflow(ctx, &t); err != nil {
		return nil, err
	}
	t.BlockedBy, err = s.GetTaskDependencies(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// CreateTaskOpts holds optional parameters for CreateTask.
type CreateTaskOpts struct {
	BlockedBy []TaskDep
	MaxDepth  int
	Draft     bool
	Workflow  TaskWorkflow
	// GoalID links the new task to a goal of its board (#7087); a goal of another board or a closed one refuses the task. A retry of a task that has a goal, with none given, continues under it.
	GoalID *int64
}

// CreateTask inserts a task and returns it.
func (s *Store) CreateTask(ctx context.Context, project, title, body, priority, createdBy string, assignedTo ...string) (*Task, error) {
	return s.CreateTaskWithOpts(ctx, project, title, body, priority, createdBy, nil, assignedTo...)
}

// CreateTaskWithOpts inserts a task with optional dependency configuration.
func (s *Store) CreateTaskWithOpts(ctx context.Context, project, title, body, priority, createdBy string, opts *CreateTaskOpts, assignedTo ...string) (*Task, error) {
	w := TaskWorkflow{}
	if opts != nil {
		w = opts.Workflow
	}
	w, err := s.prepareWorkflow(ctx, project, w)
	if err != nil {
		return nil, err
	}
	if priority == "" {
		priority = "medium"
	}
	now := nowUTC()

	var bodyPtr *string
	if body != "" {
		bodyPtr = &body
	}
	var assignPtr *string
	if len(assignedTo) > 0 && assignedTo[0] != "" {
		assignPtr = &assignedTo[0]
	}

	initialStatus := "draft"

	// The goal is checked before anything is stored: a refusal leaves no task behind.
	var goalID *int64
	if opts != nil && opts.GoalID != nil {
		if err := s.checkGoalForTask(ctx, s.db, project, *opts.GoalID); err != nil {
			return nil, err
		}
		goalID = opts.GoalID
	} else if w.RetryOf != 0 {
		goalID = s.inheritedGoal(ctx, project, w.RetryOf)
	}

	result, err := s.db.ExecContext(ctx,
		`INSERT INTO board_tasks (board_id, title, body, status, priority, created_by, assigned_to, created_at, goal_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		project, title, bodyPtr, initialStatus, priority, createdBy, assignPtr, now, goalID)
	if err != nil {
		return nil, err
	}
	taskID, _ := result.LastInsertId()
	s.ensureBoardRow(ctx, project)
	// Keep new tasks unclaimable until configuration and dependencies are stored.
	if err := saveWorkflow(ctx, s.db, taskID, w); err != nil {
		return nil, err
	}

	if opts != nil && len(opts.BlockedBy) > 0 {
		maxDepth := opts.MaxDepth
		if maxDepth <= 0 {
			maxDepth = 3
		}
		if err := s.AddTaskDependencies(ctx, project, taskID, opts.BlockedBy, maxDepth); err != nil {
			s.db.ExecContext(ctx, "DELETE FROM task_workflows WHERE task_id = ?", taskID)
			s.db.ExecContext(ctx, "DELETE FROM board_tasks WHERE id = ? AND board_id = ?", taskID, project)
			return nil, err
		}
	}

	if opts == nil || !opts.Draft {
		task, err := s.PublishTask(ctx, project, taskID)
		if err != nil {
			return nil, err
		}
		if w.RetryOf != 0 {
			if err := s.RewireRetryDependents(ctx); err != nil {
				return nil, err
			}
			task, err = s.getTaskByID(ctx, project, taskID)
			if err != nil {
				return nil, err
			}
		}
		return task, nil
	}
	return s.getTaskByID(ctx, project, taskID)
}

// ListTasks returns all tasks for a project, ordered by priority then ID.
func (s *Store) ListTasks(ctx context.Context, project string) ([]Task, error) {
	tasks, _, err := s.ListTasksCapped(ctx, project, 0)
	return tasks, err
}

// ListTasksCapped is ListTasks limited to max rows (max <= 0: every row). more says there were rows past the cap, so a caller
// that must bound a reply (the full=1 list a viewer or an agent can ask for, #4602) can say it was cut.
func (s *Store) ListTasksCapped(ctx context.Context, project string, max int) (tasks []Task, more bool, err error) {
	q := `SELECT id, revision, board_id, title, body, status, priority, created_by, assigned_to, completed_by, completion_message, created_at, claimed_at, completed_at, session_id, cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, parked_reason, parked_until, remind_at, goal_id FROM board_tasks WHERE board_id = ?
		 ORDER BY CASE priority WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 END, id ASC`
	args := []any{project}
	if max > 0 {
		q += " LIMIT ?"
		args = append(args, max+1)
	}
	if err = s.reader().SelectContext(ctx, &tasks, q, args...); err != nil {
		return nil, false, err
	}
	if max > 0 && len(tasks) > max {
		// More than the cap: keep what a reader of the whole list needs first, every open task and then the newest done ones
		// (the coordination report reads full=1 for the board's state: the first 500 by priority and id were the OLDEST
		// critical tasks, done long ago, and dropped the newest and the low-priority open ones, #4622). The kept rows come out
		// in the same order as before.
		more = true
		tasks = tasks[:0]
		q = `SELECT id, revision, board_id, title, body, status, priority, created_by, assigned_to, completed_by, completion_message, created_at, claimed_at, completed_at, session_id, cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, parked_reason, parked_until, remind_at, goal_id FROM board_tasks WHERE board_id = ?
		 ORDER BY CASE WHEN ` + doneTaskStatusSQL + ` THEN 1 ELSE 0 END ASC,
		          CASE WHEN ` + doneTaskStatusSQL + ` THEN 0 ELSE CASE priority WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 END END ASC,
		          CASE WHEN ` + doneTaskStatusSQL + ` THEN -id ELSE id END ASC LIMIT ?`
		if err = s.reader().SelectContext(ctx, &tasks, q, project, max); err != nil {
			return nil, false, err
		}
		sort.SliceStable(tasks, func(i, j int) bool {
			ri, rj := taskPriorityRank(tasks[i].Priority), taskPriorityRank(tasks[j].Priority)
			if ri != rj {
				return ri < rj
			}
			return tasks[i].ID < tasks[j].ID
		})
	}
	s.populateTaskDeps(ctx, tasks)
	return tasks, more, nil
}

// taskPriorityRank is the order ListTasks sorts priorities in (critical first); an unknown value sorts first, as SQLite
// sorts a NULL.
func taskPriorityRank(priority string) int {
	switch priority {
	case "critical":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	case "low":
		return 3
	}
	return -1
}

// CountOpenTasksCreatedBy is how many open tasks (pending, in progress, blocked, draft, review pending) createdBy has created on a board.
func (s *Store) CountOpenTasksCreatedBy(ctx context.Context, project, createdBy string) (int, error) {
	var n int
	err := s.reader().GetContext(ctx, &n, `SELECT COUNT(*) FROM board_tasks WHERE board_id = ? AND created_by = ? AND `+openTaskStatusSQL, project, createdBy)
	return n, err
}

// ListOpenTasks returns the board's pending, in-progress, blocked and draft
// tasks with their BlockedBy dependencies, but without workflow hydration
// (Task.Workflow is left zero). It is the cheap read for frequently polled views
// such as the team status endpoint: ListTasks hydrates every task, completed
// ones included, with several queries each, which on a long-lived board means
// thousands of queries holding the single connection per call.
func (s *Store) ListOpenTasks(ctx context.Context, project string) ([]Task, error) {
	var tasks []Task
	err := s.reader().SelectContext(ctx, &tasks,
		`SELECT id, revision, board_id, title, body, status, priority, created_by, assigned_to, completed_by, completion_message, created_at, claimed_at, completed_at, session_id, cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, parked_reason, parked_until, remind_at, goal_id FROM board_tasks
		 WHERE board_id = ? AND status IN ('pending', 'in_progress', 'blocked', 'draft')
		 ORDER BY CASE priority WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 END, id ASC`,
		project)
	if err != nil {
		return nil, err
	}
	s.loadBlockedBy(ctx, tasks)
	return tasks, nil
}

// TasksUnderReview returns a board's tasks waiting for their completion review, by id,
// with their assignees: a candidate submitted for review (still in_progress, or
// review_pending once the worker's slot is released). The team status says
// "awaiting-review" only for these (#3520).
func (s *Store) TasksUnderReview(ctx context.Context, project string) (map[int64]string, error) {
	var rows []struct {
		ID         int64  `db:"id"`
		AssignedTo string `db:"assigned_to"`
	}
	err := s.reader().SelectContext(ctx, &rows,
		`SELECT t.id, COALESCE(t.assigned_to, '') AS assigned_to FROM board_tasks t
		 WHERE t.board_id = ? AND (t.status = 'review_pending' OR (t.status = 'in_progress' AND EXISTS (
		   SELECT 1 FROM task_workflows w WHERE w.task_id = t.id AND json_extract(w.data, '$.completion_review') IS NOT NULL)))`, project)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(rows))
	for _, r := range rows {
		out[r.ID] = r.AssignedTo
	}
	return out, nil
}

// ListAllTasks returns the most recent tasks across all boards.
func (s *Store) ListAllTasks(ctx context.Context, limit int) ([]Task, error) {
	if limit <= 0 {
		limit = 100
	}
	var tasks []Task
	err := s.reader().SelectContext(ctx, &tasks,
		`SELECT id, revision, board_id, title, body, status, priority, created_by, assigned_to, completed_by, completion_message, created_at, claimed_at, completed_at, session_id, cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, parked_reason, parked_until, remind_at, goal_id FROM board_tasks
		 ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	s.populateTaskDeps(ctx, tasks)
	return tasks, nil
}

// populateTaskDeps batch-loads dependencies for a slice of tasks.
func (s *Store) populateTaskDeps(ctx context.Context, tasks []Task) {
	s.hydrateWorkflows(ctx, tasks)
	s.loadBlockedBy(ctx, tasks)
}

// loadBlockedBy batch-loads the BlockedBy dependencies for a slice of tasks.
func (s *Store) loadBlockedBy(ctx context.Context, tasks []Task) {
	if len(tasks) == 0 {
		return
	}
	ids := make([]int64, len(tasks))
	idMap := make(map[int64]*Task, len(tasks))
	for i := range tasks {
		ids[i] = tasks[i].ID
		idMap[tasks[i].ID] = &tasks[i]
	}

	// Build IN clause
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(
		`SELECT td.task_id, td.blocked_by_task_id, td.blocked_by_board_id, bt.title, bt.status
		 FROM task_dependencies td
		 LEFT JOIN board_tasks bt ON bt.id = td.blocked_by_task_id
		 WHERE td.task_id IN (%s)`, strings.Join(placeholders, ","))

	var rows []struct {
		TaskID           int64   `db:"task_id"`
		BlockedByTaskID  int64   `db:"blocked_by_task_id"`
		BlockedByBoardID string  `db:"blocked_by_board_id"`
		Title            *string `db:"title"`
		Status           *string `db:"status"`
	}
	if err := s.reader().SelectContext(ctx, &rows, query, args...); err != nil {
		return
	}
	if len(rows) == 0 {
		return
	}
	// Like loadDependencyRule per dependency, but batched; on error the
	// dependencies are listed without rule evaluation, as before.
	rules, rulesErr := s.loadDependencyRules(ctx, ids)
	for _, r := range rows {
		t := idMap[r.TaskID]
		if t == nil {
			continue
		}
		dep := TaskDep{TaskID: r.BlockedByTaskID, BoardID: r.BlockedByBoardID}
		if rulesErr == nil {
			applyDependencyRule(&dep, rules[dependencyRuleKey{t.ID, dep.TaskID}])
		}
		if r.Title != nil {
			dep.Title = *r.Title
		}
		if r.Status != nil {
			dep.Status = *r.Status
		}
		t.BlockedBy = append(t.BlockedBy, dep)
	}
}

// HasActiveTaskForAssignee returns true when the assignee already has another
// in-progress task on the same board. excludeTaskID can be used to ignore the
// task currently being created or reassigned.
func (s *Store) HasActiveTaskForAssignee(ctx context.Context, project, assignee string, excludeTaskID int64) (bool, error) {
	if assignee == "" {
		return false, nil
	}

	var count int
	err := s.reader().GetContext(ctx, &count,
		`SELECT COUNT(1) FROM board_tasks
		 WHERE board_id = ? AND assigned_to = ? AND status = 'in_progress' AND id != ?`,
		project, assignee, excludeTaskID)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// FindIdleSubscriber returns a random active subscriber with no in-progress tasks, or nil.
// skip, when set, excludes subscribers the board cannot judge on its own (for
// example archived agents, which the session store knows about).
func (s *Store) FindIdleSubscriber(ctx context.Context, project string, skip func(*Subscriber) bool) *Subscriber {
	var subs []Subscriber
	err := s.reader().SelectContext(ctx, &subs,
		`SELECT * FROM board_subscribers
		 WHERE project = ? AND is_active = 1 AND session_name != ''
		 AND subscriber_id NOT IN (
		     SELECT assigned_to FROM board_tasks WHERE board_id = ? AND status = 'in_progress' AND assigned_to IS NOT NULL
		 )
		 ORDER BY RANDOM()`, project, project)
	if err != nil {
		return nil
	}
	for i := range subs {
		if skip == nil || !skip(&subs[i]) {
			return &subs[i]
		}
	}
	return nil
}

// ActiveTaskForSubscriber returns the subscriber's current in-progress task, or nil.
func (s *Store) ActiveTaskForSubscriber(ctx context.Context, project, subscriberID string) *Task {
	var task Task
	err := s.reader().GetContext(ctx, &task,
		`SELECT id, revision, board_id, title, body, status, priority, created_by, assigned_to, completed_by, completion_message, created_at, claimed_at, completed_at, session_id, cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, goal_id
		 FROM board_tasks WHERE board_id = ? AND assigned_to = ? AND status = 'in_progress'
		 ORDER BY claimed_at DESC, id DESC LIMIT 1`, project, subscriberID)
	if err != nil {
		return nil
	}
	result, err := s.getTaskByID(ctx, project, task.ID)
	if err != nil {
		return nil
	}
	return result
}

// NextPendingTaskForSubscriber returns the next pending task assigned to or
// available for the subscriber, without claiming it. Returns nil if none.
func (s *Store) NextPendingTaskForSubscriber(ctx context.Context, project, subscriberID string) *Task {
	priorityOrder := `CASE priority WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 END, id ASC`
	var task Task
	// Check assigned tasks first
	err := s.db.GetContext(ctx, &task,
		`SELECT id, revision, board_id, title, body, status, priority, created_by, assigned_to, completed_by, completion_message, created_at, claimed_at, completed_at, session_id, cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, goal_id
		 FROM board_tasks WHERE board_id = ? AND status = 'pending' AND assigned_to = ? AND NOT `+parkedSQL+`
		 ORDER BY `+priorityOrder+` LIMIT 1`, project, subscriberID)
	if err == nil {
		return &task
	}
	// Then unassigned
	err = s.db.GetContext(ctx, &task,
		`SELECT id, revision, board_id, title, body, status, priority, created_by, assigned_to, completed_by, completion_message, created_at, claimed_at, completed_at, session_id, cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, goal_id
		 FROM board_tasks WHERE board_id = ? AND status = 'pending' AND (assigned_to IS NULL OR assigned_to = '') AND NOT `+parkedSQL+`
		 ORDER BY `+priorityOrder+` LIMIT 1`, project)
	if err == nil {
		return &task
	}
	return nil
}

// ClaimTask finds and atomically claims the best pending task for the subscriber.
// It first looks for tasks assigned to the subscriber, then unassigned tasks.
// Tasks are ordered by priority (critical > high > medium > low), then by ID.
// Returns nil if no tasks are available.
// An agent cannot claim a new task while they have an in-progress task. A parked task is never offered or claimed until it is unparked (#5183).
func (s *Store) ClaimTask(ctx context.Context, project, subscriberID string, requestedID ...int64) (*Task, error) {
	now := nowUTC()
	priorityOrder := `CASE priority WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 END, id ASC`

	var activeCount int
	if err := s.db.GetContext(ctx, &activeCount,
		`SELECT COUNT(*) FROM board_tasks WHERE board_id = ? AND assigned_to = ? AND status = 'in_progress'`,
		project, subscriberID); err == nil && activeCount > 0 {
		return nil, fmt.Errorf("complete your current task before claiming a new one")
	}

	// Assigned pending work is always considered before the unassigned pool.
	// Keep all candidates so a stale pending row with unmet dependencies cannot
	// hide a later ready task.
	var candidateIDs []int64
	if len(requestedID) > 0 && requestedID[0] > 0 {
		if err := s.db.SelectContext(ctx, &candidateIDs, `SELECT id FROM board_tasks WHERE id = ? AND board_id = ? AND status = 'pending' AND (assigned_to = ? OR assigned_to IS NULL OR assigned_to = '') AND NOT `+parkedSQL, requestedID[0], project, subscriberID); err != nil || len(candidateIDs) == 0 {
			if t, gerr := s.GetTask(ctx, project, requestedID[0]); gerr == nil && t != nil && t.Status == "pending" && t.Parked(time.Now()) {
				return nil, fmt.Errorf("task #%d is parked and cannot be claimed until it is unparked", requestedID[0])
			}
			return nil, fmt.Errorf("requested task is not available to this subscriber")
		}
	} else {
		var assigned, unassigned []int64
		if err := s.db.SelectContext(ctx, &assigned, `SELECT id FROM board_tasks WHERE board_id = ? AND status = 'pending' AND assigned_to = ? AND NOT `+parkedSQL+` ORDER BY `+priorityOrder, project, subscriberID); err != nil {
			return nil, err
		}
		if err := s.db.SelectContext(ctx, &unassigned, `SELECT id FROM board_tasks WHERE board_id = ? AND status = 'pending' AND (assigned_to IS NULL OR assigned_to = '') AND NOT `+parkedSQL+` ORDER BY `+priorityOrder, project); err != nil {
			return nil, err
		}
		candidateIDs = append(assigned, unassigned...)
	}

	for _, taskID := range candidateIDs {
		tx, err := s.db.BeginTxx(ctx, nil)
		if err != nil {
			return nil, err
		}
		inputs, ready, err := dependencyInputs(ctx, tx, taskID)
		if err != nil {
			tx.Rollback()
			return nil, err
		}
		if !ready {
			// Reconcile stale queue metadata and continue searching this claim.
			if _, err := tx.ExecContext(ctx, "UPDATE board_tasks SET status = 'blocked', edited_at = ? WHERE id = ? AND board_id = ? AND status = 'pending'", nowUTC(), taskID, project); err != nil {
				tx.Rollback()
				return nil, err
			}
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			continue
		}
		result, err := tx.ExecContext(ctx,
			`UPDATE board_tasks SET status = 'in_progress', assigned_to = ?, claimed_at = ?, last_activity_at = ?
			 WHERE id = ? AND board_id = ? AND status = 'pending'
			   AND (assigned_to = ? OR assigned_to IS NULL OR assigned_to = '')
			   AND NOT EXISTS (SELECT 1 FROM board_tasks WHERE board_id = ? AND assigned_to = ? AND status = 'in_progress')`,
			subscriberID, now, now, taskID, project, subscriberID, project, subscriberID)
		if err != nil {
			tx.Rollback()
			return nil, err
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			var hasActive int
			tx.GetContext(ctx, &hasActive, `SELECT COUNT(*) FROM board_tasks WHERE board_id = ? AND assigned_to = ? AND status = 'in_progress'`, project, subscriberID)
			tx.Rollback()
			if hasActive > 0 {
				return nil, fmt.Errorf("complete your current task before claiming a new one")
			}
			continue
		}
		w, err := loadWorkflow(ctx, tx, taskID)
		if err != nil {
			tx.Rollback()
			return nil, err
		}
		if w.TeamMode == nil {
			mode, err := loadWorkingMode(ctx, tx, project)
			if err != nil {
				tx.Rollback()
				return nil, err
			}
			w.TeamMode = &mode
			if mode.Instructions != "" {
				w.Instructions += "\n\nTeam working instructions:\n" + mode.Instructions
			}
		}
		w.Inputs = inputs
		if err := saveWorkflow(ctx, tx, taskID, w); err != nil {
			tx.Rollback()
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		if s.sessionsDB != nil {
			var sessionName string
			if err := s.db.GetContext(ctx, &sessionName, `SELECT session_name FROM board_subscribers WHERE subscriber_id = ? AND project = ? AND is_active = 1 LIMIT 1`, subscriberID, project); err == nil && sessionName != "" {
				sessionUUID := naming.SessionIDFromName(sessionName)
				var exists int
				if err := s.sessionsDB.GetContext(ctx, &exists, `SELECT 1 FROM live_sessions WHERE session_id = ? AND status = 'active' LIMIT 1`, sessionUUID); err == nil {
					s.db.ExecContext(ctx, `UPDATE board_tasks SET session_id = ? WHERE id = ? AND board_id = ?`, sessionUUID, taskID, project)
				}
			}
		}
		return s.getTaskByID(ctx, project, taskID)
	}
	return nil, nil
}

// TouchActiveTask records activity by an agent without changing task state.
// It is intentionally best-effort: activity tracking must never block the
// agent's normal board operation.
func (s *Store) TouchActiveTask(ctx context.Context, project, subscriberID string) error {
	if project == "" || subscriberID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE board_tasks SET last_activity_at = ?
		WHERE board_id = ? AND assigned_to = ? AND status = 'in_progress'`, nowUTC(), project, subscriberID)
	return err
}

// TouchTask records activity for a specific active task.
func (s *Store) TouchTask(ctx context.Context, project string, taskID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE board_tasks SET last_activity_at = ?
		WHERE board_id = ? AND id = ? AND status = 'in_progress'`, nowUTC(), project, taskID)
	return err
}

// SnoozeTaskReminder suppresses inactivity reminders until the supplied time.
func (s *Store) SnoozeTaskReminder(ctx context.Context, project string, taskID int64, until time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE board_tasks SET idle_snoozed_until = ?
		WHERE board_id = ? AND id = ? AND status = 'in_progress'`, until.UTC().Format(time.RFC3339), project, taskID)
	return err
}

// IdleTasks returns active tasks that have had no recorded activity since the
// supplied time. Older databases use claimed_at until activity is recorded.
func (s *Store) IdleTasks(ctx context.Context, project string, before time.Time) ([]Task, error) {
	var tasks []Task
	err := s.reader().SelectContext(ctx, &tasks, `SELECT id, revision, board_id, title, body, status, priority,
		created_by, assigned_to, completed_by, completion_message, created_at, claimed_at,
		completed_at, session_id, cost_usd, input_tokens, output_tokens, cache_read_tokens,
		cache_write_tokens FROM board_tasks
		WHERE board_id = ? AND status = 'in_progress' AND COALESCE(last_activity_at, claimed_at) IS NOT NULL
		AND NOT `+parkedSQL+`
		AND COALESCE(last_activity_at, claimed_at) < ?
		AND (idle_snoozed_until IS NULL OR idle_snoozed_until <= ?)`, project, before.UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339))
	return tasks, err
}

// CompleteTask marks a task as completed. Pending tasks can be completed
// without being claimed first: the operator is never prompted to claim the
// tasks assigned to them, and just marks them done.
func (s *Store) CompleteTask(ctx context.Context, project string, taskID int64, subscriberID string, message *string) (*Task, error) {
	return s.CompleteTaskWithArtifacts(ctx, project, taskID, subscriberID, message, "success", nil)
}

// ErrReviewTaskReassign: a review task is not reassigned; its reviewers are changed on the task under review.
var ErrReviewTaskReassign = errors.New("a review task cannot be reassigned")

// NotAssigneeError is a refusal to complete a task assigned to someone else: a permission refusal (the routes answer 403), not a malformed request.
type NotAssigneeError struct{ Msg string }

func (e *NotAssigneeError) Error() string { return e.Msg }

// ReassignTask resets a task back to pending, optionally with a new assignee.
// Works on pending, in_progress, or blocked tasks, preserving blocked status.
// If assignee is empty, the task becomes unassigned.
func (s *Store) ReassignTask(ctx context.Context, project string, taskID int64, assignee string) (*Task, error) {
	// A review task's reviewer is a place in the reviewed task's round: giving the task to someone else would leave the round naming the old reviewer, and the new one would be refused
	// ("no open review") when they gave their verdict. Reviewers change through the reviewed task (#5191).
	if w, err := loadWorkflow(ctx, s.db, taskID); err == nil && w.ReviewOf > 0 {
		return nil, fmt.Errorf("%w: #%d is the review of #%d: change its reviewers with: coral-board task review %d --to \"Name,Name\"", ErrReviewTaskReassign, taskID, w.ReviewOf, w.ReviewOf)
	}
	var assignPtr *string
	if assignee != "" {
		assignPtr = &assignee
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE board_tasks
		 SET status = CASE WHEN status = 'blocked' THEN 'blocked' ELSE 'pending' END, assigned_to = ?, claimed_at = NULL, session_id = NULL
		 WHERE id = ? AND board_id = ? AND status IN ('pending', 'in_progress', 'blocked') AND NOT EXISTS (SELECT 1 FROM task_workflows w WHERE w.task_id=board_tasks.id AND json_extract(w.data,'$.completion_review') IS NOT NULL)`,
		assignPtr, taskID, project)
	if err != nil {
		return nil, err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("task #%d cannot be reassigned (finished, awaiting review, or not found)", taskID)
	}

	return s.getTaskByID(ctx, project, taskID)
}

// TaskUpdate holds optional fields for updating a task.
// TaskUpdate holds optional fields for updating a task.
type TaskUpdate struct {
	Title      *string    `json:"title,omitempty"`
	Body       *string    `json:"body,omitempty"`
	Priority   *string    `json:"priority,omitempty"`
	AssignedTo *string    `json:"assigned_to,omitempty"`
	BlockedBy  *[]TaskDep `json:"blocked_by,omitempty"`
}

// UpdateTask applies partial updates to a pending, in_progress, or blocked task.
// If AssignedTo changes on an in_progress task, the task resets to pending.
// Returns (updatedTask, previousStatus, error) so the handler can detect status transitions.
func (s *Store) UpdateTask(ctx context.Context, project string, taskID int64, updates TaskUpdate, maxDepth int) (*Task, string, error) {
	task, err := s.getTaskByID(ctx, project, taskID)
	if err != nil {
		return nil, "", fmt.Errorf("task #%d not found", taskID)
	}
	if task.Status == "completed" || task.Status == "skipped" || task.Status == "review_pending" {
		return nil, "", fmt.Errorf("task #%d cannot be edited (status: %s)", taskID, task.Status)
	}
	prevStatus := task.Status
	if updates.BlockedBy != nil && task.Status == "in_progress" {
		return nil, "", fmt.Errorf("dependencies can only change before a task starts")
	}

	setClauses := []string{}
	args := []any{}

	if updates.Title != nil {
		if *updates.Title == "" {
			return nil, "", fmt.Errorf("title cannot be empty")
		}
		setClauses = append(setClauses, "title = ?")
		args = append(args, *updates.Title)
	}
	if updates.Body != nil {
		setClauses = append(setClauses, "body = ?")
		args = append(args, *updates.Body)
	}
	if updates.Priority != nil {
		switch *updates.Priority {
		case "critical", "high", "medium", "low":
		default:
			return nil, "", fmt.Errorf("invalid priority: %s", *updates.Priority)
		}
		setClauses = append(setClauses, "priority = ?")
		args = append(args, *updates.Priority)
	}
	if updates.AssignedTo != nil {
		setClauses = append(setClauses, "assigned_to = ?")
		if *updates.AssignedTo == "" {
			args = append(args, nil)
		} else {
			args = append(args, *updates.AssignedTo)
		}
		if task.Status == "in_progress" {
			setClauses = append(setClauses, "status = 'pending'", "claimed_at = NULL", "session_id = NULL")
		}
	}

	// Handle blocked_by updates
	if updates.BlockedBy != nil {
		if maxDepth <= 0 {
			maxDepth = 3
		}
		if err := s.AddTaskDependencies(ctx, project, taskID, *updates.BlockedBy, maxDepth); err != nil {
			return nil, "", err
		}
	}
	if len(setClauses) > 0 {
		setClauses = append(setClauses, "edited_at = ?") // the edit moves updated_at (#5819)
		args = append(args, nowUTC())
		query := fmt.Sprintf("UPDATE board_tasks SET %s WHERE id = ? AND board_id = ? AND status NOT IN ('completed', 'skipped', 'review_pending') AND NOT EXISTS (SELECT 1 FROM task_workflows w WHERE w.task_id=board_tasks.id AND json_extract(w.data,'$.completion_review') IS NOT NULL)", strings.Join(setClauses, ", "))
		args = append(args, taskID, project)
		result, err := s.db.ExecContext(ctx, query, args...)
		if err != nil {
			return nil, "", err
		}
		if n, _ := result.RowsAffected(); n == 0 {
			return nil, "", fmt.Errorf("task has already finished")
		}
	}

	result, err := s.getTaskByID(ctx, project, taskID)
	if err != nil {
		return nil, "", err
	}
	return result, prevStatus, nil
}

// PublishTask transitions a draft task to pending or blocked based on its dependencies.
func (s *Store) PublishTask(ctx context.Context, project string, taskID int64) (*Task, error) {
	task, err := s.getTaskByID(ctx, project, taskID)
	if err != nil {
		return nil, fmt.Errorf("task #%d not found", taskID)
	}
	if task.Status != "draft" {
		return nil, fmt.Errorf("task #%d is not a draft (status: %s)", taskID, task.Status)
	}

	// Check if task has unresolved dependencies.
	_, ready, err := dependencyInputs(ctx, s.db, taskID)
	if err != nil {
		return nil, err
	}
	newStatus := "pending"
	if !ready {
		newStatus = "blocked"
	}

	result, err := s.db.ExecContext(ctx,
		"UPDATE board_tasks SET status = ? WHERE id = ? AND board_id = ? AND status = 'draft'",
		newStatus, taskID, project) // not an edit: every new task is published here as it is created
	if err != nil {
		return nil, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("task is no longer a draft")
	}

	return s.getTaskByID(ctx, project, taskID)
}

// CancelTask marks a task as skipped. Can cancel pending, in_progress, or blocked tasks.
func (s *Store) CancelTask(ctx context.Context, project string, taskID int64, subscriberID string, message *string) (*Task, error) {
	now := nowUTC()
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx,
		`UPDATE board_tasks
		 SET status = 'skipped', completed_by = ?, completion_message = ?, completed_at = ?
		 WHERE id = ? AND board_id = ? AND status IN ('pending', 'in_progress', 'blocked', 'draft', 'review_pending')`,
		subscriberID, message, now, taskID, project)
	if err != nil {
		return nil, err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("task #%d cannot be cancelled (already completed or not found)", taskID)
	}
	if err := resolveCompletedTask(ctx, tx, taskID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.computeAndStoreTaskCost(ctx, project, taskID)

	return s.getTaskByID(ctx, project, taskID)
}
