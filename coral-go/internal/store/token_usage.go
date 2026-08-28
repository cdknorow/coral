package store

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"
)

// TokenUsage represents a token usage snapshot for a session.
type TokenUsage struct {
	ID               int64   `db:"id" json:"id"`
	SessionID        string  `db:"session_id" json:"session_id"`
	AgentName        string  `db:"agent_name" json:"agent_name"`
	AgentType        string  `db:"agent_type" json:"agent_type"`
	TeamID           *int64  `db:"team_id" json:"team_id,omitempty"`
	BoardName        *string `db:"board_name" json:"board_name,omitempty"`
	InputTokens      int     `db:"input_tokens" json:"input_tokens"`
	OutputTokens     int     `db:"output_tokens" json:"output_tokens"`
	CacheReadTokens  int     `db:"cache_read_tokens" json:"cache_read_tokens"`
	CacheWriteTokens int     `db:"cache_write_tokens" json:"cache_write_tokens"`
	TotalTokens      int     `db:"total_tokens" json:"total_tokens"`
	CostUSD          float64 `db:"cost_usd" json:"cost_usd"`
	NumTurns         int     `db:"num_turns" json:"num_turns"`
	SessionStartAt   string  `db:"session_start_at" json:"session_start_at,omitempty"`
	LastActivityAt   string  `db:"last_activity_at" json:"last_activity_at,omitempty"`
	ExecutionTimeSec int64   `db:"execution_time_sec" json:"execution_time_sec"`
	RecordedAt       string  `db:"recorded_at" json:"recorded_at"`
	Source           string  `db:"source" json:"source,omitempty"`
}

// UsageSummary represents aggregated token usage totals.
type UsageSummary struct {
	AgentType        string  `db:"agent_type" json:"agent_type"`
	InputTokens      int64   `db:"input_tokens" json:"input_tokens"`
	OutputTokens     int64   `db:"output_tokens" json:"output_tokens"`
	CacheReadTokens  int64   `db:"cache_read_tokens" json:"cache_read_tokens"`
	CacheWriteTokens int64   `db:"cache_write_tokens" json:"cache_write_tokens"`
	TotalTokens      int64   `db:"total_tokens" json:"total_tokens"`
	CostUSD          float64 `db:"cost_usd" json:"cost_usd"`
	NumSessions      int     `db:"num_sessions" json:"num_sessions"`
}

// AgentUsageSummary represents per-agent (session) token usage aggregates.
type AgentUsageSummary struct {
	SessionID        string  `db:"session_id" json:"session_id"`
	AgentName        string  `db:"agent_name" json:"agent_name"`
	AgentType        string  `db:"agent_type" json:"agent_type"`
	BoardName        *string `db:"board_name" json:"board_name,omitempty"`
	InputTokens      int64   `db:"input_tokens" json:"input_tokens"`
	OutputTokens     int64   `db:"output_tokens" json:"output_tokens"`
	CacheReadTokens  int64   `db:"cache_read_tokens" json:"cache_read_tokens"`
	CacheWriteTokens int64   `db:"cache_write_tokens" json:"cache_write_tokens"`
	TotalTokens      int64   `db:"total_tokens" json:"total_tokens"`
	CostUSD          float64 `db:"cost_usd" json:"cost_usd"`
	NumRecords       int     `db:"num_records" json:"requests"`
	FirstSeen        string  `db:"first_seen" json:"first_seen"`
	LastSeen         string  `db:"last_seen" json:"last_seen"`
	LaunchedAt       *string `db:"launched_at" json:"launched_at,omitempty"`
	StoppedAt        *string `db:"stopped_at" json:"stopped_at,omitempty"`
}

// sourceDedup is a SQL filter that prefers JSONL records over proxy records
// for the same session. When both sources exist, only JSONL is counted to
// avoid double-counting tokens.
const sourceDedup = `(COALESCE(source,'jsonl') = 'jsonl' OR session_id NOT IN (SELECT DISTINCT session_id FROM token_usage WHERE source = 'jsonl'))`

// TurnCost represents a single turn's cost data for charting.
type TurnCost struct {
	CostUSD    float64 `db:"cost_usd" json:"cost_usd"`
	RecordedAt string  `db:"recorded_at" json:"timestamp"`
}

// UsageFilter specifies filters for ListUsage.
type UsageFilter struct {
	SessionID string
	TeamID    *int64
	BoardName string
	Since     string
}

// TokenUsageStore provides CRUD and aggregation for token usage records.
type TokenUsageStore struct {
	db *DB
}

// NewTokenUsageStore creates a new TokenUsageStore.
func NewTokenUsageStore(db *DB) *TokenUsageStore {
	return &TokenUsageStore{db: db}
}

// RecordUsage inserts a per-call token usage record. Each row represents a
// single API call's delta tokens at a specific timestamp. The composite key
// (session_id, recorded_at) ensures one row per call — duplicates are ignored.
func (s *TokenUsageStore) RecordUsage(ctx context.Context, u *TokenUsage) error {
	if u.RecordedAt == "" {
		u.RecordedAt = NowUTC()
	}
	if u.AgentType == "" {
		u.AgentType = "claude"
	}

	if u.Source == "" {
		u.Source = "jsonl"
	}

	result, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO token_usage
		 (session_id, agent_name, agent_type, team_id, board_name, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, total_tokens, cost_usd, num_turns, session_start_at, last_activity_at, recorded_at, source)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.SessionID, u.AgentName, u.AgentType, u.TeamID, u.BoardName,
		u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens, u.TotalTokens, u.CostUSD, u.NumTurns, u.SessionStartAt, u.LastActivityAt, u.RecordedAt, u.Source)
	if err != nil {
		return err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	u.ID = id
	return nil
}

// GetSessionUsage returns aggregated token usage for a session (sum of all per-turn records).
func (s *TokenUsageStore) GetSessionUsage(ctx context.Context, sessionID string) (*TokenUsage, error) {
	var u TokenUsage
	err := s.db.GetContext(ctx, &u,
		`SELECT MAX(id) as id, session_id, MAX(agent_name) as agent_name, MAX(agent_type) as agent_type,
		 MAX(team_id) as team_id, MAX(board_name) as board_name,
		 COALESCE(SUM(input_tokens), 0) as input_tokens, COALESCE(SUM(output_tokens), 0) as output_tokens,
		 COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens, COALESCE(SUM(cache_write_tokens), 0) as cache_write_tokens,
		 COALESCE(SUM(total_tokens), 0) as total_tokens, COALESCE(SUM(cost_usd), 0) as cost_usd,
		 COUNT(*) as num_turns,
		 COALESCE(MIN(session_start_at), '') as session_start_at,
		 COALESCE(MAX(last_activity_at), '') as last_activity_at,
		 CASE WHEN MIN(NULLIF(session_start_at, '')) IS NOT NULL AND MAX(NULLIF(last_activity_at, '')) IS NOT NULL
		      THEN MAX(0, CAST(ROUND((julianday(MAX(NULLIF(last_activity_at, ''))) - julianday(MIN(NULLIF(session_start_at, '')))) * 86400) AS INTEGER))
		      ELSE 0 END as execution_time_sec,
		 MAX(recorded_at) as recorded_at
		 FROM token_usage WHERE session_id = ? AND `+sourceDedup+` GROUP BY session_id`, sessionID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// GetTeamUsage returns aggregated token usage for a team (sum of all per-turn records).
func (s *TokenUsageStore) GetTeamUsage(ctx context.Context, teamID int64) (*UsageSummary, error) {
	var summary UsageSummary
	err := s.db.GetContext(ctx, &summary,
		`SELECT COALESCE(SUM(input_tokens), 0) as input_tokens,
		        COALESCE(SUM(output_tokens), 0) as output_tokens,
		        COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens,
		        COALESCE(SUM(cache_write_tokens), 0) as cache_write_tokens,
		        COALESCE(SUM(total_tokens), 0) as total_tokens,
		        COALESCE(SUM(cost_usd), 0) as cost_usd,
		        COUNT(DISTINCT session_id) as num_sessions
		 FROM token_usage WHERE team_id = ? AND `+sourceDedup, teamID)
	if err != nil {
		return nil, err
	}
	return &summary, nil
}

// GetBoardUsage returns aggregated token usage for a board (sum of all per-turn records).
func (s *TokenUsageStore) GetBoardUsage(ctx context.Context, boardName string) (*UsageSummary, error) {
	var summary UsageSummary
	err := s.db.GetContext(ctx, &summary,
		`SELECT COALESCE(SUM(input_tokens), 0) as input_tokens,
		        COALESCE(SUM(output_tokens), 0) as output_tokens,
		        COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens,
		        COALESCE(SUM(cache_write_tokens), 0) as cache_write_tokens,
		        COALESCE(SUM(total_tokens), 0) as total_tokens,
		        COALESCE(SUM(cost_usd), 0) as cost_usd,
		        COUNT(DISTINCT session_id) as num_sessions
		 FROM token_usage WHERE board_name = ? AND `+sourceDedup, boardName)
	if err != nil {
		return nil, err
	}
	return &summary, nil
}

// GetUsageSummary returns totals grouped by agent_type since a given time.
func (s *TokenUsageStore) GetUsageSummary(ctx context.Context, since string) ([]UsageSummary, error) {
	var summaries []UsageSummary
	query := `SELECT agent_type,
	          COALESCE(SUM(input_tokens), 0) as input_tokens,
	          COALESCE(SUM(output_tokens), 0) as output_tokens,
	          COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens,
	          COALESCE(SUM(cache_write_tokens), 0) as cache_write_tokens,
	          COALESCE(SUM(total_tokens), 0) as total_tokens,
	          COALESCE(SUM(cost_usd), 0) as cost_usd,
	          COUNT(DISTINCT session_id) as num_sessions
	   FROM token_usage WHERE ` + sourceDedup + ` AND 1=1`
	var args []interface{}
	if since != "" {
		query += " AND recorded_at >= ?"
		args = append(args, since)
	}
	query += ` GROUP BY agent_type`
	err := s.db.SelectContext(ctx, &summaries, query, args...)
	return summaries, err
}

// GetUsageSummaryByAgent returns per-agent (session) usage aggregates since a given time.
func (s *TokenUsageStore) GetUsageSummaryByAgent(ctx context.Context, since string) ([]AgentUsageSummary, error) {
	var summaries []AgentUsageSummary
	query := `SELECT t.session_id, t.agent_name, t.agent_type, t.board_name,
	          COALESCE(SUM(t.input_tokens), 0) as input_tokens,
	          COALESCE(SUM(t.output_tokens), 0) as output_tokens,
	          COALESCE(SUM(t.cache_read_tokens), 0) as cache_read_tokens,
	          COALESCE(SUM(t.cache_write_tokens), 0) as cache_write_tokens,
	          COALESCE(SUM(t.total_tokens), 0) as total_tokens,
	          COALESCE(SUM(t.cost_usd), 0) as cost_usd,
	          COUNT(*) as num_records,
	          MIN(t.recorded_at) as first_seen,
	          MAX(t.recorded_at) as last_seen,
	          ls.created_at as launched_at,
	          ls.stopped_at as stopped_at
	   FROM token_usage t
	   LEFT JOIN live_sessions ls ON ls.session_id = t.session_id
	   WHERE (COALESCE(t.source,'jsonl') = 'jsonl' OR t.session_id NOT IN (SELECT DISTINCT session_id FROM token_usage WHERE source = 'jsonl')) AND 1=1`
	var args []interface{}
	if since != "" {
		query += " AND t.recorded_at >= ?"
		args = append(args, since)
	}
	query += ` GROUP BY t.session_id ORDER BY cost_usd DESC`
	err := s.db.SelectContext(ctx, &summaries, query, args...)
	return summaries, err
}

// ListUsage returns aggregated token usage per session (sum of all per-turn records).
func (s *TokenUsageStore) ListUsage(ctx context.Context, f UsageFilter) ([]TokenUsage, error) {
	query := `SELECT MAX(id) as id, session_id, MAX(agent_name) as agent_name, MAX(agent_type) as agent_type,
	          MAX(team_id) as team_id, MAX(board_name) as board_name,
	          COALESCE(SUM(input_tokens), 0) as input_tokens, COALESCE(SUM(output_tokens), 0) as output_tokens,
	          COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens, COALESCE(SUM(cache_write_tokens), 0) as cache_write_tokens,
	          COALESCE(SUM(total_tokens), 0) as total_tokens, COALESCE(SUM(cost_usd), 0) as cost_usd,
	          COUNT(*) as num_turns,
	          COALESCE(MIN(NULLIF(session_start_at, '')), '') as session_start_at,
	          COALESCE(MAX(NULLIF(last_activity_at, '')), '') as last_activity_at,
	          CASE WHEN MIN(NULLIF(session_start_at, '')) IS NOT NULL AND MAX(NULLIF(last_activity_at, '')) IS NOT NULL
	               THEN MAX(0, CAST(ROUND((julianday(MAX(NULLIF(last_activity_at, ''))) - julianday(MIN(NULLIF(session_start_at, '')))) * 86400) AS INTEGER))
	               ELSE 0 END as execution_time_sec,
	          MAX(recorded_at) as recorded_at,
	          COALESCE(MAX(source), 'jsonl') as source
	   FROM token_usage WHERE ` + sourceDedup + ` AND 1=1`
	var args []interface{}

	if f.SessionID != "" {
		query += " AND session_id = ?"
		args = append(args, f.SessionID)
	}
	if f.TeamID != nil {
		query += " AND team_id = ?"
		args = append(args, *f.TeamID)
	}
	if f.BoardName != "" {
		query += " AND board_name = ?"
		args = append(args, f.BoardName)
	}
	if f.Since != "" {
		query += " AND recorded_at >= ?"
		args = append(args, f.Since)
	}

	query += ` GROUP BY session_id ORDER BY MAX(recorded_at) DESC`

	var results []TokenUsage
	err := s.db.SelectContext(ctx, &results, query, args...)
	return results, err
}

// GetLatestUsageBySessionIDs returns the latest token usage record per session
// for the given session IDs, keyed by session_id.
func (s *TokenUsageStore) GetLatestUsageBySessionIDs(ctx context.Context, sessionIDs []string) (map[string]*TokenUsage, error) {
	if len(sessionIDs) == 0 {
		return map[string]*TokenUsage{}, nil
	}

	query, args, err := sqlx.In(
		`SELECT MAX(id) as id, session_id, MAX(agent_name) as agent_name, MAX(agent_type) as agent_type,
		 MAX(team_id) as team_id, MAX(board_name) as board_name,
		 COALESCE(SUM(input_tokens), 0) as input_tokens, COALESCE(SUM(output_tokens), 0) as output_tokens,
		 COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens, COALESCE(SUM(cache_write_tokens), 0) as cache_write_tokens,
		 COALESCE(SUM(total_tokens), 0) as total_tokens, COALESCE(SUM(cost_usd), 0) as cost_usd,
		 COUNT(*) as num_turns, MAX(recorded_at) as recorded_at
		 FROM token_usage WHERE session_id IN (?) AND `+sourceDedup+`
		 GROUP BY session_id`, sessionIDs)
	if err != nil {
		return nil, err
	}

	var records []TokenUsage
	if err := s.db.SelectContext(ctx, &records, query, args...); err != nil {
		return nil, err
	}

	result := make(map[string]*TokenUsage, len(records))
	for i := range records {
		result[records[i].SessionID] = &records[i]
	}
	return result, nil
}

// GetLatestTurnContextBySessionIDs returns the latest single turn's total context
// usage per session (input_tokens + cache_read_tokens + cache_write_tokens).
// This represents the current context window fill level since each API call
// sends the full conversation as input (split across fresh, cached, and new-cache tokens).
func (s *TokenUsageStore) GetLatestTurnContextBySessionIDs(ctx context.Context, sessionIDs []string) (map[string]int, error) {
	if len(sessionIDs) == 0 {
		return map[string]int{}, nil
	}

	// Use qualified sourceDedup for the outer WHERE to avoid ambiguous column name
	qualifiedDedup := `(COALESCE(t.source,'jsonl') = 'jsonl' OR t.session_id NOT IN (SELECT DISTINCT session_id FROM token_usage WHERE source = 'jsonl'))`
	query, args, err := sqlx.In(
		`SELECT t.session_id, (t.input_tokens + t.cache_read_tokens + t.cache_write_tokens) as context_tokens
		 FROM token_usage t
		 INNER JOIN (
		     SELECT session_id, MAX(recorded_at) as max_at
		     FROM token_usage
		     WHERE session_id IN (?) AND `+sourceDedup+`
		     GROUP BY session_id
		 ) latest ON t.session_id = latest.session_id AND t.recorded_at = latest.max_at
		 WHERE t.session_id IN (?) AND `+qualifiedDedup, sessionIDs, sessionIDs)
	if err != nil {
		return nil, err
	}

	type row struct {
		SessionID     string `db:"session_id"`
		ContextTokens int    `db:"context_tokens"`
	}
	var rows []row
	if err := s.db.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}

	result := make(map[string]int, len(rows))
	for _, r := range rows {
		result[r.SessionID] = r.ContextTokens
	}
	return result, nil
}

// GetSessionTurns returns per-turn cost data for a session, ordered by time.
func (s *TokenUsageStore) GetSessionTurns(ctx context.Context, sessionID string) ([]TurnCost, error) {
	var turns []TurnCost
	err := s.db.SelectContext(ctx, &turns,
		`SELECT cost_usd, recorded_at FROM token_usage
		 WHERE session_id = ? AND `+sourceDedup+`
		 ORDER BY recorded_at ASC`, sessionID)
	return turns, err
}

// TimeSeriesBucket represents a single time bucket of aggregated cost.
type TimeSeriesBucket struct {
	Bucket           string  `db:"bucket" json:"bucket"`
	CostUSD          float64 `db:"cost_usd" json:"cost_usd"`
	InputTokens      int64   `db:"input_tokens" json:"input_tokens"`
	OutputTokens     int64   `db:"output_tokens" json:"output_tokens"`
	CacheReadTokens  int64   `db:"cache_read_tokens" json:"cache_read_tokens"`
	CacheWriteTokens int64   `db:"cache_write_tokens" json:"cache_write_tokens"`
	NumRequests      int     `db:"num_requests" json:"num_requests"`
}

// GetUsageTimeSeries returns cost data bucketed by time interval.
// The interval parameter controls bucket size: "5m", "1h", "1d".
func (s *TokenUsageStore) GetUsageTimeSeries(ctx context.Context, since string, interval string) ([]TimeSeriesBucket, error) {
	// Map interval to SQLite strftime format
	var bucketExpr string
	switch interval {
	case "5m":
		// Round to 5-minute intervals
		bucketExpr = `strftime('%Y-%m-%dT%H:', recorded_at) || printf('%02d', (CAST(strftime('%M', recorded_at) AS INTEGER) / 5) * 5) || ':00Z'`
	case "1h":
		bucketExpr = `strftime('%Y-%m-%dT%H:00:00Z', recorded_at)`
	case "1d":
		bucketExpr = `strftime('%Y-%m-%dT00:00:00Z', recorded_at)`
	default:
		bucketExpr = `strftime('%Y-%m-%dT%H:00:00Z', recorded_at)`
	}

	query := `SELECT ` + bucketExpr + ` as bucket,
	          COALESCE(SUM(cost_usd), 0) as cost_usd,
	          COALESCE(SUM(input_tokens), 0) as input_tokens,
	          COALESCE(SUM(output_tokens), 0) as output_tokens,
	          COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens,
	          COALESCE(SUM(cache_write_tokens), 0) as cache_write_tokens,
	          COUNT(*) as num_requests
	   FROM token_usage WHERE ` + sourceDedup + ` AND 1=1`

	var args []interface{}
	if since != "" {
		query += " AND recorded_at >= ?"
		args = append(args, since)
	}
	query += ` GROUP BY bucket ORDER BY bucket ASC`

	var buckets []TimeSeriesBucket
	err := s.db.SelectContext(ctx, &buckets, query, args...)
	return buckets, err
}

// BoardUsageSummary represents aggregated token usage for a board/team.
type BoardUsageSummary struct {
	BoardName        string  `db:"board_name" json:"board_name"`
	InputTokens      int64   `db:"input_tokens" json:"input_tokens"`
	OutputTokens     int64   `db:"output_tokens" json:"output_tokens"`
	CacheReadTokens  int64   `db:"cache_read_tokens" json:"cache_read_tokens"`
	CacheWriteTokens int64   `db:"cache_write_tokens" json:"cache_write_tokens"`
	TotalTokens      int64   `db:"total_tokens" json:"total_tokens"`
	CostUSD          float64 `db:"cost_usd" json:"cost_usd"`
	NumAgents        int     `db:"num_agents" json:"num_agents"`
}

// BranchUsageSummary represents aggregated token usage for a git branch.
type BranchUsageSummary struct {
	Branch           string  `db:"branch" json:"branch"`
	PRNumber         int     `db:"pr_number" json:"pr_number,omitempty"`
	RemoteURL        string  `db:"remote_url" json:"remote_url,omitempty"`
	RepoName         string  `db:"-" json:"repo_name,omitempty"`
	BoardName        string  `db:"board_name" json:"board_name"`
	InputTokens      int64   `db:"input_tokens" json:"input_tokens"`
	OutputTokens     int64   `db:"output_tokens" json:"output_tokens"`
	CacheReadTokens  int64   `db:"cache_read_tokens" json:"cache_read_tokens"`
	CacheWriteTokens int64   `db:"cache_write_tokens" json:"cache_write_tokens"`
	TotalTokens      int64   `db:"total_tokens" json:"total_tokens"`
	CostUSD          float64 `db:"cost_usd" json:"cost_usd"`
	NumAgents        int     `db:"num_agents" json:"num_agents"`
}

// GetUsageSummaryByBranch returns per-branch usage aggregates by joining
// token_usage with git_snapshots on session_id. Only sessions that have
// at least one git snapshot are included. Each session is attributed to
// its most recent branch.
func (s *TokenUsageStore) GetUsageSummaryByBranch(ctx context.Context, since string, branch string) ([]BranchUsageSummary, error) {
	query := `SELECT gs.branch,
	          COALESCE(MAX(gs.pr_number), 0) as pr_number,
	          COALESCE(MAX(gs.remote_url), '') as remote_url,
	          COALESCE(MAX(t.board_name), '') as board_name,
	          COALESCE(SUM(t.input_tokens), 0) as input_tokens,
	          COALESCE(SUM(t.output_tokens), 0) as output_tokens,
	          COALESCE(SUM(t.cache_read_tokens), 0) as cache_read_tokens,
	          COALESCE(SUM(t.cache_write_tokens), 0) as cache_write_tokens,
	          COALESCE(SUM(t.total_tokens), 0) as total_tokens,
	          COALESCE(SUM(t.cost_usd), 0) as cost_usd,
	          COUNT(DISTINCT t.session_id) as num_agents
	   FROM token_usage t
	   INNER JOIN (
	       SELECT session_id, branch, COALESCE(pr_number, 0) as pr_number, COALESCE(remote_url, '') as remote_url
	       FROM git_snapshots
	       WHERE session_id IS NOT NULL
	       AND id IN (
	           SELECT MAX(id) FROM git_snapshots
	           WHERE session_id IS NOT NULL
	           GROUP BY session_id
	       )
	   ) gs ON t.session_id = gs.session_id
	   WHERE (COALESCE(t.source,'jsonl') = 'jsonl' OR t.session_id NOT IN (SELECT DISTINCT session_id FROM token_usage WHERE source = 'jsonl')) AND 1=1`
	var args []interface{}
	if since != "" {
		query += " AND t.recorded_at >= ?"
		args = append(args, since)
	}
	if branch != "" {
		query += " AND gs.branch = ?"
		args = append(args, branch)
	}
	query += ` GROUP BY gs.branch, gs.remote_url, COALESCE(t.board_name, '') ORDER BY cost_usd DESC`
	var summaries []BranchUsageSummary
	err := s.db.SelectContext(ctx, &summaries, query, args...)
	return summaries, err
}

// GetUsageSummaryByBoard returns per-board (team) usage aggregates since a given time.
func (s *TokenUsageStore) GetUsageSummaryByBoard(ctx context.Context, since string) ([]BoardUsageSummary, error) {
	query := `SELECT COALESCE(board_name, '') as board_name,
	          COALESCE(SUM(input_tokens), 0) as input_tokens,
	          COALESCE(SUM(output_tokens), 0) as output_tokens,
	          COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens,
	          COALESCE(SUM(cache_write_tokens), 0) as cache_write_tokens,
	          COALESCE(SUM(total_tokens), 0) as total_tokens,
	          COALESCE(SUM(cost_usd), 0) as cost_usd,
	          COUNT(DISTINCT session_id) as num_agents
	   FROM token_usage WHERE ` + sourceDedup + ` AND 1=1`
	var args []interface{}
	if since != "" {
		query += " AND recorded_at >= ?"
		args = append(args, since)
	}
	query += ` GROUP BY COALESCE(board_name, '') ORDER BY cost_usd DESC`
	var summaries []BoardUsageSummary
	err := s.db.SelectContext(ctx, &summaries, query, args...)
	return summaries, err
}
