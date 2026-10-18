package store

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"time"
)

// The Cost view reads token usage by the hour (#4752). At 330,000 usage rows its summary took 1.3 s for the last day, 6.6 s for the last week
// and 4.9 s for all time, and its five requests used 12 s of CPU a minute while it was open: the floor was the scan of the raw rows (0.9 us a
// row, random reads), whatever the question. token_usage_hour_agg holds the raw rows summed by hour and by every column a Cost query groups by
// (session, agent, type, board, team, source, model): about 25 times fewer rows.
//
// It is derived data, kept exact rather than kept current. Triggers on token_usage mark an hour dirty (token_usage_hour_dirty) whenever a row
// of it is inserted, changed or deleted, by any writer of any version, and the refresh recomputes a dirty hour from the raw rows in one
// transaction (no running sums to drift) once the hour is over. An hour that holds a row whose recorded_at is not a UTC timestamp in the
// canonical layout is listed in token_usage_hour_odd and never aggregated. A Cost query reads the aggregate for the hours that are over, clean,
// not odd and entirely after the hour its window begins in, and the raw rows for every other hour (those still being written, the dirty
// ones, the odd ones, the hour the window begins in). So a query that finds the aggregate behind answers from more raw rows, never from a
// stale sum: it gives the numbers the raw rows give.

// canonicalUsageTimeOf is true for a recorded_at the aggregate can hold: a UTC timestamp with a T and a Z or +00:00 suffix. Anything else (another
// offset, a space for the T) is not: the hour of such a value is not its first 13 characters.
func canonicalUsageTimeOf(col string) string {
	return `(` + col + ` GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]*' AND (substr(` + col + `, -1) = 'Z' OR substr(` + col + `, -6) = '+00:00'))`
}

var tokenUsageHourAggSQL = `
CREATE TABLE IF NOT EXISTS token_usage_hour_agg (
	hour               TEXT NOT NULL,   -- the first 13 characters of recorded_at (2006-01-02T15), a UTC hour
	session_id         TEXT NOT NULL,
	agent_name         TEXT NOT NULL,
	agent_type         TEXT NOT NULL,
	team_id            INTEGER,
	board_name         TEXT,
	source             TEXT NOT NULL,
	model              TEXT,
	input_tokens       INTEGER NOT NULL,
	output_tokens      INTEGER NOT NULL,
	total_tokens       INTEGER NOT NULL,
	cache_read_tokens  INTEGER NOT NULL,
	cache_write_tokens INTEGER NOT NULL,
	cost_usd           REAL NOT NULL,
	n_rows             INTEGER NOT NULL,
	min_at             TEXT NOT NULL,   -- the earliest and the latest recorded_at it sums
	max_at             TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_token_usage_hour_agg_hour ON token_usage_hour_agg(hour);
CREATE TABLE IF NOT EXISTS token_usage_hour_dirty (hour TEXT PRIMARY KEY) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS token_usage_hour_odd (hour TEXT PRIMARY KEY) WITHOUT ROWID;

CREATE TRIGGER IF NOT EXISTS token_usage_hour_dirty_insert AFTER INSERT ON token_usage BEGIN
	INSERT OR IGNORE INTO token_usage_hour_dirty VALUES (substr(NEW.recorded_at, 1, 13));
END;
CREATE TRIGGER IF NOT EXISTS token_usage_hour_dirty_update AFTER UPDATE ON token_usage BEGIN
	INSERT OR IGNORE INTO token_usage_hour_dirty VALUES (substr(OLD.recorded_at, 1, 13));
	INSERT OR IGNORE INTO token_usage_hour_dirty VALUES (substr(NEW.recorded_at, 1, 13));
END;
CREATE TRIGGER IF NOT EXISTS token_usage_hour_dirty_delete AFTER DELETE ON token_usage BEGIN
	INSERT OR IGNORE INTO token_usage_hour_dirty VALUES (substr(OLD.recorded_at, 1, 13));
END;
CREATE TRIGGER IF NOT EXISTS token_usage_hour_odd_insert AFTER INSERT ON token_usage WHEN NOT ` + canonicalUsageTimeOf("NEW.recorded_at") + ` BEGIN
	INSERT OR IGNORE INTO token_usage_hour_odd VALUES (substr(NEW.recorded_at, 1, 13));
END;
CREATE TRIGGER IF NOT EXISTS token_usage_hour_odd_update AFTER UPDATE ON token_usage WHEN NOT ` + canonicalUsageTimeOf("NEW.recorded_at") + ` BEGIN
	INSERT OR IGNORE INTO token_usage_hour_odd VALUES (substr(NEW.recorded_at, 1, 13));
END;
`

// ensureTokenUsageHourAgg creates the aggregate, the lists and the triggers and, the first time, marks every hour of the raw rows dirty (so the
// refresh builds them) and lists the odd ones. One write transaction: no insert can land between the marking and the triggers.
func (d *DB) ensureTokenUsageHourAgg(ctx context.Context) error {
	tx, err := d.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existed int
	if err := tx.GetContext(ctx, &existed, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'token_usage_hour_agg'`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, tokenUsageHourAggSQL); err != nil {
		return fmt.Errorf("create token usage hour aggregate: %w", err)
	}
	if existed == 0 {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO token_usage_hour_dirty SELECT DISTINCT substr(recorded_at, 1, 13) FROM token_usage`); err != nil {
			return fmt.Errorf("mark token usage hours: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO token_usage_hour_odd SELECT DISTINCT substr(recorded_at, 1, 13) FROM token_usage WHERE NOT `+canonicalUsageTimeOf("recorded_at")); err != nil {
			return fmt.Errorf("list odd token usage hours: %w", err)
		}
	}
	return tx.Commit()
}

var refreshTokenUsageHourSQL = `
INSERT INTO token_usage_hour_agg
SELECT ?1, session_id, agent_name, agent_type, team_id, board_name, COALESCE(source, 'jsonl'), model,
       SUM(input_tokens), SUM(output_tokens), SUM(total_tokens), SUM(cache_read_tokens), SUM(cache_write_tokens), SUM(cost_usd),
       COUNT(*), MIN(recorded_at), MAX(recorded_at)
FROM token_usage
WHERE recorded_at >= ?1 AND recorded_at < ?2 AND ` + canonicalUsageTimeOf("recorded_at") + `
GROUP BY session_id, agent_name, agent_type, team_id, board_name, COALESCE(source, 'jsonl'), model`

// RefreshTokenUsageHourAgg recomputes the dirty hours that are over at now, oldest first, at most maxHours, each in its own transaction (the old
// aggregate rows of the hour go, the new ones come, the mark is cleared). It returns how many hours it did.
func (d *DB) RefreshTokenUsageHourAgg(ctx context.Context, now time.Time, maxHours int) (int, error) {
	current := now.UTC().Format("2006-01-02T15") // the hour still being written is never aggregated
	done := 0
	for done < maxHours && ctx.Err() == nil {
		var hour string
		if err := d.GetContext(ctx, &hour, `SELECT COALESCE(MIN(hour), '') FROM token_usage_hour_dirty WHERE hour < ?`, current); err != nil {
			return done, err
		}
		if hour == "" {
			return done, nil
		}
		if err := d.refreshTokenUsageHour(ctx, hour); err != nil {
			return done, err
		}
		done++
	}
	return done, ctx.Err()
}

func (d *DB) refreshTokenUsageHour(ctx context.Context, hour string) error {
	tx, err := d.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM token_usage_hour_agg WHERE hour = ?`, hour); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, refreshTokenUsageHourSQL, hour, hour+"~"); err != nil { // "~" sorts after every character of a timestamp (see tokenHourBounds)
		return fmt.Errorf("aggregate token usage for %s: %w", hour, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM token_usage_hour_dirty WHERE hour = ?`, hour); err != nil {
		return err
	}
	return tx.Commit()
}

// canonicalSince is a since the aggregate can answer: a UTC RFC 3339 time as the dashboard writes it (T, Z or +00:00). The raw rows are
// compared with since as text, so only a since in the rows' own layout has a hour that means the same in text and in time.
var canonicalSince = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|\+00:00)$`)

// usageCols are the columns of token_usage_all the Cost queries use.
const usageCols = `session_id, agent_name, agent_type, team_id, board_name, input_tokens, output_tokens, total_tokens, cost_usd, cache_read_tokens, cache_write_tokens, COALESCE(source, 'jsonl') AS source, model`

// usageRelation is the rows a Cost query reads, as a derived table with token_usage_all's columns that those queries use, and min_at and
// max_at (the earliest and latest recorded_at a row stands for). since "" is no lower bound. It gives:
//
//   - the aggregate, for the clean, non-odd hours from the one after the hour since falls in, up to the last aggregated hour;
//   - the raw rows for the rest: those from since on in the hours before that, those of the dirty and the odd hours inside, and those after the last aggregated hour;
//   - the rolled-up hourly rows (token_usage_hourly) from since on.
//
// A since that is not a canonical UTC time, and a database with nothing aggregated yet, get the raw and hourly rows only, as before.
func usageRelation(ctx context.Context, db sqlGetter, since string) (frag string, args []any) {
	raw := `SELECT ` + usageCols + `, 1 AS n_rows, recorded_at, recorded_at AS min_at, recorded_at AS max_at FROM token_usage`
	hourly := `SELECT ` + usageCols + `, n_rows, recorded_at, recorded_at AS min_at, recorded_at AS max_at FROM token_usage_hourly`
	plain := func() (string, []any) {
		if since == "" {
			return `(` + raw + ` UNION ALL ` + hourly + `)`, nil
		}
		return `(` + raw + ` WHERE recorded_at >= ? UNION ALL ` + hourly + ` WHERE recorded_at >= ?)`, []any{since, since}
	}
	var last sql.NullString
	if !tokenUsageHourAggOn || (since != "" && !canonicalSince.MatchString(since)) {
		return plain()
	}
	if err := db.GetContext(ctx, &last, `SELECT MAX(hour) FROM token_usage_hour_agg`); err != nil || !last.Valid {
		return plain()
	}
	from := "" // the first aggregated hour a query may use
	if since != "" {
		t, err := time.Parse(time.RFC3339Nano, since)
		if err != nil {
			return plain()
		}
		from = t.UTC().Truncate(time.Hour).Add(time.Hour).Format("2006-01-02T15")
		if from > last.String { // the window ends before the aggregate's last hour begins: nothing of it to use
			return plain()
		}
	}
	skip := `(SELECT hour FROM token_usage_hour_dirty UNION SELECT hour FROM token_usage_hour_odd)`
	parts := []string{
		`SELECT session_id, agent_name, agent_type, team_id, board_name, input_tokens, output_tokens, total_tokens, cost_usd, cache_read_tokens, cache_write_tokens, source, model, n_rows, hour || ':00:00.000Z' AS recorded_at, min_at, max_at
		 FROM token_usage_hour_agg WHERE hour >= ? AND hour <= ? AND hour NOT IN ` + skip,
	}
	args = append(args, from, last.String)
	if since != "" { // the raw rows of the hours before the aggregate's, from since on
		parts = append(parts, raw+` WHERE recorded_at >= ? AND recorded_at < ?`)
		args = append(args, since, from)
	}
	// the raw rows of the dirty and odd hours inside the aggregate's range
	parts = append(parts, `SELECT `+usageCols+`, 1 AS n_rows, r.recorded_at, r.recorded_at AS min_at, r.recorded_at AS max_at
		 FROM `+skip+` s JOIN token_usage r ON r.recorded_at >= s.hour AND r.recorded_at < s.hour || '~' WHERE s.hour >= ? AND s.hour <= ?`)
	args = append(args, from, last.String)
	// the raw rows after the last aggregated hour
	parts = append(parts, raw+` WHERE recorded_at >= ?`)
	args = append(args, last.String+"~")
	if since != "" {
		parts = append(parts, hourly+` WHERE recorded_at >= ?`)
		args = append(args, since)
	} else {
		parts = append(parts, hourly)
	}
	frag = `(`
	for i, p := range parts {
		if i > 0 {
			frag += ` UNION ALL `
		}
		frag += p
	}
	return frag + `)`, args
}

// tokenUsageHourAggOn is false only in the tests that compare the aggregate with the raw rows.
var tokenUsageHourAggOn = true

// sqlGetter is what usageRelation needs of the database.
type sqlGetter interface {
	GetContext(ctx context.Context, dest any, query string, args ...any) error
}
