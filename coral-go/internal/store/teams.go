package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/jmoiron/sqlx"
)

// Team represents a persistent agent team.
type Team struct {
	ID         int64   `db:"id" json:"id"`
	Name       string  `db:"name" json:"name"`
	ConfigJSON string  `db:"config_json" json:"-"`
	Status     string  `db:"status" json:"status"`
	WorkingDir string  `db:"working_dir" json:"working_dir"`
	IsWorktree int     `db:"is_worktree" json:"is_worktree"`
	CreatedAt  string  `db:"created_at" json:"created_at"`
	UpdatedAt  string  `db:"updated_at" json:"updated_at"`
	StoppedAt  *string `db:"stopped_at" json:"stopped_at,omitempty"`

	// Computed fields
	Config  json.RawMessage `db:"-" json:"config,omitempty"`
	Members []TeamMember    `db:"-" json:"members,omitempty"`
}

// HydrateConfig populates Config from ConfigJSON.
func (t *Team) HydrateConfig() {
	if t.ConfigJSON != "" {
		t.Config = json.RawMessage(t.ConfigJSON)
	}
}

// TeamMember represents an agent slot in a team.
type TeamMember struct {
	ID              int64   `db:"id" json:"id"`
	TeamID          int64   `db:"team_id" json:"team_id"`
	AgentName       string  `db:"agent_name" json:"agent_name"`
	AgentConfigJSON string  `db:"agent_config_json" json:"-"`
	SessionID       *string `db:"session_id" json:"session_id,omitempty"`
	Status          string  `db:"status" json:"status"`
	CreatedAt       string  `db:"created_at" json:"created_at"`
	StoppedAt       *string `db:"stopped_at" json:"stopped_at,omitempty"`

	// Computed fields
	AgentConfig json.RawMessage `db:"-" json:"agent_config,omitempty"`
}

// HydrateConfig populates AgentConfig from AgentConfigJSON.
func (m *TeamMember) HydrateConfig() {
	if m.AgentConfigJSON != "" {
		m.AgentConfig = json.RawMessage(m.AgentConfigJSON)
	}
}

// TeamStore provides CRUD operations for teams and team members.
type TeamStore struct {
	db *DB
}

// NewTeamStore creates a new TeamStore.
func NewTeamStore(db *DB) *TeamStore {
	return &TeamStore{db: db}
}

// CreateTeam inserts a new team and returns it with ID populated.
func (s *TeamStore) CreateTeam(ctx context.Context, t *Team) (*Team, error) {
	now := NowUTC()
	t.CreatedAt = now
	t.UpdatedAt = now
	if t.Status == "" {
		t.Status = "running"
	}

	result, err := s.db.ExecContext(ctx,
		`INSERT INTO teams (name, config_json, status, working_dir, is_worktree, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.Name, t.ConfigJSON, t.Status, t.WorkingDir, t.IsWorktree, now, now)
	if err != nil {
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	t.ID = id
	return t, nil
}

// GetTeam returns a team by name with its member list.
func (s *TeamStore) GetTeam(ctx context.Context, name string) (*Team, error) {
	var t Team
	err := s.db.GetContext(ctx, &t,
		`SELECT id, name, config_json, status, working_dir, is_worktree, created_at, updated_at, stopped_at
		 FROM teams WHERE name = ?`, name)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.HydrateConfig()

	var members []TeamMember
	err = s.db.SelectContext(ctx, &members,
		`SELECT id, team_id, agent_name, agent_config_json, session_id, status, created_at, stopped_at
		 FROM team_members WHERE team_id = ? ORDER BY id`, t.ID)
	if err != nil {
		return nil, err
	}
	for i := range members {
		members[i].HydrateConfig()
	}
	t.Members = members
	return &t, nil
}

// GetTeamByID returns a team by ID without members.
func (s *TeamStore) GetTeamByID(ctx context.Context, teamID int64) (*Team, error) {
	var t Team
	err := s.db.GetContext(ctx, &t,
		`SELECT id, name, config_json, status, working_dir, is_worktree, created_at, updated_at, stopped_at
		 FROM teams WHERE id = ?`, teamID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.HydrateConfig()
	return &t, nil
}

// ListTeams returns all teams, optionally filtered by status.
func (s *TeamStore) ListTeams(ctx context.Context, statusFilter string) ([]Team, error) {
	var teams []Team
	if statusFilter != "" {
		err := s.db.SelectContext(ctx, &teams,
			`SELECT id, name, config_json, status, working_dir, is_worktree, created_at, updated_at, stopped_at
			 FROM teams WHERE status = ? ORDER BY updated_at DESC, id DESC`, statusFilter)
		return teams, err
	}
	err := s.db.SelectContext(ctx, &teams,
		`SELECT id, name, config_json, status, working_dir, is_worktree, created_at, updated_at, stopped_at
		 FROM teams ORDER BY updated_at DESC, id DESC`)
	for i := range teams {
		teams[i].HydrateConfig()
	}
	return teams, err
}

// UpdateTeamStatus sets the team status and updates timestamps.
func (s *TeamStore) UpdateTeamStatus(ctx context.Context, teamID int64, status string) error {
	now := NowUTC()
	if status == "stopped" {
		_, err := s.db.ExecContext(ctx,
			"UPDATE teams SET status = ?, updated_at = ?, stopped_at = ? WHERE id = ?",
			status, now, now, teamID)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		"UPDATE teams SET status = ?, updated_at = ?, stopped_at = NULL WHERE id = ?",
		status, now, teamID)
	return err
}

// DeleteTeam deletes a stopped team and its members (CASCADE).
func (s *TeamStore) DeleteTeam(ctx context.Context, teamID int64) error {
	result, err := s.db.ExecContext(ctx,
		"DELETE FROM teams WHERE id = ? AND status = 'stopped'", teamID)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// CreateTeamMember inserts a new team member slot.
func (s *TeamStore) CreateTeamMember(ctx context.Context, m *TeamMember) (*TeamMember, error) {
	now := NowUTC()
	m.CreatedAt = now
	if m.Status == "" {
		m.Status = "active"
	}

	result, err := s.db.ExecContext(ctx,
		`INSERT INTO team_members (team_id, agent_name, agent_config_json, session_id, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		m.TeamID, m.AgentName, m.AgentConfigJSON, m.SessionID, m.Status, now)
	if err != nil {
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	m.ID = id
	return m, nil
}

// UpdateMemberStatus updates a member's status and optionally its session ID.
func (s *TeamStore) UpdateMemberStatus(ctx context.Context, memberID int64, status string, sessionID *string) error {
	now := NowUTC()
	if status == "stopped" {
		_, err := s.db.ExecContext(ctx,
			"UPDATE team_members SET status = ?, session_id = ?, stopped_at = ? WHERE id = ?",
			status, sessionID, now, memberID)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		"UPDATE team_members SET status = ?, session_id = ?, stopped_at = NULL WHERE id = ?",
		status, sessionID, memberID)
	return err
}

// GetActiveMembers returns all active members for a team.
func (s *TeamStore) GetActiveMembers(ctx context.Context, teamID int64) ([]TeamMember, error) {
	var members []TeamMember
	err := s.db.SelectContext(ctx, &members,
		`SELECT id, team_id, agent_name, agent_config_json, session_id, status, created_at, stopped_at
		 FROM team_members WHERE team_id = ? AND status = 'active' ORDER BY id`, teamID)
	for i := range members {
		members[i].HydrateConfig()
	}
	return members, err
}

// GetSleepingMembers returns all sleeping members for a team.
func (s *TeamStore) GetSleepingMembers(ctx context.Context, teamID int64) ([]TeamMember, error) {
	var members []TeamMember
	err := s.db.SelectContext(ctx, &members,
		`SELECT id, team_id, agent_name, agent_config_json, session_id, status, created_at, stopped_at
		 FROM team_members WHERE team_id = ? AND status = 'sleeping' ORDER BY id`, teamID)
	for i := range members {
		members[i].HydrateConfig()
	}
	return members, err
}

// GetMembersForResurrect returns members whose stopped_at matches the team's stopped_at.
// These are the agents that were still active when the team was killed.
func (s *TeamStore) GetMembersForResurrect(ctx context.Context, teamID int64) ([]TeamMember, error) {
	var members []TeamMember
	err := s.db.SelectContext(ctx, &members,
		`SELECT m.id, m.team_id, m.agent_name, m.agent_config_json, m.session_id, m.status, m.created_at, m.stopped_at
		 FROM team_members m
		 JOIN teams t ON t.id = m.team_id
		 WHERE m.team_id = ? AND m.status = 'stopped' AND m.stopped_at = t.stopped_at
		 ORDER BY m.id`, teamID)
	for i := range members {
		members[i].HydrateConfig()
	}
	return members, err
}

// SetAllMembersStatus updates all members of a team with a given current status to a new status.
func (s *TeamStore) SetAllMembersStatus(ctx context.Context, teamID int64, fromStatus, toStatus string) error {
	if toStatus == "stopped" {
		now := NowUTC()
		_, err := s.db.ExecContext(ctx,
			"UPDATE team_members SET status = ?, stopped_at = ? WHERE team_id = ? AND status = ?",
			toStatus, now, teamID, fromStatus)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		"UPDATE team_members SET status = ?, stopped_at = NULL WHERE team_id = ? AND status = ?",
		toStatus, teamID, fromStatus)
	return err
}

// WakeTeamMembers marks a team's sleeping members active, except members whose
// live session is archived: team wake leaves those asleep.
func (s *TeamStore) WakeTeamMembers(ctx context.Context, teamID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE team_members SET status = 'active', stopped_at = NULL
		 WHERE team_id = ? AND status = 'sleeping'
		 AND (session_id IS NULL OR session_id NOT IN (SELECT session_id FROM live_sessions WHERE is_archived = 1 AND status = 'active'))`,
		teamID)
	return err
}

// StopTeam atomically stops a team and its active members with the same timestamp.
// This ensures GetMembersForResurrect can match stopped_at between members and team.
func (s *TeamStore) StopTeam(ctx context.Context, teamID int64) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		now := teamStopNow()
		// A member stopped on its own in the same clock tick as this stop would share its
		// timestamp and be resurrected with the team (#3920: on Windows' coarser clock it was).
		// The team's stop takes an instant after every earlier member stop.
		var latest sql.NullString
		if err := tx.GetContext(ctx, &latest,
			"SELECT MAX(stopped_at) FROM team_members WHERE team_id = ? AND stopped_at IS NOT NULL", teamID); err != nil {
			return err
		}
		if latest.Valid && latest.String >= now {
			if t, err := time.Parse(ISOFormat, latest.String); err == nil {
				now = t.Add(time.Microsecond).Format(ISOFormat)
			}
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE team_members SET status = 'stopped', stopped_at = ? WHERE team_id = ? AND status = 'active'",
			now, teamID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			"UPDATE teams SET status = 'stopped', updated_at = ?, stopped_at = ? WHERE id = ?",
			now, now, teamID)
		return err
	})
}

// GetMemberBySessionID finds a team member by its current session ID.
func (s *TeamStore) GetMemberBySessionID(ctx context.Context, sessionID string) (*TeamMember, error) {
	var m TeamMember
	err := s.db.GetContext(ctx, &m,
		`SELECT id, team_id, agent_name, agent_config_json, session_id, status, created_at, stopped_at
		 FROM team_members WHERE session_id = ?`, sessionID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m.HydrateConfig()
	return &m, nil
}

// teamStopNow is StopTeam's clock (a var for tests).
var teamStopNow = NowUTC
