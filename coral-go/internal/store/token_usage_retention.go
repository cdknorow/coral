package store

import (
	"context"
	"fmt"
	"time"
)

// token_usage retention (#4163 part 2; the operator's option B on the #4142 proposal). Raw
// rows (one per API call) are kept for TokenUsageRawDays; older ones are rolled into one row
// per hour and kind of usage in token_usage_hourly, so a session's turn chart and a task's
// cost stay exact for a month and every total stays the same for good.
//
//   - token_usage_all is raw rows and hourly rows together, with n_rows (how many raw rows a
//     row stands for, 1 for a raw row) and ctx_tokens (the context of the newest raw row it
//     holds). Every reader of totals reads it; counts are SUM(n_rows).
//   - token_usage_rollup (the per-session running sums) does not change when raw rows are
//     rolled up: its rebuilds read token_usage_all, which holds the hourly rows by then. Its
//     delete trigger is off while token_usage_pruning holds a row, which spares a rebuild of
//     the session's sums for every raw row deleted.
//   - A raw row for a session and hour already rolled up is ignored (a trigger, so writers of
//     any version are covered): re-reading an old transcript never counts it twice.

// TokenUsageRawDays is how long raw token usage rows are kept before they are rolled up.
const TokenUsageRawDays = 30

const tokenUsageHourlySQL = `
CREATE TABLE IF NOT EXISTS token_usage_hourly (
	hour               TEXT NOT NULL, -- the first 13 characters of the rows' recorded_at
	id                 INTEGER,       -- the newest raw row's id
	session_id         TEXT NOT NULL,
	agent_name         TEXT NOT NULL,
	agent_type         TEXT NOT NULL,
	team_id            INTEGER,
	board_name         TEXT,
	input_tokens       INTEGER NOT NULL,
	output_tokens      INTEGER NOT NULL,
	total_tokens       INTEGER NOT NULL,
	cost_usd           REAL NOT NULL,
	num_turns          INTEGER NOT NULL,
	recorded_at        TEXT NOT NULL, -- the newest raw row's
	cache_read_tokens  INTEGER NOT NULL,
	cache_write_tokens INTEGER NOT NULL,
	session_start_at   TEXT,
	last_activity_at   TEXT,
	source             TEXT NOT NULL,
	model              TEXT,
	n_rows             INTEGER NOT NULL,
	ctx_tokens         INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_token_usage_hourly_session ON token_usage_hourly(session_id, hour);
CREATE INDEX IF NOT EXISTS idx_token_usage_hourly_team ON token_usage_hourly(team_id);
CREATE INDEX IF NOT EXISTS idx_token_usage_hourly_time ON token_usage_hourly(recorded_at);
CREATE INDEX IF NOT EXISTS idx_token_usage_hourly_source ON token_usage_hourly(source, session_id);
CREATE TABLE IF NOT EXISTS token_usage_pruning (on_since TEXT);

CREATE VIEW IF NOT EXISTS token_usage_all AS
	SELECT id, session_id, agent_name, agent_type, team_id, board_name, input_tokens, output_tokens,
	       total_tokens, cost_usd, num_turns, recorded_at, cache_read_tokens, cache_write_tokens,
	       session_start_at, last_activity_at, source, model, 1 AS n_rows,
	       input_tokens + cache_read_tokens + cache_write_tokens AS ctx_tokens
	FROM token_usage
	UNION ALL
	SELECT id, session_id, agent_name, agent_type, team_id, board_name, input_tokens, output_tokens,
	       total_tokens, cost_usd, num_turns, recorded_at, cache_read_tokens, cache_write_tokens,
	       session_start_at, last_activity_at, source, model, n_rows, ctx_tokens
	FROM token_usage_hourly;

CREATE TRIGGER IF NOT EXISTS token_usage_skip_rolled_up BEFORE INSERT ON token_usage
WHEN EXISTS (SELECT 1 FROM token_usage_hourly h WHERE h.session_id = NEW.session_id AND h.hour = substr(NEW.recorded_at, 1, 13))
BEGIN
	SELECT RAISE(IGNORE);
END;
`

// rollupTokenUsageHourSQL rolls the raw rows of one hour (?1 = its prefix, ?2 and ?3 = the
// recorded_at range) into token_usage_hourly.
const rollupTokenUsageHourSQL = `
INSERT INTO token_usage_hourly
SELECT ?1, g.id, g.session_id, g.agent_name, g.agent_type, g.team_id, g.board_name,
       g.input_tokens, g.output_tokens, g.total_tokens, g.cost_usd, g.num_turns, g.recorded_at,
       g.cache_read_tokens, g.cache_write_tokens, g.session_start_at, g.last_activity_at,
       g.source, g.model, g.n_rows,
       COALESCE((SELECT x.input_tokens + x.cache_read_tokens + x.cache_write_tokens FROM token_usage x
                 WHERE x.session_id = g.session_id AND x.recorded_at = g.recorded_at LIMIT 1), 0)
FROM (SELECT MAX(id) AS id, session_id, agent_name, agent_type, team_id, board_name,
             SUM(input_tokens) AS input_tokens, SUM(output_tokens) AS output_tokens,
             SUM(total_tokens) AS total_tokens, SUM(cost_usd) AS cost_usd, SUM(num_turns) AS num_turns,
             MAX(recorded_at) AS recorded_at, SUM(cache_read_tokens) AS cache_read_tokens,
             SUM(cache_write_tokens) AS cache_write_tokens,
             MIN(NULLIF(session_start_at, '')) AS session_start_at,
             MAX(NULLIF(last_activity_at, '')) AS last_activity_at,
             COALESCE(source, 'jsonl') AS source, model, COUNT(*) AS n_rows
      FROM token_usage WHERE recorded_at >= ?2 AND recorded_at < ?3
      GROUP BY session_id, COALESCE(source, 'jsonl'), model, agent_name, agent_type, team_id, board_name) g`

// tokenHourBounds is the recorded_at range of the hour with prefix p ("2006-01-02T15", or with
// a space for the T), or ok false for a prefix that is not an hour.
func tokenHourBounds(p string) (from, to string, ok bool) {
	if len(p) != 13 || (p[10] != 'T' && p[10] != ' ') {
		return "", "", false
	}
	layout := "2006-01-02T15"
	if p[10] == ' ' {
		layout = "2006-01-02 15"
	}
	t, err := time.Parse(layout, p)
	if err != nil {
		return "", "", false
	}
	return p, t.Add(time.Hour).Format(layout), true
}

// RollupTokenUsage rolls raw token usage from hours that ended before cutoff into hourly rows,
// oldest hour first, at most maxHours, one transaction per hour (the raw rows are summed and
// deleted together). It reports how many hours and raw rows it rolled up. An hour whose rows'
// recorded_at is not a timestamp is left as it is: this is the user's usage history.
func (d *DB) RollupTokenUsage(ctx context.Context, cutoff time.Time, maxHours int) (hours int, rows int64, err error) {
	end := cutoff.UTC().Format("2006-01-02T15") // the hour holding cutoff is not complete
	after := ""                                 // hours at or before this were skipped as unreadable
	for hours < maxHours && ctx.Err() == nil {
		var first string
		if err := d.GetContext(ctx, &first,
			`SELECT COALESCE(MIN(substr(recorded_at, 1, 13)), '') FROM token_usage WHERE recorded_at < ? AND substr(recorded_at, 1, 13) > ?`,
			end, after); err != nil {
			return hours, rows, err
		}
		if first == "" || first >= end {
			return hours, rows, nil
		}
		from, to, ok := tokenHourBounds(first)
		if !ok {
			after = first
			continue
		}
		n, err := d.rollupTokenUsageHour(ctx, first, from, to)
		if err != nil {
			return hours, rows, err
		}
		hours++
		rows += n
	}
	return hours, rows, ctx.Err()
}

func (d *DB) rollupTokenUsageHour(ctx context.Context, hour, from, to string) (int64, error) {
	tx, err := d.BeginTxx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// While this row exists the per-session rollup's delete trigger is off: its sums already
	// count these rows, and a rebuild per deleted row would only cost time.
	if _, err := tx.ExecContext(ctx, `INSERT INTO token_usage_pruning VALUES (?)`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, rollupTokenUsageHourSQL, hour, from, to); err != nil {
		return 0, fmt.Errorf("roll up token usage for %s: %w", hour, err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM token_usage WHERE recorded_at >= ? AND recorded_at < ?`, from, to)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM token_usage_pruning`); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
