package store

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// ensureTokenUsageThinkingModel puts the model into the key of token_usage_thinking (#5120: the Cost view's thinking breakdown is by provider,
// model and level). A table made before has no model column: it is rebuilt, in one transaction, with every existing row kept under the empty model
// (the model of those calls was not kept: the view shows it as "Not reported"), so that nothing is lost, guessed or counted twice. A table that
// has the column is left alone, which is every table a new database creates.
func (d *DB) ensureTokenUsageThinkingModel(ctx context.Context) error {
	var n int
	if err := d.GetContext(ctx, &n, `SELECT COUNT(*) FROM pragma_table_info('token_usage_thinking') WHERE name = 'model'`); err != nil {
		return fmt.Errorf("token_usage_thinking: %w", err)
	}
	if n > 0 {
		return nil
	}
	return d.WithTx(ctx, func(tx *sqlx.Tx) error {
		for _, q := range []string{
			`CREATE TABLE token_usage_thinking_new (
				hour               TEXT NOT NULL,
				agent_type         TEXT NOT NULL,
				model              TEXT NOT NULL DEFAULT '',
				thinking           TEXT NOT NULL,
				input_tokens       INTEGER NOT NULL DEFAULT 0,
				output_tokens      INTEGER NOT NULL DEFAULT 0,
				cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
				cache_write_tokens INTEGER NOT NULL DEFAULT 0,
				total_tokens       INTEGER NOT NULL DEFAULT 0,
				thinking_tokens    INTEGER NOT NULL DEFAULT 0,
				cost_usd           REAL NOT NULL DEFAULT 0,
				n_calls            INTEGER NOT NULL DEFAULT 0,
				PRIMARY KEY (hour, agent_type, model, thinking)
			) WITHOUT ROWID`,
			`INSERT INTO token_usage_thinking_new
				(hour, agent_type, model, thinking, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, total_tokens, thinking_tokens, cost_usd, n_calls)
				SELECT hour, agent_type, '', thinking, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, total_tokens, thinking_tokens, cost_usd, n_calls
				FROM token_usage_thinking`,
			`DROP TABLE token_usage_thinking`,
			`ALTER TABLE token_usage_thinking_new RENAME TO token_usage_thinking`,
		} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return fmt.Errorf("token_usage_thinking: add the model to its key: %w", err)
			}
		}
		return nil
	})
}
