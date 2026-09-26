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
		m.Instructions = "In the shared checkout, coordinate file ownership, preserve teammates' changes, and commit only your task's work."
	case "worktrees":
		m.Instructions = "Use an isolated Git worktree and branch from the agreed base; reuse your task's checkout if available. Leave teammates' checkouts untouched and publish the exact commit."
	default:
		return fmt.Errorf("mode must be none, shared_checkout, or worktrees")
	}
	m.CustomInstructions = strings.TrimSpace(m.CustomInstructions)
	if len(m.CustomInstructions) > 4096 {
		return fmt.Errorf("custom instructions must be at most 4096 bytes")
	}
	if m.DependencyGuidance {
		m.Instructions = joinModeInstruction(m.Instructions, "Connect stages as separate dependent tasks with explicit prerequisites and named required outputs.")
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
	if err = json.Unmarshal([]byte(data), &m); err != nil {
		return m, err
	}
	// Regenerate current guidance; claim snapshots in task_workflows stay intact.
	err = m.normalize()
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
