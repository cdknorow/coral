package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/sessionstate"
	"log"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/subgentic/coral/internal/secrets"
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

// MarshalJSON is the event as every API sends it. What a permission_request wants to do (its detail's "wants", #6622) is display text for the live row alone, which the
// Coral Hub connector drops for a viewer who can only look: no events list, search or feed carries it, so a view-only viewer cannot read a command there.
func (e AgentEvent) MarshalJSON() ([]byte, error) {
	type plain AgentEvent
	p := plain(e)
	if e.EventType == "permission_request" && e.DetailJSON != nil && strings.Contains(*e.DetailJSON, `"wants"`) {
		var d map[string]any
		if json.Unmarshal([]byte(*e.DetailJSON), &d) == nil {
			delete(d, "wants")
			if b, err := json.Marshal(d); err == nil {
				s := string(b)
				p.DetailJSON = &s
			}
		}
	}
	return json.Marshal(p)
}

// StateEvent is the event as the session state derivation reads it: its type,
// summary, kind (from its detail), tool and time (#3481).
func (e AgentEvent) StateEvent() sessionstate.Event {
	ev := sessionstate.NewEvent(e.EventType, e.Summary, e.DetailJSON)
	if e.ToolName != nil {
		ev.ToolName = *e.ToolName
	}
	if e.EventType == "tool_use" && e.RefID != nil {
		ev.ToolUseID = *e.RefID // the call this PostToolUse is for
	}
	if t, err := time.Parse(time.RFC3339Nano, e.CreatedAt); err == nil {
		ev.At = t
	}
	return ev
}

// ToolCount holds a tool name and its usage count.
type ToolCount struct {
	ToolName string `db:"tool_name" json:"tool_name"`
	Count    int    `db:"count" json:"count"`
}

// TaskStore provides agent tasks, notes, and events operations.
type TaskStore struct {
	db *DB
	// eventKeep is how many events InsertAgentEvent keeps per session (0: agentEventKeep).
	// Atomic, so a test can set it while the server is serving.
	eventKeep atomic.Int64
}

// agentEventKeep is how many agent events are kept per session.
const agentEventKeep = 500

// SetEventKeep changes how many events this store keeps per session; tests use it to reach the
// pruning without inserting hundreds of events.
func (s *TaskStore) SetEventKeep(n int) { s.eventKeep.Store(int64(n)) }

// NewTaskStore creates a new TaskStore.
func NewTaskStore(db *DB) *TaskStore {
	return &TaskStore{db: db}
}

func (s *TaskStore) reader() *sqlx.DB {
	return s.db.Reader()
}

// ── Agent Tasks ────────────────────────────────────────────────────────

// ListAgentTasks returns tasks for an agent, optionally scoped by session.
func (s *TaskStore) ListAgentTasks(ctx context.Context, agentName string, sessionID *string) ([]AgentTask, error) {
	filter, filterArgs := sessionFilter(sessionID)
	args := append([]interface{}{agentName}, filterArgs...)
	var tasks []AgentTask
	err := s.reader().SelectContext(ctx, &tasks,
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

// ListAgentTasksForSessions is ListAgentTasks for many sessions in one query (#4154): the tasks
// of each session id, in sort order and hydrated as ListAgentTasks hydrates them. A caller that
// also filters by agent name (as ListAgentTasks does) checks AgentName itself. The board status
// asked once per agent, on every poll of every board.
func (s *TaskStore) ListAgentTasksForSessions(ctx context.Context, sessionIDs []string) (map[string][]AgentTask, error) {
	out := map[string][]AgentTask{}
	if len(sessionIDs) == 0 {
		return out, nil
	}
	ids, err := sessionIDsJSON(sessionIDs)
	if err != nil {
		return nil, err
	}
	var tasks []AgentTask
	if err := s.reader().SelectContext(ctx, &tasks,
		`SELECT id, agent_name, session_id, title, body, priority, completion_message, completed, sort_order, created_at, updated_at,
		        started_at, completed_at, cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, display_name
		 FROM agent_tasks WHERE session_id IN (SELECT value FROM json_each(?)) ORDER BY session_id, sort_order`, ids); err != nil {
		return nil, err
	}
	for i := range tasks {
		if err := s.hydrateAgentTask(ctx, &tasks[i]); err != nil {
			return nil, err
		}
		if tasks[i].SessionID != nil {
			out[*tasks[i].SessionID] = append(out[*tasks[i].SessionID], tasks[i])
		}
	}
	return out, nil
}

// CreateAgentTask creates a new task with auto-incrementing sort order.
func (s *TaskStore) CreateAgentTask(ctx context.Context, agentName, title string, sessionID *string, displayName *string) (*AgentTask, error) {
	return s.CreateAgentTaskWithWorkflow(ctx, agentName, title, sessionID, displayName, "", "medium", nil)
}

// GetAgentTask returns one agent task by ID, or nil.
func (s *TaskStore) GetAgentTask(ctx context.Context, taskID int64) (*AgentTask, error) {
	var t AgentTask
	err := s.reader().GetContext(ctx, &t, "SELECT * FROM agent_tasks WHERE id = ?", taskID)
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
	AgentTaskPending       = 0
	AgentTaskDone          = 1
	AgentTaskInProgress    = 2
	AgentTaskCancelled     = 3
	AgentTaskBlocked       = 4
	AgentTaskDraft         = 5
	AgentTaskReviewPending = 6
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
	return s.FinishAgentTaskWithArtifactsAndCandidate(ctx, taskID, state, message, "success", nil, "")
}

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
	err := s.reader().GetContext(ctx, &task,
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
	err = s.reader().GetContext(ctx, &usage,
		`SELECT COALESCE(SUM(input_tokens), 0) as input_tokens,
		        COALESCE(SUM(output_tokens), 0) as output_tokens,
		        COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens,
		        COALESCE(SUM(cache_write_tokens), 0) as cache_write_tokens,
		        COALESCE(SUM(cost_usd), 0) as cost_usd
		 FROM token_usage_all
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
	err := s.reader().SelectContext(ctx, &notes,
		`SELECT id, agent_name, content, created_at, updated_at
		 FROM agent_notes WHERE agent_name = ?`+filter+` ORDER BY created_at DESC, id DESC`,
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

// GetAgentNote returns a note, or nil when there is none with that id.
func (s *TaskStore) GetAgentNote(ctx context.Context, noteID int64) (*AgentNote, error) {
	var n AgentNote
	err := s.reader().GetContext(ctx, &n,
		"SELECT id, agent_name, session_id, content, created_at, updated_at FROM agent_notes WHERE id = ?", noteID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &n, nil
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

// redactAgentEvent redacts an event before it is stored (#3763, secrets audit #3762 finding 1):
// a tool call's summary and detail hold the whole command ("curl -H 'Authorization: Bearer
// ...'", "export TOKEN=...", "psql postgres://user:pw@..."), and sessions.db is what gets
// backed up and attached. Coral Hub's redactor runs once here, at the write, for every writer
// (the hooks' events, goals, thinking, auto-accept): the summary as text, the detail by its JSON
// structure (each value scanned with its key). What can't be checked is not stored (#3808).
func redactAgentEvent(event *AgentEvent) {
	red := eventRedactor()
	if s, err := red.Text([]byte(event.Summary)); err == nil {
		event.Summary = string(s)
	} else {
		event.Summary = uncheckedEvent
	}
	if event.DetailJSON != nil {
		if d, err := red.Document([]byte(*event.DetailJSON)); err == nil {
			ds := string(d)
			event.DetailJSON = &ds
		} else {
			event.DetailJSON = nil
		}
	}
}

// eventRedactor is the redactor events are stored through (a test replaces it).
var eventRedactor = secrets.Redactor

// uncheckedEvent is stored in place of an event summary the redactor could not check.
const uncheckedEvent = "[this event could not be checked for secrets, so it was not stored]"

// InsertAgentEvent inserts an event and auto-prunes to 500 per agent.
// CreatedAt is stamped with the current time unless the caller set it, which
// transcript-derived events do so they sort where they actually happened.
func (s *TaskStore) InsertAgentEvent(ctx context.Context, event *AgentEvent) (*AgentEvent, error) {
	if event.CreatedAt == "" {
		event.CreatedAt = nowUTC()
	}
	redactAgentEvent(event)

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
	keep := agentEventKeep
	if n := s.eventKeep.Load(); n > 0 {
		keep = int(n)
	}
	pruneQuery := `DELETE FROM agent_events WHERE agent_name = ? AND id NOT IN
		 (SELECT id FROM agent_events WHERE agent_name = ? ORDER BY id DESC LIMIT ?)`
	pruneArgs := []any{event.AgentName, event.AgentName, keep}
	if event.SessionID != nil && *event.SessionID != "" {
		pruneQuery = `DELETE FROM agent_events WHERE agent_name = ? AND session_id = ? AND id NOT IN
			 (SELECT id FROM agent_events WHERE agent_name = ? AND session_id = ? ORDER BY id DESC LIMIT ?)`
		pruneArgs = []any{event.AgentName, *event.SessionID, event.AgentName, *event.SessionID, keep}
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
	err := s.reader().GetContext(ctx, &n,
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
	err := s.reader().SelectContext(ctx, &events,
		`SELECT id, agent_name, session_id, event_type, tool_name, summary, detail_json, created_at, duration_ms, ref_id
		 FROM agent_events`+where+` ORDER BY created_at DESC, id DESC LIMIT ?`,
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
	err := s.reader().SelectContext(ctx, &counts,
		`SELECT tool_name, COUNT(*) as count FROM agent_events`+where+`
		 GROUP BY tool_name ORDER BY count DESC`,
		args...)
	return counts, err
}

// sessionIDsJSON encodes ids, without duplicates, for json_each. Unlike
// IN (...), joining json_each repeats rows for a repeated id.
func sessionIDsJSON(sessionIDs []string) (string, error) {
	seen := make(map[string]bool, len(sessionIDs))
	unique := make([]string, 0, len(sessionIDs))
	for _, id := range sessionIDs {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	b, err := json.Marshal(unique)
	return string(b), err
}

// SessionsWithAnyEvent is the sessions among sessionIDs that have recorded any event at all, a status or a goal too: the agent's CLI has reported, so it is no longer "starting" (#6745: a scheduled run
// whose events are all of the kinds GetLatestEventTypes leaves out never cleared it).
func (s *TaskStore) SessionsWithAnyEvent(ctx context.Context, sessionIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(sessionIDs) == 0 {
		return out, nil
	}
	ids, err := sessionIDsJSON(sessionIDs)
	if err != nil {
		return nil, err
	}
	var rows []string
	if err := s.reader().SelectContext(ctx, &rows,
		`SELECT s.value FROM json_each(?) s WHERE EXISTS (SELECT 1 FROM agent_events e WHERE e.session_id = s.value)`, ids); err != nil {
		return nil, err
	}
	for _, id := range rows {
		out[id] = true
	}
	return out, nil
}

// GetLatestEventTypes returns the latest (event_type, summary) per session (excluding status/goal/confidence).
func (s *TaskStore) GetLatestEventTypes(ctx context.Context, sessionIDs []string) (map[string][2]string, error) {
	if len(sessionIDs) == 0 {
		return map[string][2]string{}, nil
	}
	ids, err := sessionIDsJSON(sessionIDs)
	if err != nil {
		return nil, err
	}
	// One backward index seek per session (idx_agent_events_session_time)
	// instead of sorting every session's full history. Ties on created_at go
	// to the newest id.
	var rows []struct {
		SessionID string `db:"session_id"`
		EventType string `db:"event_type"`
		Summary   string `db:"summary"`
	}
	if err := s.reader().SelectContext(ctx, &rows,
		`SELECT session_id, event_type, summary FROM agent_events WHERE id IN (
		   SELECT (SELECT e.id FROM agent_events e
		           WHERE e.session_id = s.value AND e.event_type NOT IN ('status', 'goal', 'confidence')
		           ORDER BY e.created_at DESC, e.id DESC LIMIT 1)
		   FROM json_each(?) s)`, ids); err != nil {
		return nil, err
	}
	result := make(map[string][2]string, len(rows))
	for _, r := range rows {
		result[r.SessionID] = [2]string{r.EventType, r.Summary}
	}
	return result, nil
}

// GetSessionStateEvents returns the ordered events that participate in live
// state derivation. Keeping this in the store makes the derivation restart
// safe: notification state is reconstructed from the event log rather than an
// in-memory latch. Events are returned oldest first for each session.
//
// Only the suffix that decides the state is returned: the newest boundary
// (stop, prompt_submit, tool_use, session_reset, auto_accept or session_end) and
// the events after it. sessionstate.Derive resets on every boundary, so earlier
// events cannot change its result, and the suffix keeps the newest timestamp the
// transcript merge compares against. One exception (#3481): a tool_use does not
// close another tool's open request (Claude runs tools in parallel), so while a
// permission_request is open since the newest turn boundary (all but tool_use),
// the suffix starts at the earliest such request.
func (s *TaskStore) GetSessionStateEvents(ctx context.Context, sessionIDs []string) (map[string][]AgentEvent, error) {
	if len(sessionIDs) == 0 {
		return map[string][]AgentEvent{}, nil
	}
	ids, err := sessionIDsJSON(sessionIDs)
	if err != nil {
		return nil, err
	}
	var rows []AgentEvent
	if err := s.reader().SelectContext(ctx, &rows,
		`WITH turn AS (
		   SELECT s.value AS sid,
		          (SELECT x.id FROM agent_events x
		           WHERE x.session_id = s.value
		             AND x.event_type IN ('stop', 'prompt_submit', 'session_reset', 'auto_accept', 'session_end')
		           ORDER BY x.created_at DESC, x.id DESC LIMIT 1) AS tid
		   FROM json_each(?) s),
		 boundary AS (
		   SELECT turn.sid,
		          COALESCE(
		            (SELECT x.id FROM agent_events x
		             WHERE x.session_id = turn.sid AND x.event_type = 'permission_request'
		               AND (turn.tid IS NULL OR (x.created_at, x.id) > ((SELECT t.created_at FROM agent_events t WHERE t.id = turn.tid), turn.tid))
		             ORDER BY x.created_at, x.id LIMIT 1),
		            (SELECT x.id FROM agent_events x
		             WHERE x.session_id = turn.sid
		               AND x.event_type IN ('stop', 'prompt_submit', 'tool_use', 'session_reset', 'auto_accept', 'session_end')
		             ORDER BY x.created_at DESC, x.id DESC LIMIT 1)) AS bid
		   FROM turn)
		 SELECT e.id, e.agent_name, e.session_id, e.event_type, e.tool_name, e.summary, e.detail_json, e.created_at, e.ref_id
		 FROM boundary
		 JOIN agent_events b ON b.id = boundary.bid
		 JOIN agent_events e ON e.session_id = boundary.sid
		 WHERE e.event_type IN ('notification', 'stop', 'prompt_submit', 'tool_use', 'session_reset', 'auto_accept', 'permission_request', 'request_resolved', 'session_end')
		   AND (e.created_at, e.id) >= (b.created_at, b.id)
		 UNION ALL
		 SELECT e.id, e.agent_name, e.session_id, e.event_type, e.tool_name, e.summary, e.detail_json, e.created_at, e.ref_id
		 FROM boundary
		 JOIN agent_events e ON e.session_id = boundary.sid
		 WHERE boundary.bid IS NULL AND e.event_type IN ('notification', 'permission_request', 'request_resolved')
		 ORDER BY 3, 8, 1`, ids); err != nil {
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

// TurnStartTime returns when the session's current turn began: its EARLIEST prompt_submit
// since the newest turn boundary (stop, which includes a StopFailure, session_end or
// session_reset). ok is false when there is none. Claude records a prompt_submit for
// input typed DURING a turn too (it is queued), so the newest one is not the turn's start
// (#3510, QA's live repro on Claude 2.1.300).
func (s *TaskStore) TurnStartTime(ctx context.Context, sessionID string) (time.Time, bool, error) {
	var at sql.NullString
	err := s.reader().GetContext(ctx, &at,
		`SELECT MIN(e.created_at) FROM agent_events e
		 WHERE e.session_id = ? AND e.event_type = 'prompt_submit'
		   AND e.created_at > COALESCE((SELECT MAX(b.created_at) FROM agent_events b
		       WHERE b.session_id = e.session_id AND b.event_type IN ('stop', 'session_end', 'session_reset')), '')`, sessionID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !at.Valid) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	t, err := time.Parse(ISOFormat, at.String)
	if err != nil {
		return time.Time{}, false, err
	}
	return t, true, nil
}

// GetLatestGoalEvent returns the newest goal event for a session, or nil.
func (s *TaskStore) GetLatestGoalEvent(ctx context.Context, sessionID string) (*AgentEvent, error) {
	var ev AgentEvent
	err := s.reader().GetContext(ctx, &ev,
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

// LatestStopFailure returns the error text of a session's newest turn end when that turn
// ended in an API error (the StopFailure hook: event "stop" with detail failed). ok is false when
// the newest turn end was a normal one, or there is none (#3613). The text is the CLI's own and
// stays on the server: callers map it to a fixed label.
func (s *TaskStore) LatestStopFailure(ctx context.Context, sessionID string) (text string, ok bool, err error) {
	var detail sql.NullString
	err = s.reader().GetContext(ctx, &detail,
		`SELECT detail_json FROM agent_events WHERE session_id = ? AND event_type = 'stop'
		 ORDER BY created_at DESC, id DESC LIMIT 1`, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil || !detail.Valid {
		return "", false, err
	}
	var d struct {
		Failed bool   `json:"failed"`
		Error  string `json:"error"`
	}
	if json.Unmarshal([]byte(detail.String), &d) != nil || !d.Failed {
		return "", false, nil
	}
	return d.Error, true, nil
}

// The states of a session's current turn, as RunTurn reads them from its stored events (#7090).
const (
	TurnNone    = ""        // no prompt of this session was submitted yet (or it was reset since): nothing to finish
	TurnWorking = "working" // a prompt was submitted and its turn has not ended: a stop is awaited, or the agent went on after one
	TurnDone    = "done"    // the turn ended normally (a stop that did not fail)
	TurnFailed  = "failed"  // the turn ended in an error (StopFailure: a stop with detail failed)
)

// RunTurn is how the current turn of a session stands, and, once it has ended, the time of the stop that ended it.
type RunTurn struct {
	State string
	At    string
}

// SessionTurn says how a session's current turn stands, from two bounded reads of its stored events (#7090): the newest prompt_submit or session_reset, which opens or resets the turn, and the newest tool call,
// request or stop after it. A prompt starts a turn; the newest of what the agent did since is the state: a stop ends it (normally, or in an error when its detail says failed), a tool call or a request is
// the turn going on (also when it came after an earlier stop: the agent went on). A reset, or no prompt at all, is none: a stop with no prompt before it (a startup, a stop of something else) ends nothing.
// Only rows after the prompt are read, and only the newest of them: a run with thousands of tool calls costs the same as one with none. Events are kept to the newest few hundred per session: a turn whose
// prompt was pruned reads as none, never as ended. An unreadable store is an error, never a state.
func (s *TaskStore) SessionTurn(ctx context.Context, sessionID string) (RunTurn, error) {
	var open struct {
		ID   int64  `db:"id"`
		Type string `db:"event_type"`
	}
	err := s.reader().GetContext(ctx, &open,
		`SELECT id, event_type FROM agent_events WHERE session_id = ? AND event_type IN ('prompt_submit', 'session_reset') ORDER BY id DESC LIMIT 1`, sessionID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && open.Type != "prompt_submit") {
		return RunTurn{}, nil
	}
	if err != nil {
		return RunTurn{}, err
	}
	var last struct {
		Type   string         `db:"event_type"`
		Detail sql.NullString `db:"detail_json"`
		At     string         `db:"created_at"`
	}
	err = s.reader().GetContext(ctx, &last,
		`SELECT event_type, detail_json, created_at FROM agent_events WHERE session_id = ? AND id > ? AND event_type IN ('stop', 'tool_use', 'permission_request') ORDER BY id DESC LIMIT 1`, sessionID, open.ID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && last.Type != "stop") {
		return RunTurn{State: TurnWorking}, nil
	}
	if err != nil {
		return RunTurn{}, err
	}
	turn := RunTurn{State: TurnDone, At: last.At}
	var d struct {
		Failed bool `json:"failed"`
	}
	if last.Detail.Valid && json.Unmarshal([]byte(last.Detail.String), &d) == nil && d.Failed {
		turn.State = TurnFailed
	}
	return turn, nil
}

// GetLatestGoals returns the latest goal summary per session.
func (s *TaskStore) GetLatestGoals(ctx context.Context, sessionIDs []string) (map[string]string, error) {
	if len(sessionIDs) == 0 {
		return map[string]string{}, nil
	}
	ids, err := sessionIDsJSON(sessionIDs)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		SessionID string `db:"session_id"`
		Summary   string `db:"summary"`
	}
	if err := s.reader().SelectContext(ctx, &rows,
		`SELECT session_id, summary FROM agent_events WHERE id IN (
		   SELECT (SELECT e.id FROM agent_events e
		           WHERE e.session_id = s.value AND e.event_type = 'goal'
		           ORDER BY e.created_at DESC, e.id DESC LIMIT 1)
		   FROM json_each(?) s)`, ids); err != nil {
		return nil, err
	}
	result := make(map[string]string, len(rows))
	for _, r := range rows {
		result[r.SessionID] = r.Summary
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
	err := s.reader().SelectContext(ctx, &rows,
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
	err := s.reader().SelectContext(ctx, &rows,
		`SELECT session_id, COUNT(*) AS cnt FROM agent_event_files GROUP BY session_id`) // edited_files.go
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
		 FROM agent_events INDEXED BY idx_agent_events_edited_files
		 WHERE session_id IN (?)
		   AND event_type = 'tool_use'
		   AND tool_name IN ('Write', 'Edit')
		   AND detail_json IS NOT NULL AND json_valid(detail_json)`,
		sessionIDs)
	if err != nil {
		return nil, err
	}
	query = s.db.Rebind(query)

	var rows []struct {
		SessionID string `db:"session_id"`
		FilePath  string `db:"file_path"`
	}
	if err := s.reader().SelectContext(ctx, &rows, query, args...); err != nil {
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
	err := s.reader().SelectContext(ctx, &tasks,
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
	err := s.reader().SelectContext(ctx, &notes,
		`SELECT id, agent_name, content, created_at, updated_at
		 FROM agent_notes WHERE session_id = ? ORDER BY created_at DESC, id DESC`, sessionID)
	return notes, err
}
