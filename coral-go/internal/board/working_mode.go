package board

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/jmoiron/sqlx"
	"strings"
)

// WorkingMode is the team convention, frozen into a task on its first claim.
type WorkingMode struct {
	Mode               string `json:"mode"`
	DependencyGuidance bool   `json:"dependency_guidance"`
	CustomInstructions string `json:"custom_instructions"`
	Instructions       string `json:"instructions"`
}

func (m *WorkingMode) normalize() error {
	if m.Mode == "" {
		m.Mode = "none"
	}
	switch m.Mode {
	case "none":
		m.Instructions = ""
	case "shared_checkout":
		m.Instructions = "Work in the team's shared checkout. Coordinate file ownership before editing; preserve teammates' uncommitted changes. Commit only your task's changes and report the exact revision."
	case "worktrees":
		m.Instructions = "Work in an isolated Git worktree and branch for this task; reuse an existing isolated task checkout when appropriate. Start from the agreed base revision. Do not change another agent's checkout. Publish the exact commit and hand-off artifacts; downstream agents must consume that revision."
	default:
		return fmt.Errorf("mode must be none, shared_checkout, or worktrees")
	}
	m.CustomInstructions = strings.TrimSpace(m.CustomInstructions)
	if len(m.CustomInstructions) > 4096 {
		return fmt.Errorf("custom instructions must be at most 4096 bytes")
	}
	if m.DependencyGuidance {
		m.Instructions = joinModeInstruction(m.Instructions, "Use separate dependent tasks for implementation, testing, and release. Declare prerequisites and named required outputs. Consume the upstream revision and artifacts returned on claim. Publish evidence on completion; wait for readiness notifications rather than polling blocked tasks.")
	}
	m.Instructions = joinModeInstruction(m.Instructions, m.CustomInstructions)
	return nil
}
func joinModeInstruction(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "\n" + b
}

func loadWorkingMode(ctx context.Context, db sqlx.QueryerContext, project string) (WorkingMode, error) {
	m := WorkingMode{Mode: "none"}
	var data string
	err := sqlx.GetContext(ctx, db, &data, "SELECT data FROM board_working_modes WHERE board_id = ?", project)
	if err == sql.ErrNoRows {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	err = json.Unmarshal([]byte(data), &m)
	return m, err
}
func (s *Store) GetWorkingMode(ctx context.Context, project string) (WorkingMode, error) {
	return loadWorkingMode(ctx, s.db, project)
}
func (s *Store) SetWorkingMode(ctx context.Context, project string, m WorkingMode) (WorkingMode, error) {
	if err := m.normalize(); err != nil {
		return m, err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return m, err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO board_working_modes(board_id,data) VALUES(?,?) ON CONFLICT(board_id) DO UPDATE SET data=excluded.data", project, string(data))
	return m, err
}
