package store

import (
	"context"
	"database/sql"
	"github.com/subgentic/coral/internal/clitext"
	"sort"
	"strings"
	"time"

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
	// Model is the model the agent reported for these tokens. Set it only to a
	// value that was actually observed: leave it empty (stored as NULL) rather
	// than filling in a default, so the column never holds a guess. It is
	// write-only for now: the session-level reads aggregate rows that may span
	// several models, so they do not select it.
	Model string `db:"model" json:"model,omitempty"`
	// Thinking is the thinking level the call's turn ran at, as the transcript recorded it ("" when it recorded none: never a guess).
	// ThinkingTokens is the thinking tokens the CLI reported for the call. Both only feed the Cost view's breakdown by thinking level
	// (token_usage_thinking); they are not columns of the usage rows (#4969).
	Thinking       string `db:"-" json:"-"`
	ThinkingTokens int    `db:"-" json:"-"`
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

// ModelUsageSummary is token usage for one model id of one agent type. Model is "" for usage
// that recorded no model (older rows, or a CLI that does not report one): that bucket is shown
// as unknown, never filled with a guess.
type ModelUsageSummary struct {
	AgentType        string  `db:"agent_type" json:"agent_type"`
	Model            string  `db:"model" json:"model"`
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
	Folder           string  `db:"folder" json:"folder"`
	WorkingDir       string  `db:"working_dir" json:"working_dir"`
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
const sourceDedup = `(COALESCE(source,'jsonl') = 'jsonl' OR session_id NOT IN (SELECT DISTINCT session_id FROM token_usage_all WHERE source = 'jsonl'))`

// usageDedup is sourceDedup over usageRelation's rows (source is already COALESCEd there), with the sessions that have a jsonl row read from
// token_usage_rollup (one row per session and source) rather than from a scan of every usage row. alias is "" or "t.".
func usageDedup(alias string) string {
	return `(` + alias + `source = 'jsonl' OR ` + alias + `session_id NOT IN (SELECT session_id FROM token_usage_rollup WHERE source = 'jsonl'))`
}

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

// RepriceZeroCosts fills historical usage rows whose model is now recognized.
// Older pollers stored zero when a provider-specific model alias was unknown.
func (s *TokenUsageStore) RepriceZeroCosts(ctx context.Context, price func(model string, input, output, cacheRead, cacheWrite int) float64) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens FROM token_usage WHERE cost_usd = 0 AND model IS NOT NULL AND model <> ''`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type row struct {
		id   int64
		cost float64
	}
	var updates []row
	for rows.Next() {
		var id int64
		var model string
		var in, out, read, write int
		if err := rows.Scan(&id, &model, &in, &out, &read, &write); err != nil {
			return err
		}
		cost := price(model, in, out, read, write)
		if cost > 0 {
			updates = append(updates, row{id, cost})
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, u := range updates {
		if _, err := s.db.ExecContext(ctx, `UPDATE token_usage SET cost_usd = ? WHERE id = ? AND cost_usd = 0`, u.cost, u.id); err != nil {
			return err
		}
	}
	return nil
}

// NewTokenUsageStore creates a new TokenUsageStore.
func NewTokenUsageStore(db *DB) *TokenUsageStore {
	return &TokenUsageStore{db: db}
}

// RecordUsage inserts a per-call token usage record. Each row represents a
// single API call's delta tokens at a specific timestamp. The composite key
// (session_id, recorded_at) ensures one row per call — duplicates are ignored.
func (s *TokenUsageStore) RecordUsage(ctx context.Context, u *TokenUsage) error {
	return s.recordUsageOn(ctx, s.db, u)
}

// usageExecer is what a record is written through: the database (one statement at a time, each its own
// transaction) or a transaction (RecordUsageBatch).
type usageExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// usageBatchMax is the most records RecordUsageBatch writes in one transaction: the database has one
// writer, and a first scan of a long transcript has thousands of calls, which should not hold it for all of them.
const usageBatchMax = 500

// RecordUsageBatch records each of us as RecordUsage does (the same statements, the same duplicate and
// timestamp rules, the thinking breakdown only for a call that was stored), many to a transaction instead
// of one: on a first read of a long transcript the transaction per call was most of the token poller's time
// (#5074). The result is one error per record, nil for one that was recorded or ignored as a repeat; a record
// that fails does not stop the others. If a transaction cannot begin or commit, its records all carry that error.
func (s *TokenUsageStore) RecordUsageBatch(ctx context.Context, us []*TokenUsage) []error {
	errs := make([]error, len(us))
	for lo := 0; lo < len(us); lo += usageBatchMax {
		hi := min(lo+usageBatchMax, len(us))
		tx, err := s.db.BeginTxx(ctx, nil)
		if err != nil {
			for i := lo; i < hi; i++ {
				errs[i] = err
			}
			continue
		}
		ex := &preparedExecer{tx: tx}
		for i := lo; i < hi; i++ {
			errs[i] = s.recordUsageOn(ctx, ex, us[i])
		}
		ex.close()
		if err := tx.Commit(); err != nil {
			_ = tx.Rollback()
			for i := lo; i < hi; i++ {
				errs[i] = err
			}
		}
	}
	return errs
}

// preparedExecer runs a transaction's statements prepared once each: a batch repeats the same two statements for every
// record, and parsing them again each time was a large part of the cost of a record.
type preparedExecer struct {
	tx    *sqlx.Tx
	stmts map[string]*sql.Stmt
}

func (p *preparedExecer) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	st, ok := p.stmts[query]
	if !ok {
		var err error
		if st, err = p.tx.PrepareContext(ctx, query); err != nil {
			return nil, err
		}
		if p.stmts == nil {
			p.stmts = map[string]*sql.Stmt{}
		}
		p.stmts[query] = st
	}
	return st.ExecContext(ctx, args...)
}

func (p *preparedExecer) close() {
	for _, st := range p.stmts {
		_ = st.Close()
	}
}

func (s *TokenUsageStore) recordUsageOn(ctx context.Context, ex usageExecer, u *TokenUsage) error {
	stamped := u.RecordedAt == ""
	if stamped {
		u.RecordedAt = tokenUsageNow()
	}
	if u.AgentType == "" {
		u.AgentType = "claude"
	}

	if u.Source == "" {
		u.Source = "jsonl"
	}

	insert := func() (sql.Result, error) {
		return ex.ExecContext(ctx,
			`INSERT OR IGNORE INTO token_usage
		 (session_id, agent_name, agent_type, team_id, board_name, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, total_tokens, cost_usd, num_turns, session_start_at, last_activity_at, recorded_at, source, model)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''))`,
			u.SessionID, u.AgentName, u.AgentType, u.TeamID, u.BoardName,
			u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens, u.TotalTokens, u.CostUSD, u.NumTurns, u.SessionStartAt, u.LastActivityAt, u.RecordedAt, u.Source, strings.TrimSpace(u.Model))
	}
	result, err := insert()
	if err != nil {
		return err
	}
	// (session_id, recorded_at) drops a second report of the same transcript turn. A row Coral
	// stamped itself (a report with no timestamp) that lands on the same instant as another of
	// its session is not a duplicate: it takes the next free microsecond rather than being
	// dropped (#3920: on Windows' coarser clock two reports in a row collided).
	for i := 0; stamped && i < 1000; i++ {
		if n, _ := result.RowsAffected(); n > 0 {
			break
		}
		next, perr := time.Parse(ISOFormat, u.RecordedAt)
		if perr != nil {
			break
		}
		u.RecordedAt = next.Add(time.Microsecond).Format(ISOFormat)
		if result, err = insert(); err != nil {
			return err
		}
	}
	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	u.ID = id
	if n, _ := result.RowsAffected(); n > 0 {
		return s.recordThinking(ctx, ex, u) // only a call that was stored counts: a repeated report is ignored
	}
	return nil
}

// GetSessionUsage returns aggregated token usage for a session (sum of all per-turn records).
func (s *TokenUsageStore) GetSessionUsage(ctx context.Context, sessionID string) (*TokenUsage, error) {
	var u TokenUsage
	err := s.db.Reader().GetContext(ctx, &u,
		`SELECT MAX(id) as id, session_id, MAX(agent_name) as agent_name, MAX(agent_type) as agent_type,
		 MAX(team_id) as team_id, MAX(board_name) as board_name,
		 COALESCE(SUM(input_tokens), 0) as input_tokens, COALESCE(SUM(output_tokens), 0) as output_tokens,
		 COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens, COALESCE(SUM(cache_write_tokens), 0) as cache_write_tokens,
		 COALESCE(SUM(total_tokens), 0) as total_tokens, COALESCE(SUM(cost_usd), 0) as cost_usd,
		 SUM(n_rows) as num_turns,
		 COALESCE(MIN(session_start_at), '') as session_start_at,
		 COALESCE(MAX(last_activity_at), '') as last_activity_at,
		 CASE WHEN MIN(NULLIF(session_start_at, '')) IS NOT NULL AND MAX(NULLIF(last_activity_at, '')) IS NOT NULL
		      THEN MAX(0, CAST(ROUND((julianday(MAX(NULLIF(last_activity_at, ''))) - julianday(MIN(NULLIF(session_start_at, '')))) * 86400) AS INTEGER))
		      ELSE 0 END as execution_time_sec,
		 MAX(recorded_at) as recorded_at
		 FROM token_usage_all WHERE session_id = ? AND `+sourceDedup+` GROUP BY session_id`, sessionID)
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
	err := s.db.Reader().GetContext(ctx, &summary,
		`SELECT COALESCE(SUM(input_tokens), 0) as input_tokens,
		        COALESCE(SUM(output_tokens), 0) as output_tokens,
		        COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens,
		        COALESCE(SUM(cache_write_tokens), 0) as cache_write_tokens,
		        COALESCE(SUM(total_tokens), 0) as total_tokens,
		        COALESCE(SUM(cost_usd), 0) as cost_usd,
		        COUNT(DISTINCT session_id) as num_sessions
		 FROM token_usage_all WHERE team_id = ? AND `+sourceDedup, teamID)
	if err != nil {
		return nil, err
	}
	return &summary, nil
}

// GetBoardUsage returns aggregated token usage for a board (sum of all per-turn records).
func (s *TokenUsageStore) GetBoardUsage(ctx context.Context, boardName string) (*UsageSummary, error) {
	var summary UsageSummary
	err := s.db.Reader().GetContext(ctx, &summary,
		`SELECT COALESCE(SUM(input_tokens), 0) as input_tokens,
		        COALESCE(SUM(output_tokens), 0) as output_tokens,
		        COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens,
		        COALESCE(SUM(cache_write_tokens), 0) as cache_write_tokens,
		        COALESCE(SUM(total_tokens), 0) as total_tokens,
		        COALESCE(SUM(cost_usd), 0) as cost_usd,
		        COUNT(DISTINCT session_id) as num_sessions
		 FROM token_usage_all WHERE board_name = ? AND `+sourceDedup, boardName)
	if err != nil {
		return nil, err
	}
	return &summary, nil
}

// GetUsageSummary returns totals grouped by agent_type since a given time.
func (s *TokenUsageStore) GetUsageSummary(ctx context.Context, since string) ([]UsageSummary, error) {
	var summaries []UsageSummary
	rel, args := usageRelation(ctx, s.db.Reader(), since)
	query := `SELECT agent_type,
	          COALESCE(SUM(input_tokens), 0) as input_tokens,
	          COALESCE(SUM(output_tokens), 0) as output_tokens,
	          COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens,
	          COALESCE(SUM(cache_write_tokens), 0) as cache_write_tokens,
	          COALESCE(SUM(total_tokens), 0) as total_tokens,
	          COALESCE(SUM(cost_usd), 0) as cost_usd,
	          COUNT(DISTINCT session_id) as num_sessions
	   FROM ` + rel + ` u WHERE ` + usageDedup("u.")
	query += ` GROUP BY agent_type`
	err := s.db.Reader().SelectContext(ctx, &summaries, query, args...)
	return summaries, err
}

// GetUsageSummaryByModel returns totals grouped by agent type and the model id each row
// recorded. Rows are per API call (hourly rows keep the model too), so a session that switched
// models counts in each model's row with its tokens split between them. Usage with no recorded
// model is one bucket with model "".
func (s *TokenUsageStore) GetUsageSummaryByModel(ctx context.Context, since string) ([]ModelUsageSummary, error) {
	summaries := []ModelUsageSummary{}
	rel, args := usageRelation(ctx, s.db.Reader(), since)
	query := `SELECT agent_type, COALESCE(model, '') as model,
	          COALESCE(SUM(input_tokens), 0) as input_tokens,
	          COALESCE(SUM(output_tokens), 0) as output_tokens,
	          COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens,
	          COALESCE(SUM(cache_write_tokens), 0) as cache_write_tokens,
	          COALESCE(SUM(total_tokens), 0) as total_tokens,
	          COALESCE(SUM(cost_usd), 0) as cost_usd,
	          COUNT(DISTINCT session_id) as num_sessions
	   FROM ` + rel + ` u WHERE ` + usageDedup("u.")
	query += ` GROUP BY agent_type, COALESCE(model, '') ORDER BY cost_usd DESC, agent_type, model`
	err := s.db.Reader().SelectContext(ctx, &summaries, query, args...)
	return summaries, err
}

// GetUsageSummaryByAgent returns per-agent (session) usage aggregates since a given time.
func (s *TokenUsageStore) GetUsageSummaryByAgent(ctx context.Context, since string) ([]AgentUsageSummary, error) {
	var summaries []AgentUsageSummary
	rel, args := usageRelation(ctx, s.db.Reader(), since)
	// Prefer the session's current display name so renames show; unnamed
	// agents come back empty and the UI labels them "Agent". The folder is
	// reported separately so those rows can still be told apart.
	query := `SELECT t.session_id,
	          COALESCE(NULLIF(ls.display_name, ''), MAX(t.agent_name), '') as agent_name,
	          t.agent_type, COALESCE(NULLIF(t.board_name, ''), ls.board_name) as board_name,
	          COALESCE(ls.agent_name, '') as folder,
	          COALESCE(ls.working_dir, '') as working_dir,
	          COALESCE(SUM(t.input_tokens), 0) as input_tokens,
	          COALESCE(SUM(t.output_tokens), 0) as output_tokens,
	          COALESCE(SUM(t.cache_read_tokens), 0) as cache_read_tokens,
	          COALESCE(SUM(t.cache_write_tokens), 0) as cache_write_tokens,
	          COALESCE(SUM(t.total_tokens), 0) as total_tokens,
	          COALESCE(SUM(t.cost_usd), 0) as cost_usd,
	          SUM(t.n_rows) as num_records,
	          MIN(t.min_at) as first_seen,
	          MAX(t.max_at) as last_seen,
	          ls.created_at as launched_at,
	          ls.stopped_at as stopped_at
	   FROM ` + rel + ` t
	   LEFT JOIN live_sessions ls ON ls.session_id = t.session_id
	   WHERE ` + usageDedup("t.")
	query += ` GROUP BY t.session_id ORDER BY cost_usd DESC`
	err := s.db.Reader().SelectContext(ctx, &summaries, query, args...)
	return summaries, err
}

// ListUsage returns aggregated token usage per session (sum of all per-turn records).
func (s *TokenUsageStore) ListUsage(ctx context.Context, f UsageFilter) ([]TokenUsage, error) {
	query := `SELECT MAX(t.id) as id, t.session_id,
	          COALESCE(NULLIF(MAX(ls.display_name), ''), MAX(t.agent_name)) as agent_name,
	          MAX(t.agent_type) as agent_type,
	          MAX(t.team_id) as team_id, COALESCE(NULLIF(MAX(t.board_name), ''), MAX(ls.board_name)) as board_name,
	          COALESCE(SUM(t.input_tokens), 0) as input_tokens, COALESCE(SUM(t.output_tokens), 0) as output_tokens,
	          COALESCE(SUM(t.cache_read_tokens), 0) as cache_read_tokens, COALESCE(SUM(t.cache_write_tokens), 0) as cache_write_tokens,
	          COALESCE(SUM(t.total_tokens), 0) as total_tokens, COALESCE(SUM(t.cost_usd), 0) as cost_usd,
	          SUM(t.n_rows) as num_turns,
	          COALESCE(MIN(NULLIF(t.session_start_at, '')), '') as session_start_at,
	          COALESCE(MAX(NULLIF(t.last_activity_at, '')), '') as last_activity_at,
	          CASE WHEN MIN(NULLIF(t.session_start_at, '')) IS NOT NULL AND MAX(NULLIF(t.last_activity_at, '')) IS NOT NULL
	               THEN MAX(0, CAST(ROUND((julianday(MAX(NULLIF(t.last_activity_at, ''))) - julianday(MIN(NULLIF(t.session_start_at, '')))) * 86400) AS INTEGER))
	               ELSE 0 END as execution_time_sec,
	          MAX(t.recorded_at) as recorded_at,
	          COALESCE(MAX(t.source), 'jsonl') as source
	   FROM token_usage_all t LEFT JOIN live_sessions ls ON ls.session_id = t.session_id WHERE (COALESCE(t.source,'jsonl') = 'jsonl' OR t.session_id NOT IN (SELECT DISTINCT session_id FROM token_usage_all WHERE source = 'jsonl')) AND 1=1`
	var args []interface{}

	if f.SessionID != "" {
		query += " AND t.session_id = ?"
		args = append(args, f.SessionID)
	}
	if f.TeamID != nil {
		query += " AND t.team_id = ?"
		args = append(args, *f.TeamID)
	}
	if f.BoardName != "" {
		query += " AND COALESCE(NULLIF(t.board_name, ''), ls.board_name) = ?"
		args = append(args, f.BoardName)
	}
	if f.Since != "" {
		query += " AND t.recorded_at >= ?"
		args = append(args, f.Since)
	}

	query += ` GROUP BY t.session_id ORDER BY MAX(t.recorded_at) DESC, t.session_id`

	var results []TokenUsage
	err := s.db.Reader().SelectContext(ctx, &results, query, args...)
	return results, err
}

// latestUsageRollup selects the rollup rows that count for each requested
// session under the sourceDedup rule: its jsonl row if it has one, otherwise
// all of its rows. Reading the rollup instead of token_usage keeps the
// per-tick cost independent of how many turns a session has had.
const latestUsageRollup = `WITH r AS (
	SELECT * FROM token_usage_rollup WHERE session_id IN (SELECT value FROM json_each(?))
), counted AS (
	SELECT * FROM r WHERE source = 'jsonl'
	   OR NOT EXISTS (SELECT 1 FROM r j WHERE j.session_id = r.session_id AND j.source = 'jsonl')
)`

// GetLatestUsageBySessionIDs returns the latest token usage record per session
// for the given session IDs, keyed by session_id.
func (s *TokenUsageStore) GetLatestUsageBySessionIDs(ctx context.Context, sessionIDs []string) (map[string]*TokenUsage, error) {
	if len(sessionIDs) == 0 {
		return map[string]*TokenUsage{}, nil
	}
	ids, err := sessionIDsJSON(sessionIDs)
	if err != nil {
		return nil, err
	}
	var records []TokenUsage
	// The read pool (#3461): the live list asks this on every refresh.
	if err := s.db.Reader().SelectContext(ctx, &records, latestUsageRollup+`
		SELECT MAX(max_id) as id, session_id, MAX(agent_name) as agent_name, MAX(agent_type) as agent_type,
		 MAX(team_id) as team_id, MAX(board_name) as board_name,
		 SUM(input_tokens) as input_tokens, SUM(output_tokens) as output_tokens,
		 SUM(cache_read_tokens) as cache_read_tokens, SUM(cache_write_tokens) as cache_write_tokens,
		 SUM(total_tokens) as total_tokens, SUM(cost_usd) as cost_usd,
		 SUM(num_turns) as num_turns, MAX(recorded_at) as recorded_at
		FROM counted GROUP BY session_id`, ids); err != nil {
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
	ids, err := sessionIDsJSON(sessionIDs)
	if err != nil {
		return nil, err
	}
	// With a single MAX() aggregate, SQLite takes the bare latest_context
	// from the row holding the newest turn.
	var rows []struct {
		SessionID     string `db:"session_id"`
		ContextTokens int    `db:"context_tokens"`
		RecordedAt    string `db:"recorded_at"`
	}
	// The read pool (#3461): the live list asks this on every refresh.
	if err := s.db.Reader().SelectContext(ctx, &rows, latestUsageRollup+`
		SELECT session_id, latest_context as context_tokens, MAX(recorded_at) as recorded_at
		FROM counted GROUP BY session_id`, ids); err != nil {
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
	err := s.db.Reader().SelectContext(ctx, &turns,
		`SELECT cost_usd, recorded_at FROM token_usage_all
		 WHERE session_id = ? AND `+sourceDedup+`
		 ORDER BY recorded_at ASC, id ASC`, sessionID)
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

	// 5-minute buckets need the rows themselves; the hour and day buckets are made from the hourly aggregate.
	var rel string
	var args []interface{}
	if interval == "5m" {
		rel, args = `(SELECT `+usageCols+`, n_rows, recorded_at FROM token_usage_all`+sinceWhere(since)+`)`, sinceArgs(since)
	} else {
		rel, args = usageRelation(ctx, s.db.Reader(), since)
	}
	query := `SELECT ` + bucketExpr + ` as bucket,
	          COALESCE(SUM(cost_usd), 0) as cost_usd,
	          COALESCE(SUM(input_tokens), 0) as input_tokens,
	          COALESCE(SUM(output_tokens), 0) as output_tokens,
	          COALESCE(SUM(cache_read_tokens), 0) as cache_read_tokens,
	          COALESCE(SUM(cache_write_tokens), 0) as cache_write_tokens,
	          SUM(n_rows) as num_requests
	   FROM ` + rel + ` u WHERE ` + usageDedup("u.")
	query += ` GROUP BY bucket ORDER BY bucket ASC`

	var buckets []TimeSeriesBucket
	err := s.db.Reader().SelectContext(ctx, &buckets, query, args...)
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
	rel, args := usageRelation(ctx, s.db.Reader(), since)
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
	   FROM ` + rel + ` t
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
	   WHERE ` + usageDedup("t.")
	if branch != "" {
		query += " AND gs.branch = ?"
		args = append(args, branch)
	}
	query += ` GROUP BY gs.branch, gs.remote_url, COALESCE(t.board_name, '') ORDER BY cost_usd DESC`
	var summaries []BranchUsageSummary
	err := s.db.Reader().SelectContext(ctx, &summaries, query, args...)
	return mergeCleanedBranches(summaries), err
}

// mergeCleanedBranches shows each branch cleaned (a name stored before the write path cleaned it, #6832) and merges the rows whose names clean to the same one (same remote and board too): two raw spellings of
// one branch are one row, their tokens and cost added (num_agents added too: it is an upper bound for the merged row), the biggest spender first as before.
func mergeCleanedBranches(in []BranchUsageSummary) []BranchUsageSummary {
	type key struct{ branch, remote, board string }
	index := map[key]int{}
	out := make([]BranchUsageSummary, 0, len(in))
	for _, r := range in {
		r.Branch = clitext.CleanName(r.Branch)
		k := key{r.Branch, r.RemoteURL, r.BoardName}
		i, seen := index[k]
		if !seen {
			index[k] = len(out)
			out = append(out, r)
			continue
		}
		m := &out[i]
		m.InputTokens += r.InputTokens
		m.OutputTokens += r.OutputTokens
		m.CacheReadTokens += r.CacheReadTokens
		m.CacheWriteTokens += r.CacheWriteTokens
		m.TotalTokens += r.TotalTokens
		m.CostUSD += r.CostUSD
		m.NumAgents += r.NumAgents
		m.PRNumber = max(m.PRNumber, r.PRNumber)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CostUSD > out[j].CostUSD })
	return out
}

// GetUsageSummaryByBoard returns per-board (team) usage aggregates since a given time.
func (s *TokenUsageStore) GetUsageSummaryByBoard(ctx context.Context, since string) ([]BoardUsageSummary, error) {
	rel, args := usageRelation(ctx, s.db.Reader(), since)
	query := `SELECT COALESCE(NULLIF(t.board_name, ''), ls.board_name, '') as board_name,
	          COALESCE(SUM(t.input_tokens), 0) as input_tokens,
	          COALESCE(SUM(t.output_tokens), 0) as output_tokens,
	          COALESCE(SUM(t.cache_read_tokens), 0) as cache_read_tokens,
	          COALESCE(SUM(t.cache_write_tokens), 0) as cache_write_tokens,
	          COALESCE(SUM(t.total_tokens), 0) as total_tokens,
	          COALESCE(SUM(t.cost_usd), 0) as cost_usd,
	          COUNT(DISTINCT t.session_id) as num_agents
	   FROM ` + rel + ` t LEFT JOIN live_sessions ls ON ls.session_id = t.session_id
	   WHERE ` + usageDedup("t.")
	query += ` GROUP BY COALESCE(NULLIF(t.board_name, ''), ls.board_name, '') ORDER BY cost_usd DESC`
	var summaries []BoardUsageSummary
	err := s.db.Reader().SelectContext(ctx, &summaries, query, args...)
	return summaries, err
}

// tokenUsageNow stamps a usage report that has no timestamp of its own (a var for tests).
var tokenUsageNow = NowUTC

func sinceWhere(since string) string {
	if since == "" {
		return ""
	}
	return " WHERE recorded_at >= ?"
}

func sinceArgs(since string) []interface{} {
	if since == "" {
		return nil
	}
	return []interface{}{since}
}
