package store

import (
	"context"
	"fmt"
)

// token_usage holds one row per API call and is never pruned, so summing it
// for every live session on every dashboard tick grows without bound.
// token_usage_rollup keeps one row per (session_id, source) with the running
// sums, the maxima the per-session reads report and the context size of the
// newest turn. Triggers keep it in step with every write to token_usage,
// including writes by an older binary, so no Go write path has to remember it.
//
// The per-session reads apply the same source rule as sourceDedup: when a
// session has any jsonl rows only those count, otherwise every row does.
var tokenUsageRollupSQL = `
CREATE TABLE IF NOT EXISTS token_usage_rollup (
	session_id         TEXT NOT NULL,
	source             TEXT NOT NULL,
	input_tokens       INTEGER NOT NULL,
	output_tokens      INTEGER NOT NULL,
	cache_read_tokens  INTEGER NOT NULL,
	cache_write_tokens INTEGER NOT NULL,
	total_tokens       INTEGER NOT NULL,
	cost_usd           REAL NOT NULL,
	num_turns          INTEGER NOT NULL,
	max_id             INTEGER,
	agent_name         TEXT,
	agent_type         TEXT,
	team_id            INTEGER,
	board_name         TEXT,
	recorded_at        TEXT,
	latest_context     INTEGER NOT NULL,
	PRIMARY KEY (session_id, source)
);

CREATE TRIGGER IF NOT EXISTS token_usage_rollup_insert AFTER INSERT ON token_usage BEGIN
	INSERT INTO token_usage_rollup VALUES (
		NEW.session_id, COALESCE(NEW.source, 'jsonl'),
		NEW.input_tokens, NEW.output_tokens, NEW.cache_read_tokens, NEW.cache_write_tokens,
		NEW.total_tokens, NEW.cost_usd, 1, NEW.id, NEW.agent_name, NEW.agent_type,
		NEW.team_id, NEW.board_name, NEW.recorded_at,
		NEW.input_tokens + NEW.cache_read_tokens + NEW.cache_write_tokens)
	ON CONFLICT (session_id, source) DO UPDATE SET
		input_tokens       = input_tokens + excluded.input_tokens,
		output_tokens      = output_tokens + excluded.output_tokens,
		cache_read_tokens  = cache_read_tokens + excluded.cache_read_tokens,
		cache_write_tokens = cache_write_tokens + excluded.cache_write_tokens,
		total_tokens       = total_tokens + excluded.total_tokens,
		cost_usd           = cost_usd + excluded.cost_usd,
		num_turns          = num_turns + 1,
		max_id             = CASE WHEN max_id IS NULL OR excluded.max_id > max_id THEN excluded.max_id ELSE max_id END,
		agent_name         = CASE WHEN agent_name IS NULL OR excluded.agent_name > agent_name THEN excluded.agent_name ELSE agent_name END,
		agent_type         = CASE WHEN agent_type IS NULL OR excluded.agent_type > agent_type THEN excluded.agent_type ELSE agent_type END,
		team_id            = CASE WHEN team_id IS NULL OR excluded.team_id > team_id THEN excluded.team_id ELSE team_id END,
		board_name         = CASE WHEN board_name IS NULL OR excluded.board_name > board_name THEN excluded.board_name ELSE board_name END,
		latest_context     = CASE WHEN recorded_at IS NULL OR excluded.recorded_at > recorded_at THEN excluded.latest_context ELSE latest_context END,
		recorded_at        = CASE WHEN recorded_at IS NULL OR excluded.recorded_at > recorded_at THEN excluded.recorded_at ELSE recorded_at END;
END;

-- Repricing (RepriceZeroCosts) changes only cost_usd: apply the difference.
CREATE TRIGGER IF NOT EXISTS token_usage_rollup_update_delta AFTER UPDATE ON token_usage
WHEN NEW.session_id IS OLD.session_id AND COALESCE(NEW.source, 'jsonl') IS COALESCE(OLD.source, 'jsonl')
 AND NEW.id IS OLD.id AND NEW.recorded_at IS OLD.recorded_at AND NEW.agent_name IS OLD.agent_name
 AND NEW.agent_type IS OLD.agent_type AND NEW.team_id IS OLD.team_id AND NEW.board_name IS OLD.board_name
BEGIN
	UPDATE token_usage_rollup SET
		input_tokens       = input_tokens + NEW.input_tokens - OLD.input_tokens,
		output_tokens      = output_tokens + NEW.output_tokens - OLD.output_tokens,
		cache_read_tokens  = cache_read_tokens + NEW.cache_read_tokens - OLD.cache_read_tokens,
		cache_write_tokens = cache_write_tokens + NEW.cache_write_tokens - OLD.cache_write_tokens,
		total_tokens       = total_tokens + NEW.total_tokens - OLD.total_tokens,
		cost_usd           = cost_usd + NEW.cost_usd - OLD.cost_usd,
		latest_context     = CASE WHEN recorded_at = NEW.recorded_at
		                          THEN NEW.input_tokens + NEW.cache_read_tokens + NEW.cache_write_tokens
		                          ELSE latest_context END
	WHERE session_id = NEW.session_id AND source = COALESCE(NEW.source, 'jsonl');
END;

-- Any other update, and deletes, rebuild the affected rollup rows.
CREATE TRIGGER IF NOT EXISTS token_usage_rollup_update_rebuild AFTER UPDATE ON token_usage
WHEN NOT (NEW.session_id IS OLD.session_id AND COALESCE(NEW.source, 'jsonl') IS COALESCE(OLD.source, 'jsonl')
 AND NEW.id IS OLD.id AND NEW.recorded_at IS OLD.recorded_at AND NEW.agent_name IS OLD.agent_name
 AND NEW.agent_type IS OLD.agent_type AND NEW.team_id IS OLD.team_id AND NEW.board_name IS OLD.board_name)
BEGIN
	DELETE FROM token_usage_rollup WHERE session_id = OLD.session_id AND source = COALESCE(OLD.source, 'jsonl');
	DELETE FROM token_usage_rollup WHERE session_id = NEW.session_id AND source = COALESCE(NEW.source, 'jsonl');
	` + rebuildTokenUsageRollup(`session_id = OLD.session_id AND COALESCE(source, 'jsonl') = COALESCE(OLD.source, 'jsonl')`) + `;
	` + rebuildTokenUsageRollup(`session_id = NEW.session_id AND COALESCE(source, 'jsonl') = COALESCE(NEW.source, 'jsonl')
	  AND NOT (NEW.session_id IS OLD.session_id AND COALESCE(NEW.source, 'jsonl') IS COALESCE(OLD.source, 'jsonl'))`) + `;
END;

CREATE TRIGGER IF NOT EXISTS token_usage_rollup_delete AFTER DELETE ON token_usage BEGIN
	DELETE FROM token_usage_rollup WHERE session_id = OLD.session_id AND source = COALESCE(OLD.source, 'jsonl');
	` + rebuildTokenUsageRollup(`session_id = OLD.session_id AND COALESCE(source, 'jsonl') = COALESCE(OLD.source, 'jsonl')`) + `;
END;
`

// rebuildTokenUsageRollup recomputes the rollup rows for the token_usage rows
// matching where. The newest turn's context comes from the row at
// MAX(recorded_at), which (session_id, recorded_at) makes unique.
func rebuildTokenUsageRollup(where string) string {
	return `INSERT INTO token_usage_rollup
	SELECT g.*, (SELECT x.input_tokens + x.cache_read_tokens + x.cache_write_tokens FROM token_usage x
	             WHERE x.session_id = g.session_id AND x.recorded_at = g.recorded_at)
	FROM (SELECT session_id, COALESCE(source, 'jsonl') AS source,
	             SUM(input_tokens), SUM(output_tokens), SUM(cache_read_tokens), SUM(cache_write_tokens),
	             SUM(total_tokens), SUM(cost_usd), COUNT(*), MAX(id), MAX(agent_name), MAX(agent_type),
	             MAX(team_id), MAX(board_name), MAX(recorded_at) AS recorded_at
	      FROM token_usage WHERE ` + where + ` GROUP BY 1, 2) g`
}

// ensureTokenUsageRollup creates the rollup and its triggers and, the first
// time, fills it from the existing rows. Both happen in one write transaction
// so no insert can land between the backfill and the triggers.
func (d *DB) ensureTokenUsageRollup(ctx context.Context) error {
	tx, err := d.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existed int
	if err := tx.GetContext(ctx, &existed,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'token_usage_rollup'`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, tokenUsageRollupSQL); err != nil {
		return fmt.Errorf("create token usage rollup: %w", err)
	}
	if existed == 0 {
		if _, err := tx.ExecContext(ctx, rebuildTokenUsageRollup("1")); err != nil {
			return fmt.Errorf("backfill token usage rollup: %w", err)
		}
	}
	return tx.Commit()
}
