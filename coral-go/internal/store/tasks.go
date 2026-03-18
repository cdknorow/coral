package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"
)

// AgentTask represents a checklist item for an agent.
type AgentTask struct {
	ID        int64   `db:"id" json:"id"`
	AgentName string  `db:"agent_name" json:"agent_name"`
	SessionID *string `db:"session_id" json:"session_id,omitempty"`
	Title     string  `db:"title" json:"title"`
	Completed int     `db:"completed" json:"completed"`
	SortOrder int     `db:"sort_order" json:"sort_order"`
	CreatedAt string  `db:"created_at" json:"created_at"`
	UpdatedAt string  `db:"updated_at" json:"updated_at"`
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
	var tasks []AgentTask
	if sessionID != nil {
		err := s.db.SelectContext(ctx, &tasks,
			`SELECT id, agent_name, title, completed, sort_order, created_at, updated_at
			 FROM agent_tasks WHERE agent_name = ? AND session_id = ? ORDER BY sort_order`,
			agentName, *sessionID)
		return tasks, err
	}
	err := s.db.SelectContext(ctx, &tasks,
		`SELECT id, agent_name, title, completed, sort_order, created_at, updated_at
		 FROM agent_tasks WHERE agent_name = ? ORDER BY sort_order`,
		agentName)
	return tasks, err
}

// CreateAgentTask creates a new task with auto-incrementing sort order.
func (s *TaskStore) CreateAgentTask(ctx context.Context, agentName, title string, sessionID *string) (*AgentTask, error) {
	now := nowUTC()

	// Get next sort order
	var nextOrder int
	var err error
	if sessionID != nil {
		err = s.db.GetContext(ctx, &nextOrder,
			"SELECT COALESCE(MAX(sort_order), -1) + 1 FROM agent_tasks WHERE agent_name = ? AND session_id = ?",
			agentName, *sessionID)
	} else {
		err = s.db.GetContext(ctx, &nextOrder,
			"SELECT COALESCE(MAX(sort_order), -1) + 1 FROM agent_tasks WHERE agent_name = ?",
			agentName)
	}
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}

	result, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_tasks (agent_name, session_id, title, sort_order, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		agentName, sessionID, title, nextOrder, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()
	return &AgentTask{
		ID: id, AgentName: agentName, Title: title,
		Completed: 0, SortOrder: nextOrder,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

// CreateAgentTaskIfNotExists creates a task only if one with the same title doesn't exist.
func (s *TaskStore) CreateAgentTaskIfNotExists(ctx context.Context, agentName, title string, sessionID *string) (*AgentTask, error) {
	var existing AgentTask
	var err error
	if sessionID != nil {
		err = s.db.GetContext(ctx, &existing,
			`SELECT id, agent_name, title, completed, sort_order, created_at, updated_at
			 FROM agent_tasks WHERE agent_name = ? AND title = ? AND session_id = ?`,
			agentName, title, *sessionID)
	} else {
		err = s.db.GetContext(ctx, &existing,
			`SELECT id, agent_name, title, completed, sort_order, created_at, updated_at
			 FROM agent_tasks WHERE agent_name = ? AND title = ?`,
			agentName, title)
	}
	if err == nil {
		return &existing, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	return s.CreateAgentTask(ctx, agentName, title, sessionID)
}

// UpdateAgentTask updates task fields (title, completed, sort_order).
func (s *TaskStore) UpdateAgentTask(ctx context.Context, taskID int64, title *string, completed *int, sortOrder *int) error {
	now := nowUTC()
	sets := []string{"updated_at = ?"}
	args := []interface{}{now}
	if title != nil {
		sets = append(sets, "title = ?")
		args = append(args, *title)
	}
	if completed != nil {
		sets = append(sets, "completed = ?")
		args = append(args, *completed)
	}
	if sortOrder != nil {
		sets = append(sets, "sort_order = ?")
		args = append(args, *sortOrder)
	}
	args = append(args, taskID)
	_, err := s.db.ExecContext(ctx,
		fmt.Sprintf("UPDATE agent_tasks SET %s WHERE id = ?", strings.Join(sets, ", ")),
		args...)
	return err
}

// CompleteAgentTaskByTitle marks a task as completed by title match.
func (s *TaskStore) CompleteAgentTaskByTitle(ctx context.Context, agentName, title string, sessionID *string) error {
	now := nowUTC()
	if sessionID != nil {
		_, err := s.db.ExecContext(ctx,
			`UPDATE agent_tasks SET completed = 1, updated_at = ?
			 WHERE agent_name = ? AND title = ? AND session_id = ? AND completed = 0`,
			now, agentName, title, *sessionID)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE agent_tasks SET completed = 1, updated_at = ?
		 WHERE agent_name = ? AND title = ? AND completed = 0`,
		now, agentName, title)
	return err
}

// DeleteAgentTask deletes a task by ID.
func (s *TaskStore) DeleteAgentTask(ctx context.Context, taskID int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM agent_tasks WHERE id = ?", taskID)
	return err
}

// ReorderAgentTasks sets sort_order based on the provided ID order.
func (s *TaskStore) ReorderAgentTasks(ctx context.Context, agentName string, taskIDs []int64) error {
	now := nowUTC()
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for idx, tid := range taskIDs {
		tx.ExecContext(ctx,
			"UPDATE agent_tasks SET sort_order = ?, updated_at = ? WHERE id = ? AND agent_name = ?",
			idx, now, tid, agentName)
	}
	return tx.Commit()
}

// ── Agent Notes ────────────────────────────────────────────────────────

// ListAgentNotes returns notes for an agent, optionally scoped by session.
func (s *TaskStore) ListAgentNotes(ctx context.Context, agentName string, sessionID *string) ([]AgentNote, error) {
	var notes []AgentNote
	if sessionID != nil {
		err := s.db.SelectContext(ctx, &notes,
			`SELECT id, agent_name, content, created_at, updated_at
			 FROM agent_notes WHERE agent_name = ? AND session_id = ? ORDER BY created_at DESC`,
			agentName, *sessionID)
		return notes, err
	}
	err := s.db.SelectContext(ctx, &notes,
		`SELECT id, agent_name, content, created_at, updated_at
		 FROM agent_notes WHERE agent_name = ? ORDER BY created_at DESC`,
		agentName)
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
	id, _ := result.LastInsertId()
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
func (s *TaskStore) InsertAgentEvent(ctx context.Context, event *AgentEvent) (*AgentEvent, error) {
	now := nowUTC()
	event.CreatedAt = now

	result, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_events (agent_name, session_id, event_type, tool_name, summary, detail_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		event.AgentName, event.SessionID, event.EventType, event.ToolName,
		event.Summary, event.DetailJSON, now)
	if err != nil {
		return nil, err
	}
	event.ID, _ = result.LastInsertId()

	// Auto-prune to 500 events per agent
	s.db.ExecContext(ctx,
		`DELETE FROM agent_events WHERE agent_name = ? AND id NOT IN
		 (SELECT id FROM agent_events WHERE agent_name = ? ORDER BY id DESC LIMIT 500)`,
		event.AgentName, event.AgentName)

	return event, nil
}

// ListAgentEvents returns recent events for an agent.
func (s *TaskStore) ListAgentEvents(ctx context.Context, agentName string, limit int, sessionID *string) ([]AgentEvent, error) {
	var events []AgentEvent
	if sessionID != nil {
		err := s.db.SelectContext(ctx, &events,
			`SELECT id, agent_name, session_id, event_type, tool_name, summary, detail_json, created_at
			 FROM agent_events WHERE agent_name = ? AND session_id = ? ORDER BY created_at DESC LIMIT ?`,
			agentName, *sessionID, limit)
		return events, err
	}
	err := s.db.SelectContext(ctx, &events,
		`SELECT id, agent_name, session_id, event_type, tool_name, summary, detail_json, created_at
		 FROM agent_events WHERE agent_name = ? ORDER BY created_at DESC LIMIT ?`,
		agentName, limit)
	return events, err
}

// GetAgentEventCounts returns tool usage counts for an agent.
func (s *TaskStore) GetAgentEventCounts(ctx context.Context, agentName string, sessionID *string) ([]ToolCount, error) {
	var counts []ToolCount
	if sessionID != nil {
		err := s.db.SelectContext(ctx, &counts,
			`SELECT tool_name, COUNT(*) as count FROM agent_events
			 WHERE agent_name = ? AND session_id = ? AND tool_name IS NOT NULL
			 GROUP BY tool_name ORDER BY count DESC`,
			agentName, *sessionID)
		return counts, err
	}
	err := s.db.SelectContext(ctx, &counts,
		`SELECT tool_name, COUNT(*) as count FROM agent_events
		 WHERE agent_name = ? AND tool_name IS NOT NULL
		 GROUP BY tool_name ORDER BY count DESC`,
		agentName)
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

// GetLatestGoals returns the latest goal summary per session.
func (s *TaskStore) GetLatestGoals(ctx context.Context, sessionIDs []string) (map[string]string, error) {
	if len(sessionIDs) == 0 {
		return map[string]string{}, nil
	}
	query, args, err := sqlx.In(
		`SELECT session_id, summary FROM agent_events
		 WHERE session_id IN (?) AND event_type = 'goal'
		 ORDER BY created_at DESC`,
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
	if sessionID != nil {
		_, err := s.db.ExecContext(ctx,
			"DELETE FROM agent_events WHERE agent_name = ? AND session_id = ?",
			agentName, *sessionID)
		return err
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM agent_events WHERE agent_name = ?", agentName)
	return err
}

// GetLastKnownStatusSummary returns the most recent status and goal per agent/session.
func (s *TaskStore) GetLastKnownStatusSummary(ctx context.Context) (map[string]map[string]*string, error) {
	type row struct {
		SessionID *string `db:"session_id"`
		AgentName string  `db:"agent_name"`
		Summary   string  `db:"summary"`
	}
	var statusRows []row
	s.db.SelectContext(ctx, &statusRows,
		`SELECT session_id, agent_name, summary FROM agent_events
		 WHERE event_type = 'status' AND id IN
		 (SELECT MAX(id) FROM agent_events WHERE event_type = 'status'
		  GROUP BY COALESCE(session_id, agent_name))`)

	var goalRows []row
	s.db.SelectContext(ctx, &goalRows,
		`SELECT session_id, agent_name, summary FROM agent_events
		 WHERE event_type = 'goal' AND id IN
		 (SELECT MAX(id) FROM agent_events WHERE event_type = 'goal'
		  GROUP BY COALESCE(session_id, agent_name))`)

	result := make(map[string]map[string]*string)
	for _, r := range statusRows {
		key := r.AgentName
		if r.SessionID != nil {
			key = *r.SessionID
		}
		if result[key] == nil {
			result[key] = map[string]*string{"status": nil, "summary": nil}
		}
		s := r.Summary
		result[key]["status"] = &s
	}
	for _, r := range goalRows {
		key := r.AgentName
		if r.SessionID != nil {
			key = *r.SessionID
		}
		if result[key] == nil {
			result[key] = map[string]*string{"status": nil, "summary": nil}
		}
		s := r.Summary
		result[key]["summary"] = &s
	}
	return result, nil
}

// ── History queries (by session_id only) ────────────────────────────────

// ListTasksBySession returns tasks for a historical session.
func (s *TaskStore) ListTasksBySession(ctx context.Context, sessionID string) ([]AgentTask, error) {
	var tasks []AgentTask
	err := s.db.SelectContext(ctx, &tasks,
		`SELECT id, agent_name, title, completed, sort_order, created_at, updated_at
		 FROM agent_tasks WHERE session_id = ? ORDER BY sort_order`, sessionID)
	return tasks, err
}

// ListNotesBySession returns notes for a historical session.
func (s *TaskStore) ListNotesBySession(ctx context.Context, sessionID string) ([]AgentNote, error) {
	var notes []AgentNote
	err := s.db.SelectContext(ctx, &notes,
		`SELECT id, agent_name, content, created_at, updated_at
		 FROM agent_notes WHERE session_id = ? ORDER BY created_at DESC`, sessionID)
	return notes, err
}

// ListEventsBySession returns events for a historical session.
func (s *TaskStore) ListEventsBySession(ctx context.Context, sessionID string, limit int) ([]AgentEvent, error) {
	var events []AgentEvent
	err := s.db.SelectContext(ctx, &events,
		`SELECT id, agent_name, session_id, event_type, tool_name, summary, detail_json, created_at
		 FROM agent_events WHERE session_id = ? ORDER BY created_at DESC LIMIT ?`, sessionID, limit)
	return events, err
}
