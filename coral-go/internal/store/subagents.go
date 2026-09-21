package store

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// Subagent is one subagent launched by a main agent, with its cumulative
// token spend. SessionID is a foreign key to live_sessions: every subagent
// belongs to exactly one main agent.
type Subagent struct {
	ID               int64   `db:"id" json:"id"`
	SessionID        string  `db:"session_id" json:"session_id"`
	SubagentID       string  `db:"subagent_id" json:"subagent_id"`
	SubagentType     *string `db:"subagent_type" json:"subagent_type,omitempty"`
	Description      *string `db:"description" json:"description,omitempty"`
	ToolUseID        *string `db:"tool_use_id" json:"tool_use_id,omitempty"`
	Model            *string `db:"model" json:"model,omitempty"`
	SpawnDepth       int     `db:"spawn_depth" json:"spawn_depth"`
	APICalls         int     `db:"api_calls" json:"api_calls"`
	InputTokens      int     `db:"input_tokens" json:"input_tokens"`
	OutputTokens     int     `db:"output_tokens" json:"output_tokens"`
	CacheReadTokens  int     `db:"cache_read_tokens" json:"cache_read_tokens"`
	CacheWriteTokens int     `db:"cache_write_tokens" json:"cache_write_tokens"`
	CostUSD          float64 `db:"cost_usd" json:"cost_usd"`
	StartedAt        *string `db:"started_at" json:"started_at,omitempty"`
	LastActivityAt   *string `db:"last_activity_at" json:"last_activity_at,omitempty"`
	CreatedAt        string  `db:"created_at" json:"created_at"`
	UpdatedAt        string  `db:"updated_at" json:"updated_at"`
}

// SubagentSpend is the summed spend of all of a main agent's subagents.
type SubagentSpend struct {
	SessionID        string  `db:"session_id" json:"session_id"`
	Subagents        int     `db:"subagents" json:"subagents"`
	APICalls         int     `db:"api_calls" json:"api_calls"`
	InputTokens      int     `db:"input_tokens" json:"input_tokens"`
	OutputTokens     int     `db:"output_tokens" json:"output_tokens"`
	CacheReadTokens  int     `db:"cache_read_tokens" json:"cache_read_tokens"`
	CacheWriteTokens int     `db:"cache_write_tokens" json:"cache_write_tokens"`
	CostUSD          float64 `db:"cost_usd" json:"cost_usd"`
}

// SubagentStore provides subagent operations.
type SubagentStore struct {
	db *DB
}

// NewSubagentStore creates a new SubagentStore.
func NewSubagentStore(db *DB) *SubagentStore {
	return &SubagentStore{db: db}
}

const subagentColumns = `id, session_id, subagent_id, subagent_type, description, tool_use_id, model, spawn_depth,
	api_calls, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_usd,
	started_at, last_activity_at, created_at, updated_at`

// UpsertSubagent stores a subagent's current state, keyed by
// (session_id, subagent_id).
//
// The usage fields are CUMULATIVE TOTALS and replace what is stored; they are
// not added to it. Callers re-derive the totals from the whole transcript on
// every sync, so writing the same transcript twice, or re-syncing everything
// after a server restart, cannot double count.
//
// Metadata (type, description, tool_use_id, model) only overwrites a stored
// value when the new value is non-empty, so a sync that ran before the
// metadata file existed does not blank out a later, fuller one. started_at
// keeps its first recorded value.
//
// It fails with a foreign key error if SessionID is not a known main agent.
func (s *SubagentStore) UpsertSubagent(ctx context.Context, sa *Subagent) error {
	if sa.SessionID == "" || sa.SubagentID == "" {
		return fmt.Errorf("session_id and subagent_id are required")
	}
	now := nowUTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO subagents
		   (session_id, subagent_id, subagent_type, description, tool_use_id, model, spawn_depth,
		    api_calls, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_usd,
		    started_at, last_activity_at, created_at, updated_at)
		 VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?,
		         ?, ?, ?, ?, ?, ?,
		         NULLIF(?, ''), NULLIF(?, ''), ?, ?)
		 ON CONFLICT(session_id, subagent_id) DO UPDATE SET
		    subagent_type      = COALESCE(excluded.subagent_type, subagent_type),
		    description        = COALESCE(excluded.description, description),
		    tool_use_id        = COALESCE(excluded.tool_use_id, tool_use_id),
		    model              = COALESCE(excluded.model, model),
		    spawn_depth        = excluded.spawn_depth,
		    api_calls          = excluded.api_calls,
		    input_tokens       = excluded.input_tokens,
		    output_tokens      = excluded.output_tokens,
		    cache_read_tokens  = excluded.cache_read_tokens,
		    cache_write_tokens = excluded.cache_write_tokens,
		    cost_usd           = excluded.cost_usd,
		    started_at         = COALESCE(started_at, excluded.started_at),
		    last_activity_at   = COALESCE(excluded.last_activity_at, last_activity_at),
		    updated_at         = excluded.updated_at`,
		sa.SessionID, sa.SubagentID, derefStr(sa.SubagentType), derefStr(sa.Description),
		derefStr(sa.ToolUseID), derefStr(sa.Model), sa.SpawnDepth,
		sa.APICalls, sa.InputTokens, sa.OutputTokens, sa.CacheReadTokens, sa.CacheWriteTokens, sa.CostUSD,
		derefStr(sa.StartedAt), derefStr(sa.LastActivityAt), now, now)
	return err
}

// GetSubagent returns one subagent, or nil if it does not exist.
func (s *SubagentStore) GetSubagent(ctx context.Context, sessionID, subagentID string) (*Subagent, error) {
	var rows []Subagent
	err := s.db.SelectContext(ctx, &rows,
		`SELECT `+subagentColumns+` FROM subagents WHERE session_id = ? AND subagent_id = ?`,
		sessionID, subagentID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// ListSubagentsBySession returns a main agent's subagents, oldest first.
func (s *SubagentStore) ListSubagentsBySession(ctx context.Context, sessionID string) ([]Subagent, error) {
	var rows []Subagent
	err := s.db.SelectContext(ctx, &rows,
		`SELECT `+subagentColumns+` FROM subagents WHERE session_id = ?
		 ORDER BY COALESCE(started_at, created_at), id`, sessionID)
	return rows, err
}

// GetSubagentSpend returns summed subagent spend per main agent for the given
// sessions. Sessions without subagents are absent from the map.
func (s *SubagentStore) GetSubagentSpend(ctx context.Context, sessionIDs []string) (map[string]SubagentSpend, error) {
	result := map[string]SubagentSpend{}
	if len(sessionIDs) == 0 {
		return result, nil
	}
	query, args, err := sqlx.In(
		`SELECT session_id, COUNT(*) AS subagents,
		        COALESCE(SUM(api_calls), 0) AS api_calls,
		        COALESCE(SUM(input_tokens), 0) AS input_tokens,
		        COALESCE(SUM(output_tokens), 0) AS output_tokens,
		        COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
		        COALESCE(SUM(cache_write_tokens), 0) AS cache_write_tokens,
		        COALESCE(SUM(cost_usd), 0) AS cost_usd
		 FROM subagents WHERE session_id IN (?) GROUP BY session_id`, sessionIDs)
	if err != nil {
		return nil, err
	}
	var rows []SubagentSpend
	if err := s.db.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}
	for _, r := range rows {
		result[r.SessionID] = r
	}
	return result, nil
}
