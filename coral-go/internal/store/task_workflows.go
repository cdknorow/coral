package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cdknorow/coral/internal/board"
)

// AgentTaskProject is an internal scope, not a public team board. JSON avoids
// collisions between agent names and session identifiers containing separators.
func AgentTaskProject(name string, sid *string) string {
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode([]any{name, sid})
	return strings.TrimSuffix(data.String(), "\n")
}

// Keep the legacy checklist as a projection for dashboard, history and cost
// queries. All lifecycle mutations go through the board engine in this same DB.
// IDs, timestamps and existing session ownership are preserved on migration.
func (d *DB) initAgentTaskEngine(ctx context.Context) error {
	engine, err := board.UseDatabase(ctx, d.DB)
	if err != nil {
		return err
	}
	d.TaskEngine = engine
	_, err = d.ExecContext(ctx, `
 INSERT OR IGNORE INTO board_tasks(id,board_id,title,body,priority,status,created_by,assigned_to,created_at,claimed_at,completed_at,completion_message,session_id,cost_usd,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens)
 SELECT id,replace(replace(json_array(agent_name,session_id),char(8232),'\u2028'),char(8233),'\u2029'),title,body,COALESCE(priority,'medium'),
 CASE completed WHEN 1 THEN 'completed' WHEN 2 THEN 'in_progress' WHEN 3 THEN 'skipped' WHEN 4 THEN 'blocked' WHEN 5 THEN 'draft' WHEN 6 THEN 'review_pending' ELSE 'pending' END,
 agent_name,agent_name,created_at,started_at,completed_at,completion_message,session_id,cost_usd,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens FROM agent_tasks;
 INSERT INTO sqlite_sequence(name,seq) SELECT 'board_tasks',seq FROM sqlite_sequence
 WHERE name='agent_tasks' AND NOT EXISTS(SELECT 1 FROM sqlite_sequence WHERE name='board_tasks');
 UPDATE sqlite_sequence SET seq=MAX(seq,COALESCE((SELECT seq FROM sqlite_sequence WHERE name='agent_tasks'),0)) WHERE name='board_tasks';
 DROP TRIGGER IF EXISTS personal_task_insert;
 DROP TRIGGER IF EXISTS personal_task_update;
 CREATE TRIGGER personal_task_insert AFTER INSERT ON board_tasks BEGIN
 INSERT OR IGNORE INTO agent_tasks(id,agent_name,session_id,title,body,priority,completed,sort_order,created_at,updated_at)
 VALUES(NEW.id,json_extract(NEW.board_id,'$[0]'),json_extract(NEW.board_id,'$[1]'),NEW.title,NEW.body,NEW.priority,
 CASE NEW.status WHEN 'completed' THEN 1 WHEN 'in_progress' THEN 2 WHEN 'skipped' THEN 3 WHEN 'blocked' THEN 4 WHEN 'draft' THEN 5 WHEN 'review_pending' THEN 6 ELSE 0 END,
 (SELECT COALESCE(MAX(sort_order),-1)+1 FROM agent_tasks WHERE agent_name=json_extract(NEW.board_id,'$[0]') AND session_id IS json_extract(NEW.board_id,'$[1]')),NEW.created_at,NEW.created_at);
 END;
 CREATE TRIGGER IF NOT EXISTS personal_task_update AFTER UPDATE ON board_tasks BEGIN
 UPDATE agent_tasks SET title=NEW.title,body=NEW.body,priority=NEW.priority,
 completed=CASE NEW.status WHEN 'completed' THEN 1 WHEN 'in_progress' THEN 2 WHEN 'skipped' THEN 3 WHEN 'blocked' THEN 4 WHEN 'draft' THEN 5 WHEN 'review_pending' THEN 6 ELSE 0 END,
 started_at=NEW.claimed_at,completed_at=NEW.completed_at,completion_message=NEW.completion_message,
 cost_usd=COALESCE(NEW.cost_usd,0),input_tokens=COALESCE(NEW.input_tokens,0),output_tokens=COALESCE(NEW.output_tokens,0),cache_read_tokens=COALESCE(NEW.cache_read_tokens,0),cache_write_tokens=COALESCE(NEW.cache_write_tokens,0),
 updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE id=NEW.id;
 END;
 CREATE TRIGGER IF NOT EXISTS personal_task_delete AFTER DELETE ON board_tasks BEGIN
 DELETE FROM agent_tasks WHERE id=OLD.id;
 END;
 `)
	if err != nil {
		return err
	}
	// Persist default instructions for historical tasks too.
	data, _ := json.Marshal(board.TaskWorkflow{Instructions: board.DefaultTaskWorkflowInstructions})
	if _, err = d.ExecContext(ctx, "INSERT OR IGNORE INTO task_workflows(task_id,data) SELECT id,? FROM board_tasks", string(data)); err != nil {
		return err
	}
	return engine.RecoverTaskReadiness(ctx)
}

func (s *TaskStore) WorkflowTask(ctx context.Context, id int64) (*board.Task, error) {
	var project string
	if err := s.db.GetContext(ctx, &project, "SELECT board_id FROM board_tasks WHERE id=?", id); err != nil {
		return nil, err
	}
	return s.db.TaskEngine.GetTask(ctx, project, id)
}

func (s *TaskStore) hydrateAgentTask(ctx context.Context, t *AgentTask) error {
	b, err := s.WorkflowTask(ctx, t.ID)
	if err != nil {
		return err
	}
	t.Status = b.Status
	t.Workflow = b.Workflow
	t.BlockedBy = b.BlockedBy
	return nil
}

func (s *TaskStore) CreateAgentTaskWithWorkflow(ctx context.Context, name, title string, sid, display *string, body, priority string, opts *board.CreateTaskOpts) (*AgentTask, error) {
	project := AgentTaskProject(name, sid)
	if opts != nil {
		for i := range opts.BlockedBy {
			dep := &opts.BlockedBy[i]
			other, err := s.WorkflowTask(ctx, dep.TaskID)
			if err != nil || other.BoardID != project || dep.BoardID != "" && dep.BoardID != project {
				return nil, fmt.Errorf("dependency must belong to this agent session")
			}
			dep.BoardID = project
		}
	}
	task, err := s.db.TaskEngine.CreateTaskWithOpts(ctx, project, title, body, priority, name, opts, name)
	if err != nil {
		return nil, err
	}
	if display != nil {
		if _, err = s.db.ExecContext(ctx, "UPDATE agent_tasks SET display_name=? WHERE id=?", display, task.ID); err != nil {
			return nil, err
		}
	}
	// Session ID is known directly for personal tasks, without a board subscription.
	if _, err = s.db.ExecContext(ctx, "UPDATE board_tasks SET session_id=? WHERE id=?", sid, task.ID); err != nil {
		return nil, err
	}
	return s.GetAgentTask(ctx, task.ID)
}

func (s *TaskStore) FinishAgentTaskWithArtifacts(ctx context.Context, id int64, state int, message, outcome string, artifacts []board.TaskArtifact) error {
	task, err := s.WorkflowTask(ctx, id)
	if err != nil {
		return err
	}
	if state == AgentTaskCancelled {
		_, err = s.db.TaskEngine.CancelTask(ctx, task.BoardID, id, task.CreatedBy, &message)
	} else {
		_, err = s.db.TaskEngine.CompleteTaskWithArtifacts(ctx, task.BoardID, id, task.CreatedBy, &message, outcome, artifacts)
	}
	if err == nil {
		s.computeAgentTaskCost(ctx, id, nowUTC())
	}
	return err
}

func (s *TaskStore) UpdateAgentTaskWorkflow(ctx context.Context, id int64, updates board.TaskUpdate) (*AgentTask, error) {
	task, err := s.WorkflowTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if updates.AssignedTo != nil {
		return nil, fmt.Errorf("personal tasks cannot be reassigned to another agent")
	}
	if updates.BlockedBy != nil {
		for i := range *updates.BlockedBy {
			dep := &(*updates.BlockedBy)[i]
			other, err := s.WorkflowTask(ctx, dep.TaskID)
			if err != nil || other.BoardID != task.BoardID || dep.BoardID != "" && dep.BoardID != task.BoardID {
				return nil, fmt.Errorf("dependency must belong to this agent session")
			}
			dep.BoardID = task.BoardID
		}
	}
	_, _, err = s.db.TaskEngine.UpdateTask(ctx, task.BoardID, id, updates, 32)
	if err != nil {
		return nil, err
	}
	return s.GetAgentTask(ctx, id)
}
