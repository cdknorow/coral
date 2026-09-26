package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/cdknorow/coral/internal/board"
	"log"
	"path/filepath"
	"strings"

	"github.com/jmoiron/sqlx"
)

// AgentTask represents a checklist item for an agent.
type AgentTask struct {
	Status            string             `db:"-" json:"status"`
	Workflow          board.TaskWorkflow `db:"-" json:"workflow"`
	BlockedBy         []board.TaskDep    `db:"-" json:"blocked_by,omitempty"`
	ID                int64              `db:"id" json:"id"`
	AgentName         string             `db:"agent_name" json:"agent_name"`
	SessionID         *string            `db:"session_id" json:"session_id,omitempty"`
	Title             string             `db:"title" json:"title"`
	Body              *string            `db:"body" json:"body,omitempty"`
	Priority          *string            `db:"priority" json:"priority,omitempty"`
	CompletionMessage *string            `db:"completion_message" json:"completion_message,omitempty"`
	Completed         int                `db:"completed" json:"completed"`
	SortOrder         int                `db:"sort_order" json:"sort_order"`
	CreatedAt         string             `db:"created_at" json:"created_at"`
	UpdatedAt         string             `db:"updated_at" json:"updated_at"`
	StartedAt         *string            `db:"started_at" json:"started_at,omitempty"`
	CompletedAt       *string            `db:"completed_at" json:"completed_at,omitempty"`
	CostUSD           float64            `db:"cost_usd" json:"cost_usd"`
	InputTokens       int                `db:"input_tokens" json:"input_tokens"`
	OutputTokens      int                `db:"output_tokens" json:"output_tokens"`
	CacheReadTokens   int                `db:"cache_read_tokens" json:"cache_read_tokens"`
	CacheWriteTokens  int                `db:"cache_write_tokens" json:"cache_write_tokens"`
	DisplayName       *string            `db:"display_name" json:"display_name,omitempty"`
}

// AgentNote represents a note for an agent.
type AgentNote struct {
	ID        int64   `db:"id" json:"id"`
	AgentName string  `db:"agent_name" json:"agent_name"`
	SessionID *string `db:"session_id" json:"session_id,omitempty"`
	Content   string  `db:"content" json:"content"`
	CreatedAt string  `db:"created_at" json:"created_at"`
	UpdatedAt string  `db:"updated_at" json:"updated_at"`
}

// AgentEvent represents an event recorded for an agent.
type AgentEvent struct {
	ID         int64   `db:"id" json:"id"`
	AgentName  string  `db:"agent_name" json:"agent_name"`
	SessionID  *string `db:"session_id" json:"session_id,omitempty"`
	EventType  string  `db:"event_type" json:"event_type"`
	ToolName   *string `db:"tool_name" json:"tool_name,omitempty"`
	Summary    string  `db:"summary" json:"summary"`
	DetailJSON *string `db:"detail_json" json:"detail_json,omitempty"`
	CreatedAt  string  `db:"created_at" json:"created_at"`
	// DurationMs is how long the activity took, when the source reports it.
	DurationMs *int64 `db:"duration_ms" json:"duration_ms,omitempty"`
	// RefID is the activity's id at its source: tool_use_id for tool calls,
	// the API request id for thinking turns.
	RefID *string `db:"ref_id" json:"ref_id,omitempty"`
}

// ToolCount holds a tool name and its usage count.
type ToolCount struct {
	ToolName string `db:"tool_name" json:"tool_name"`
	Count    int    `db:"count" json:"count"`
}

// TaskStore provides agent tasks, notes, and events operations.
type TaskStore struct {
	db *DB
}

// NewTaskStore creates a new TaskStore.
func NewTaskStore(db *DB) *TaskStore {
	return &TaskStore{db: db}
}

// ── Agent Tasks ────────────────────────────────────────────────────────

// ListAgentTasks returns tasks for an agent, optionally scoped by session.
func (s *TaskStore) ListAgentTasks(ctx context.Context, agentName string, sessionID *string) ([]AgentTask, error) {
	filter, filterArgs := sessionFilter(sessionID)
	args := append([]interface{}{agentName}, filterArgs...)
	var tasks []AgentTask
	err := s.db.SelectContext(ctx, &tasks,
		`SELECT id, agent_name, session_id, title, body, priority, completion_message, completed, sort_order, created_at, updated_at,
		        started_at, completed_at, cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, display_name
		 FROM agent_tasks WHERE agent_name = ?`+filter+` ORDER BY sort_order`,
		args...)
	if err != nil {
		return nil, err
	}
	for i := range tasks {
		if err := s.hydrateAgentTask(ctx, &tasks[i]); err != nil {
			return nil, err
		}
	}
	return tasks, nil
}

// CreateAgentTask creates a new task with auto-incrementing sort order.
func (s *TaskStore) CreateAgentTask(ctx context.Context, agentName, title string, sessionID *string, displayName *string) (*AgentTask, error) {
	return s.CreateAgentTaskWithWorkflow(ctx, agentName, title, sessionID, displayName, "", "medium", nil)
}

// GetAgentTask returns one agent task by ID, or nil.
func (s *TaskStore) GetAgentTask(ctx context.Context, taskID int64) (*AgentTask, error) {
	var t AgentTask
	err := s.db.GetContext(ctx, &t, "SELECT * FROM agent_tasks WHERE id = ?", taskID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := s.hydrateAgentTask(ctx, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// Agent task states (the completed column): pending, done, in progress, and
// cancelled (reported as "skipped", like board tasks).
const (
	AgentTaskPending    = 0
	AgentTaskDone       = 1
	AgentTaskInProgress = 2
	AgentTaskCancelled  = 3
	AgentTaskBlocked    = 4
	AgentTaskDraft      = 5
)

// SetAgentTaskDetails stores a task's details and priority (empty = unchanged).
func (s *TaskStore) SetAgentTaskDetails(ctx context.Context, taskID int64, body, priority string) error {
	updates := board.TaskUpdate{}
	if body != "" {
		updates.Body = &body
	}
	if priority != "" {
		updates.Priority = &priority
	}
	_, err := s.UpdateAgentTaskWorkflow(ctx, taskID, updates)
	return err
}

// CurrentAgentTask returns the agent's task in progress, or nil.
func (s *TaskStore) CurrentAgentTask(ctx context.Context, agentName string, sessionID *string) (*AgentTask, error) {
	filter, filterArgs := sessionFilter(sessionID)
	var t AgentTask
	err := s.db.GetContext(ctx, &t,
		"SELECT * FROM agent_tasks WHERE agent_name = ? AND completed = 2"+filter+" ORDER BY started_at DESC, id DESC LIMIT 1",
		append([]interface{}{agentName}, filterArgs...)...)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if t.Workflow.Instructions == "" {
		if err := s.hydrateAgentTask(ctx, &t); err != nil {
			return nil, err
		}
	}
	return &t, nil
}

// FinishAgentTask marks a task done or cancelled, with an optional message.
func (s *TaskStore) FinishAgentTask(ctx context.Context, taskID int64, state int, message string) error {
	return s.FinishAgentTaskWithArtifacts(ctx, taskID, state, message, "success", nil)
}

// Claim order, as for board tasks: highest priority first, then oldest.
const agentTaskClaimOrder = `CASE COALESCE(priority, 'medium') WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 2 END, id ASC`

// ClaimNextAgentTask uses the shared workflow engine to claim one ready task.
// It rejects additional claims until the session's active task is finished.
func (s *TaskStore) ClaimNextAgentTask(ctx context.Context, agentName string, sessionID *string, requested ...int64) (*AgentTask, error) {
	if active, err := s.CurrentAgentTask(ctx, agentName, sessionID); err != nil {
		return nil, err
	} else if active != nil {
		return nil, fmt.Errorf("complete your current task before claiming a new one")
	}
	var task *board.Task
	var err error
	if len(requested) > 0 && requested[0] > 0 {
		own, e := s.GetAgentTask(ctx, requested[0])
		if e != nil {
			return nil, e
		}
		if own == nil || own.AgentName != agentName || sessionID != nil && (own.SessionID == nil || *own.SessionID != *sessionID) {
			return nil, fmt.Errorf("requested task is not available to this agent")
		}
	}
	task, err = s.db.TaskEngine.ClaimTask(ctx, AgentTaskProject(agentName, sessionID), agentName, requested...)
	if err != nil || task == nil {
		return nil, err
	}
	return s.GetAgentTask(ctx, task.ID)
}

// FindOpenAgentTask returns the agent's not-yet-completed task with this exact
// title (in the same session when one is given), or nil. Used to avoid a
// duplicate row when the operator creates a task and the agent then adds the
// same task to its own list (the task-sync hook posts it by title).
func (s *TaskStore) FindOpenAgentTask(ctx context.Context, agentName, title string, sessionID *string) (*AgentTask, error) {
	filter, filterArgs := sessionFilter(sessionID)
	var t AgentTask
	err := s.db.GetContext(ctx, &t,
		"SELECT * FROM agent_tasks WHERE agent_name = ? AND title = ? AND completed IN (0, 2, 4, 5)"+filter+" ORDER BY id LIMIT 1",
		append([]interface{}{agentName, title}, filterArgs...)...)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if t.Workflow.Instructions == "" {
		if err := s.hydrateAgentTask(ctx, &t); err != nil {
			return nil, err
		}
	}
	return &t, nil
}

// UpdateAgentTask updates task fields (title, completed, sort_order).
// When completed transitions to 2 (in_progress), started_at is set.
// When completed transitions to 1 (done), completed_at is set and cost is computed
// from token_usage records between started_at and completed_at.
func (s *TaskStore) UpdateAgentTask(ctx context.Context, taskID int64, title *string, completed *int, sortOrder *int) error {
	task, err := s.GetAgentTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return fmt.Errorf("task not found")
	}
	if title != nil {
		if _, err := s.UpdateAgentTaskWorkflow(ctx, taskID, board.TaskUpdate{Title: title}); err != nil {
			return err
		}
	}
	if completed != nil {
		switch *completed {
		case AgentTaskDone, AgentTaskCancelled:
			if err := s.FinishAgentTask(ctx, taskID, *completed, ""); err != nil {
				return err
			}
		case AgentTaskInProgress:
			if task.Completed != AgentTaskInProgress {
				if _, err := s.ClaimNextAgentTask(ctx, task.AgentName, task.SessionID, taskID); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("use a new retry task instead of resetting task status")
		}
	}
	if sortOrder != nil {
		_, err = s.db.ExecContext(ctx, "UPDATE agent_tasks SET sort_order=? WHERE id=?", *sortOrder, taskID)
	}
	return err
}

// computeAgentTaskCost sums token_usage records for the task's session
// between started_at and completed_at, and updates the task's cost fields.
func (s *TaskStore) computeAgentTaskCost(ctx context.Context, taskID int64, completedAt string) {
	// Fetch the task to get session_id and started_at
	var task struct {
		SessionID *string `db:"session_id"`
		StartedAt *string `db:"started_at"`
	}
	err := s.db.GetContext(ctx, &task,
		"SELECT session_id, started_at FROM agent_tasks WHERE id = ?", taskID)
	if err != nil || task.SessionID == nil || task.StartedAt == nil {
		return
	}

	// Sum token_usage between started_at and completed_at for this session.
	// Use datetime() to normalize timezone suffixes (+00:00 vs Z).
	var usage struct {
		InputTokens      int     `db:"input_tokens"`
		OutputTokens     int     `db:"output_tokens"`
		CacheReadTokens  int     `db:"cache_read_tokens"`
		CacheWriteTokens int     `db:"cache_write_tokens"`
		CostUSD          float64 `db:"cost_usd"`
	}
	err = s.db.GetContext(ctx, &usage,
		`SELECT COALESCE(SUM(input_tokens), 0) as input_tokens,
		        COALESCE(SUM(output_tokens), 0) as output_tokens,
		        COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens,
		        COALESCE(SUM(cache_write_tokens), 0) as cache_write_tokens,
		        COALESCE(SUM(cost_usd), 0) as cost_usd
		 FROM token_usage
		 WHERE session_id = ? AND datetime(recorded_at) >= datetime(?) AND datetime(recorded_at) <= datetime(?)`,
		*task.SessionID, *task.StartedAt, completedAt)
	if err != nil {
		log.Printf("[store] compute agent task cost query failed for task %d: %v", taskID, err)
		return
	}

	if _, err := s.db.ExecContext(ctx,
		`UPDATE agent_tasks SET cost_usd = ?, input_tokens = ?, output_tokens = ?,
		        cache_read_tokens = ?, cache_write_tokens = ?
		 WHERE id = ?`,
		usage.CostUSD, usage.InputTokens, usage.OutputTokens,
		usage.CacheReadTokens, usage.CacheWriteTokens, taskID); err != nil {
		log.Printf("[store] failed to update agent task cost for task %d: %v", taskID, err)
	}
}

// CompleteAgentTaskByTitle marks a task as completed by title match.
func (s *TaskStore) CompleteAgentTaskByTitle(ctx context.Context, agentName, title string, sessionID *string) error {
	task, err := s.FindOpenAgentTask(ctx, agentName, title, sessionID)
	if err != nil || task == nil {
		return err
	}
	return s.FinishAgentTask(ctx, task.ID, AgentTaskDone, "")
}

// DeleteAgentTask deletes a task by ID.
func (s *TaskStore) DeleteAgentTask(ctx context.Context, taskID int64) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err := tx.GetContext(ctx, &status, "SELECT status FROM board_tasks WHERE id=?", taskID); err != nil {
		return err
	}
	if status == "completed" || status == "skipped" || status == "in_progress" {
		return fmt.Errorf("only unstarted tasks can be deleted; finished results are immutable")
	}
	var incoming int
	if err := tx.GetContext(ctx, &incoming, `SELECT
 (SELECT COUNT(*) FROM task_dependencies WHERE blocked_by_task_id=?) +
 (SELECT COUNT(*) FROM task_workflows WHERE json_extract(data,'$.parent_task_id')=? OR json_extract(data,'$.retry_of')=?)`, taskID, taskID, taskID); err != nil {
		return err
	}
	if incoming > 0 {
		return fmt.Errorf("task is required by other tasks; cancel it instead")
	}
	for _, query := range []string{"DELETE FROM task_dependency_rules WHERE task_id=?", "DELETE FROM task_dependencies WHERE task_id=?", "DELETE FROM task_ready_notifications WHERE task_id=?", "DELETE FROM task_workflows WHERE task_id=?", "DELETE FROM board_tasks WHERE id=?"} {
		if _, err := tx.ExecContext(ctx, query, taskID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReorderAgentTasks sets sort_order based on the provided ID order.
func (s *TaskStore) ReorderAgentTasks(ctx context.Context, agentName string, taskIDs []int64) error {
	now := nowUTC()
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		for idx, tid := range taskIDs {
			if _, err := tx.ExecContext(ctx,
				"UPDATE agent_tasks SET sort_order = ?, updated_at = ? WHERE id = ? AND agent_name = ?",
				idx, now, tid, agentName); err != nil {
				return err
			}
		}
		return nil
	})
}

// ── Agent Notes ────────────────────────────────────────────────────────

// ListAgentNotes returns notes for an agent, optionally scoped by session.
func (s *TaskStore) ListAgentNotes(ctx context.Context, agentName string, sessionID *string) ([]AgentNote, error) {
	filter, filterArgs := sessionFilter(sessionID)
	args := append([]interface{}{agentName}, filterArgs...)
	var notes []AgentNote
	err := s.db.SelectContext(ctx, &notes,
		`SELECT id, agent_name, content, created_at, updated_at
		 FROM agent_notes WHERE agent_name = ?`+filter+` ORDER BY created_at DESC`,
		args...)
	return notes, err
}

// CreateAgentNote creates a new note.
func (s *TaskStore) CreateAgentNote(ctx context.Context, agentName, content string, sessionID *string) (*AgentNote, error) {
	now := nowUTC()
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_notes (agent_name, session_id, content, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		agentName, sessionID, content, now, now)
	if err != nil {
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get note insert ID: %w", err)
	}
	return &AgentNote{
		ID: id, AgentName: agentName, Content: content,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

// UpdateAgentNote updates note content.
func (s *TaskStore) UpdateAgentNote(ctx context.Context, noteID int64, content string) error {
	now := nowUTC()
	_, err := s.db.ExecContext(ctx,
		"UPDATE agent_notes SET content = ?, updated_at = ? WHERE id = ?",
		content, now, noteID)
	return err
}

// DeleteAgentNote deletes a note by ID.
func (s *TaskStore) DeleteAgentNote(ctx context.Context, noteID int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM agent_notes WHERE id = ?", noteID)
	return err
}

// ── Agent Events ──────────────────────────────────────────────────────

// InsertAgentEvent inserts an event and auto-prunes to 500 per agent.
// CreatedAt is stamped with the current time unless the caller set it, which
// transcript-derived events do so they sort where they actually happened.
func (s *TaskStore) InsertAgentEvent(ctx context.Context, event *AgentEvent) (*AgentEvent, error) {
	if event.CreatedAt == "" {
		event.CreatedAt = nowUTC()
	}

	result, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_events (agent_name, session_id, event_type, tool_name, summary, detail_json, created_at, duration_ms, ref_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.AgentName, event.SessionID, event.EventType, event.ToolName,
		event.Summary, event.DetailJSON, event.CreatedAt, event.DurationMs, event.RefID)
	if err != nil {
		return nil, err
	}
	if id, err := result.LastInsertId(); err != nil {
		log.Printf("[store] get event insert ID: %v", err)
	} else {
		event.ID = id
	}

	// Auto-prune to 500 events per session (best-effort). Session-scoped
	// retention prevents activity from a sibling agent in the same worktree
	// from erasing an unresolved attention notification.
	pruneQuery := `DELETE FROM agent_events WHERE agent_name = ? AND id NOT IN
		 (SELECT id FROM agent_events WHERE agent_name = ? ORDER BY id DESC LIMIT 500)`
	pruneArgs := []any{event.AgentName, event.AgentName}
	if event.SessionID != nil && *event.SessionID != "" {
		pruneQuery = `DELETE FROM agent_events WHERE agent_name = ? AND session_id = ? AND id NOT IN
			 (SELECT id FROM agent_events WHERE agent_name = ? AND session_id = ? ORDER BY id DESC LIMIT 500)`
		pruneArgs = []any{event.AgentName, *event.SessionID, event.AgentName, *event.SessionID}
	}
	if _, err := s.db.ExecContext(ctx, pruneQuery, pruneArgs...); err != nil {
		log.Printf("[store] event prune failed for %s: %v", event.AgentName, err)
	}

	return event, nil
}

// AgentEventExists reports whether a session already has an event of this
// type for the given source id. Transcript-derived events use it to stay
// idempotent across re-reads and restarts.
func (s *TaskStore) AgentEventExists(ctx context.Context, sessionID, eventType, refID string) (bool, error) {
	var n int
	err := s.db.GetContext(ctx, &n,
		`SELECT COUNT(*) FROM agent_events WHERE session_id = ? AND event_type = ? AND ref_id = ?`,
		sessionID, eventType, refID)
	return n > 0, err
}

// ListAgentEvents returns recent events for an agent.
// If agentName is empty, events are not filtered by agent (useful for history queries by session).
func (s *TaskStore) ListAgentEvents(ctx context.Context, agentName string, limit int, sessionID *string) ([]AgentEvent, error) {
	var whereClauses []string
	var args []interface{}
	if agentName != "" {
		whereClauses = append(whereClauses, "agent_name = ?")
		args = append(args, agentName)
	}
	if sessionID != nil {
		whereClauses = append(whereClauses, "session_id = ?")
		args = append(args, *sessionID)
	}
	where := ""
	if len(whereClauses) > 0 {
		where = " WHERE " + strings.Join(whereClauses, " AND ")
	}
	args = append(args, limit)
	var events []AgentEvent
	err := s.db.SelectContext(ctx, &events,
		`SELECT id, agent_name, session_id, event_type, tool_name, summary, detail_json, created_at, duration_ms, ref_id
		 FROM agent_events`+where+` ORDER BY created_at DESC LIMIT ?`,
		args...)
	return events, err
}

// GetAgentEventCounts returns tool usage counts for an agent.
// If agentName is empty, counts are not filtered by agent.
func (s *TaskStore) GetAgentEventCounts(ctx context.Context, agentName string, sessionID *string) ([]ToolCount, error) {
	var whereClauses []string
	var args []interface{}
	if agentName != "" {
		whereClauses = append(whereClauses, "agent_name = ?")
		args = append(args, agentName)
	}
	if sessionID != nil {
		whereClauses = append(whereClauses, "session_id = ?")
		args = append(args, *sessionID)
	}
	whereClauses = append(whereClauses, "tool_name IS NOT NULL")
	where := " WHERE " + strings.Join(whereClauses, " AND ")
	var counts []ToolCount
	err := s.db.SelectContext(ctx, &counts,
		`SELECT tool_name, COUNT(*) as count FROM agent_events`+where+`
		 GROUP BY tool_name ORDER BY count DESC`,
		args...)
	return counts, err
}

// GetLatestEventTypes returns the latest (event_type, summary) per session (excluding status/goal/confidence).
func (s *TaskStore) GetLatestEventTypes(ctx context.Context, sessionIDs []string) (map[string][2]string, error) {
	if len(sessionIDs) == 0 {
		return map[string][2]string{}, nil
	}
	query, args, err := sqlx.In(
		`SELECT session_id, event_type, summary FROM agent_events
		 WHERE session_id IN (?) AND event_type NOT IN ('status', 'goal', 'confidence')
		 ORDER BY created_at DESC`,
		sessionIDs)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		SessionID string `db:"session_id"`
		EventType string `db:"event_type"`
		Summary   string `db:"summary"`
	}
	if err := s.db.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}
	result := make(map[string][2]string)
	for _, r := range rows {
		if _, ok := result[r.SessionID]; !ok {
			result[r.SessionID] = [2]string{r.EventType, r.Summary}
		}
	}
	return result, nil
}

// GetSessionStateEvents returns the ordered events that participate in live
// state derivation. Keeping this in the store makes the derivation restart
// safe: notification state is reconstructed from the event log rather than an
// in-memory latch. Events are returned oldest first for each session.
func (s *TaskStore) GetSessionStateEvents(ctx context.Context, sessionIDs []string) (map[string][]AgentEvent, error) {
	if len(sessionIDs) == 0 {
		return map[string][]AgentEvent{}, nil
	}
	query, args, err := sqlx.In(
		`SELECT id, agent_name, session_id, event_type, tool_name, summary, detail_json, created_at
		 FROM agent_events
		 WHERE session_id IN (?)
		   AND event_type IN ('notification', 'stop', 'prompt_submit', 'tool_use', 'session_reset')
		 ORDER BY session_id, created_at ASC, id ASC`,
		sessionIDs)
	if err != nil {
		return nil, err
	}
	var rows []AgentEvent
	if err := s.db.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}
	result := make(map[string][]AgentEvent, len(sessionIDs))
	for _, id := range sessionIDs {
		result[id] = []AgentEvent{}
	}
	for _, event := range rows {
		if event.SessionID != nil {
			result[*event.SessionID] = append(result[*event.SessionID], event)
		}
	}
	return result, nil
}

// GetLatestGoalEvent returns the newest goal event for a session, or nil.
func (s *TaskStore) GetLatestGoalEvent(ctx context.Context, sessionID string) (*AgentEvent, error) {
	var ev AgentEvent
	err := s.db.GetContext(ctx, &ev,
		`SELECT * FROM agent_events WHERE session_id = ? AND event_type = 'goal'
		 ORDER BY created_at DESC, id DESC LIMIT 1`, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ev, nil
}

// HasGoalEvent reports whether a session has ever had a goal with this text.
func (s *TaskStore) HasGoalEvent(ctx context.Context, sessionID, goal string) (bool, error) {
	var n int
	err := s.db.GetContext(ctx, &n,
		`SELECT COUNT(*) FROM agent_events WHERE session_id = ? AND event_type = 'goal' AND summary = ?`,
		sessionID, goal)
	return n > 0, err
}

// GetLatestGoals returns the latest goal summary per session.
func (s *TaskStore) GetLatestGoals(ctx context.Context, sessionIDs []string) (map[string]string, error) {
	if len(sessionIDs) == 0 {
		return map[string]string{}, nil
	}
	query, args, err := sqlx.In(
		`SELECT session_id, summary FROM agent_events
		 WHERE session_id IN (?) AND event_type = 'goal'
		 ORDER BY created_at DESC, id DESC`,
		sessionIDs)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		SessionID string `db:"session_id"`
		Summary   string `db:"summary"`
	}
	if err := s.db.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}
	result := make(map[string]string)
	for _, r := range rows {
		if _, ok := result[r.SessionID]; !ok {
			result[r.SessionID] = r.Summary
		}
	}
	return result, nil
}

// ClearAgentEvents deletes all events for an agent, optionally scoped by session.
func (s *TaskStore) ClearAgentEvents(ctx context.Context, agentName string, sessionID *string) error {
	filter, filterArgs := sessionFilter(sessionID)
	args := append([]interface{}{agentName}, filterArgs...)
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM agent_events WHERE agent_name = ?"+filter,
		args...)
	return err
}

// GetFileEdits returns a map of relative file paths to the agents that edited them,
// derived from Write/Edit tool_use events in agent_events.
// The workingDir is used to convert absolute paths in detail_json to repo-relative paths.
func (s *TaskStore) GetFileEdits(ctx context.Context, sessionID, workingDir string) (map[string][]FileAgent, error) {
	var rows []struct {
		AgentName    string `db:"agent_name"`
		FilePath     string `db:"file_path"`
		LastEditedAt string `db:"last_edited_at"`
	}
	err := s.db.SelectContext(ctx, &rows,
		`SELECT agent_name,
		        json_extract(detail_json, '$.file_path') as file_path,
		        MAX(created_at) as last_edited_at
		 FROM agent_events
		 WHERE session_id = ?
		   AND event_type = 'tool_use'
		   AND tool_name IN ('Write', 'Edit')
		   AND detail_json IS NOT NULL
		 GROUP BY agent_name, json_extract(detail_json, '$.file_path')`,
		sessionID)
	if err != nil {
		return nil, err
	}

	result := make(map[string][]FileAgent, len(rows))
	for _, r := range rows {
		if r.FilePath == "" {
			continue
		}
		// Convert absolute path to repo-relative
		relPath := r.FilePath
		if workingDir != "" && filepath.IsAbs(relPath) {
			if rel, err := filepath.Rel(workingDir, relPath); err == nil {
				relPath = rel
			}
		}
		result[relPath] = append(result[relPath], FileAgent{
			Name:         r.AgentName,
			LastEditedAt: r.LastEditedAt,
		})
	}
	return result, nil
}

// GetAllEditedFileCounts returns the count of distinct files each session has
// edited via Write/Edit tool events. Used for the tooltip file count display.
func (s *TaskStore) GetAllEditedFileCounts(ctx context.Context) (map[string]int, error) {
	var rows []struct {
		SessionID string `db:"session_id"`
		Count     int    `db:"cnt"`
	}
	err := s.db.SelectContext(ctx, &rows,
		`SELECT session_id, COUNT(DISTINCT json_extract(detail_json, '$.file_path')) AS cnt
		 FROM agent_events
		 WHERE event_type = 'tool_use'
		   AND tool_name IN ('Write', 'Edit')
		   AND detail_json IS NOT NULL
		   AND session_id IS NOT NULL
		 GROUP BY session_id`)
	if err != nil {
		return nil, err
	}
	result := make(map[string]int, len(rows))
	for _, r := range rows {
		result[r.SessionID] = r.Count
	}
	return result, nil
}

// GetEditedFilesBySession returns the set of absolute file paths each session has
// touched via Write/Edit tool events. Used by the git poller to build per-agent
// file counts instead of sharing repo-wide git diff results across all agents.
func (s *TaskStore) GetEditedFilesBySession(ctx context.Context, sessionIDs []string) (map[string]map[string]bool, error) {
	if len(sessionIDs) == 0 {
		return map[string]map[string]bool{}, nil
	}

	query, args, err := sqlx.In(
		`SELECT session_id,
		        json_extract(detail_json, '$.file_path') as file_path
		 FROM agent_events
		 WHERE session_id IN (?)
		   AND event_type = 'tool_use'
		   AND tool_name IN ('Write', 'Edit')
		   AND detail_json IS NOT NULL`,
		sessionIDs)
	if err != nil {
		return nil, err
	}
	query = s.db.Rebind(query)

	var rows []struct {
		SessionID string `db:"session_id"`
		FilePath  string `db:"file_path"`
	}
	if err := s.db.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}

	result := make(map[string]map[string]bool, len(sessionIDs))
	for _, r := range rows {
		if r.FilePath == "" {
			continue
		}
		if result[r.SessionID] == nil {
			result[r.SessionID] = make(map[string]bool)
		}
		result[r.SessionID][r.FilePath] = true
	}
	return result, nil
}

// ── History queries (by session_id only) ────────────────────────────────

// ListTasksBySession returns tasks for a historical session.
func (s *TaskStore) ListTasksBySession(ctx context.Context, sessionID string) ([]AgentTask, error) {
	var tasks []AgentTask
	err := s.db.SelectContext(ctx, &tasks,
		`SELECT * FROM agent_tasks WHERE session_id = ? ORDER BY sort_order`, sessionID)
	if err != nil {
		return nil, err
	}
	for i := range tasks {
		if err := s.hydrateAgentTask(ctx, &tasks[i]); err != nil {
			return nil, err
		}
	}
	return tasks, nil
}

// ListNotesBySession returns notes for a historical session.
func (s *TaskStore) ListNotesBySession(ctx context.Context, sessionID string) ([]AgentNote, error) {
	var notes []AgentNote
	err := s.db.SelectContext(ctx, &notes,
		`SELECT id, agent_name, content, created_at, updated_at
		 FROM agent_notes WHERE session_id = ? ORDER BY created_at DESC`, sessionID)
	return notes, err
}
