package board

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jmoiron/sqlx"
)

var ErrPresetExists = errors.New("workflow preset already exists")
var presetIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

type WorkflowPreset struct {
	ID                  string `json:"id" db:"id"`
	Name                string `json:"name" db:"name"`
	Builtin             bool   `json:"builtin"`
	DefaultInstructions string `json:"default_instructions"`
	Instructions        string `json:"instructions" db:"instructions"`
	Overridden          bool   `json:"overridden"`
}

func builtinWorkflowPresets() []WorkflowPreset {
	return []WorkflowPreset{
		{ID: "none", Name: "None", Builtin: true},
		{ID: "shared_checkout", Name: "Shared checkout", Builtin: true, Instructions: "In the shared checkout, coordinate file ownership, preserve teammates' changes, and commit only your task's work."},
		{ID: "worktrees", Name: "Worktrees", Builtin: true, Instructions: "Use an isolated Git worktree and branch from the agreed base; reuse your task's checkout if available. Leave teammates' checkouts untouched and publish the exact commit."},
	}
}
func builtinWorkflowPreset(id string) (WorkflowPreset, bool) {
	for _, p := range builtinWorkflowPresets() {
		if p.ID == id {
			p.DefaultInstructions = p.Instructions
			return p, true
		}
	}
	return WorkflowPreset{}, false
}
func loadWorkflowPreset(ctx context.Context, db sqlx.QueryerContext, project, id string) (WorkflowPreset, error) {
	if id == "" {
		id = "none"
	}
	p, builtin := builtinWorkflowPreset(id)
	var row struct {
		ID           string `db:"id"`
		Name         string `db:"name"`
		Instructions string `db:"instructions"`
	}
	err := sqlx.GetContext(ctx, db, &row, "SELECT id,name,instructions FROM board_workflow_presets WHERE board_id=? AND id=?", project, id)
	if err == sql.ErrNoRows && builtin {
		return p, nil
	}
	if err != nil {
		if err == sql.ErrNoRows {
			return p, fmt.Errorf("unknown workflow preset %q", id)
		}
		return p, err
	}
	if !builtin {
		p.ID = row.ID
		p.Name = row.Name
	} else {
		p.Overridden = true
	}
	p.Instructions = row.Instructions
	return p, nil
}
func (s *Store) ListWorkflowPresets(ctx context.Context, project string) ([]WorkflowPreset, error) {
	result := []WorkflowPreset{}
	for _, p := range builtinWorkflowPresets() {
		v, err := loadWorkflowPreset(ctx, s.db, project, p.ID)
		if err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	var rows []struct {
		ID           string `db:"id"`
		Name         string `db:"name"`
		Instructions string `db:"instructions"`
	}
	if err := s.db.SelectContext(ctx, &rows, "SELECT id,name,instructions FROM board_workflow_presets WHERE board_id=? ORDER BY id", project); err != nil {
		return nil, err
	}
	for _, row := range rows {
		if _, builtin := builtinWorkflowPreset(row.ID); !builtin {
			result = append(result, WorkflowPreset{ID: row.ID, Name: row.Name, Instructions: row.Instructions})
		}
	}
	return result, nil
}
func (s *Store) SaveWorkflowPreset(ctx context.Context, project, id, name, instructions string, create bool) (WorkflowPreset, error) {
	if !presetIDPattern.MatchString(id) {
		return WorkflowPreset{}, fmt.Errorf("preset id must be a lowercase slug of 1–64 characters, starting with a letter")
	}
	name = strings.TrimSpace(name)
	instructions = strings.TrimSpace(instructions)
	builtin, isBuiltin := builtinWorkflowPreset(id)
	if isBuiltin {
		if create {
			return WorkflowPreset{}, ErrPresetExists
		}
		name = builtin.Name
	}
	if len(name) == 0 || len(name) > 80 || len(instructions) > 4096 {
		return WorkflowPreset{}, fmt.Errorf("name must be 1–80 bytes and instructions at most 4096 UTF-8 bytes")
	}
	if create {
		res, err := s.db.ExecContext(ctx, "INSERT INTO board_workflow_presets(board_id,id,name,instructions) VALUES(?,?,?,?) ON CONFLICT(board_id,id) DO NOTHING", project, id, name, instructions)
		if err != nil {
			return WorkflowPreset{}, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return WorkflowPreset{}, err
		}
		if n == 0 {
			return WorkflowPreset{}, ErrPresetExists
		}
	} else if isBuiltin {
		if _, err := s.db.ExecContext(ctx, "INSERT INTO board_workflow_presets(board_id,id,name,instructions) VALUES(?,?,?,?) ON CONFLICT(board_id,id) DO UPDATE SET instructions=excluded.instructions", project, id, name, instructions); err != nil {
			return WorkflowPreset{}, err
		}
	} else {
		res, err := s.db.ExecContext(ctx, "UPDATE board_workflow_presets SET name=?,instructions=? WHERE board_id=? AND id=?", name, instructions, project, id)
		if err != nil {
			return WorkflowPreset{}, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return WorkflowPreset{}, err
		}
		if n == 0 {
			return WorkflowPreset{}, fmt.Errorf("unknown workflow preset %q", id)
		}
	}
	return loadWorkflowPreset(ctx, s.db, project, id)
}
func (s *Store) ResetWorkflowPreset(ctx context.Context, project, id string) (WorkflowPreset, error) {
	p, ok := builtinWorkflowPreset(id)
	if !ok {
		return p, fmt.Errorf("only built-in presets can be reset")
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM board_workflow_presets WHERE board_id=? AND id=?", project, id)
	return p, err
}
