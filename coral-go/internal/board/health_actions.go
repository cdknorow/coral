package board

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Coral's own health actions (#4175: review auto-delegation, the claim and status nudges).
// Every action goes through the limits here, in the store, out of any agent's reach
// (Security's #4176 rule 5): a cooldown per (rule, target), a cap per target per hour and one
// budget per team per hour. The escalate-after-N counters live here too, so a restart neither
// forgets an escalation nor nudges again.

// HealthLimits bound one rule's actions.
type HealthLimits struct {
	Cooldown         time.Duration // between two actions of this rule on the same target
	PerTargetPerHour int           // actions of any rule on the target, per hour
	TeamPerHour      int           // actions of any rule on this board, per hour
}

// Why a health action was refused.
const (
	HealthRefusedCooldown = "cooldown"
	HealthRefusedTarget   = "target cap"
	HealthRefusedBudget   = "team budget"
)

func (s *Store) ensureHealthSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS health_actions (
	project TEXT NOT NULL,
	rule    TEXT NOT NULL,
	target  TEXT NOT NULL,
	at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_health_actions ON health_actions(project, at);
CREATE TABLE IF NOT EXISTS health_counters (
	project   TEXT NOT NULL,
	rule      TEXT NOT NULL,
	target    TEXT NOT NULL,
	subject   TEXT NOT NULL,
	count     INTEGER NOT NULL,
	last_at   TEXT NOT NULL,
	escalated INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (project, rule, target, subject)
);
CREATE TABLE IF NOT EXISTS health_items (
	project     TEXT NOT NULL,
	item        TEXT NOT NULL,
	state       TEXT NOT NULL,
	shown_at    TEXT NOT NULL,
	acked_state TEXT,
	acked_by    TEXT,
	acked_at    TEXT,
	PRIMARY KEY (project, item)
);`)
	return err
}

const healthTimeFormat = "2006-01-02T15:04:05.000Z"

// TakeHealthAction records an action of rule on target at now if the limits allow it, in one
// transaction. It returns "" when the action may go ahead, else why it may not.
func (s *Store) TakeHealthAction(ctx context.Context, project, rule, target string, now time.Time, lim HealthLimits) (string, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	hourAgo := now.Add(-time.Hour).UTC().Format(healthTimeFormat)
	if lim.TeamPerHour > 0 {
		var n int
		if err := tx.GetContext(ctx, &n, `SELECT COUNT(*) FROM health_actions WHERE project = ? AND at > ? AND rule NOT LIKE '%-msg' AND rule != 'budget-reached'`, project, hourAgo); err != nil {
			return "", err
		}
		if n >= lim.TeamPerHour {
			return HealthRefusedBudget, nil
		}
	}
	if lim.Cooldown > 0 {
		var last sql.NullString
		if err := tx.GetContext(ctx, &last, `SELECT MAX(at) FROM health_actions WHERE project = ? AND rule = ? AND target = ?`, project, rule, target); err != nil {
			return "", err
		}
		if last.Valid && last.String > now.Add(-lim.Cooldown).UTC().Format(healthTimeFormat) {
			return HealthRefusedCooldown, nil
		}
	}
	if lim.PerTargetPerHour > 0 {
		var n int
		if err := tx.GetContext(ctx, &n, `SELECT COUNT(*) FROM health_actions WHERE project = ? AND target = ? AND at > ? AND rule NOT LIKE '%-msg'`, project, target, hourAgo); err != nil {
			return "", err
		}
		if n >= lim.PerTargetPerHour {
			return HealthRefusedTarget, nil
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO health_actions(project, rule, target, at) VALUES (?, ?, ?, ?)`,
		project, rule, target, now.UTC().Format(healthTimeFormat)); err != nil {
		return "", err
	}
	// A day is all the limits look at.
	if _, err := tx.ExecContext(ctx, `DELETE FROM health_actions WHERE project = ? AND at < ?`,
		project, now.Add(-24*time.Hour).UTC().Format(healthTimeFormat)); err != nil {
		return "", err
	}
	return "", tx.Commit()
}

// HealthCounter is how often a rule acted on one target about one subject (a task), and
// whether it has escalated.
type HealthCounter struct {
	Count     int    `db:"count"`
	Escalated bool   `db:"escalated"`
	LastAt    string `db:"last_at"` // when it last changed ("" when there is none)
}

// GetHealthCounter returns the counter, zero when there is none.
func (s *Store) GetHealthCounter(ctx context.Context, project, rule, target, subject string) (HealthCounter, error) {
	var c HealthCounter
	err := s.db.GetContext(ctx, &c, `SELECT count, escalated, last_at FROM health_counters WHERE project = ? AND rule = ? AND target = ? AND subject = ?`,
		project, rule, target, subject)
	if errors.Is(err, sql.ErrNoRows) {
		return HealthCounter{}, nil
	}
	return c, err
}

// BumpHealthCounter adds one to the counter and returns it.
func (s *Store) BumpHealthCounter(ctx context.Context, project, rule, target, subject string, now time.Time) (HealthCounter, error) {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO health_counters(project, rule, target, subject, count, last_at) VALUES (?, ?, ?, ?, 1, ?)
		ON CONFLICT(project, rule, target, subject) DO UPDATE SET count = count + 1, last_at = excluded.last_at`,
		project, rule, target, subject, now.UTC().Format(healthTimeFormat)); err != nil {
		return HealthCounter{}, err
	}
	return s.GetHealthCounter(ctx, project, rule, target, subject)
}

// MarkHealthEscalated records that the rule escalated for target and subject: it acts on them
// no more until ResetHealthCounter.
func (s *Store) MarkHealthEscalated(ctx context.Context, project, rule, target, subject string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO health_counters(project, rule, target, subject, count, last_at, escalated) VALUES (?, ?, ?, ?, 0, ?, 1)
		ON CONFLICT(project, rule, target, subject) DO UPDATE SET escalated = 1, last_at = excluded.last_at`,
		project, rule, target, subject, now.UTC().Format(healthTimeFormat))
	return err
}

// ResetHealthCounter forgets the counter (the state it counted is over: the task was claimed,
// the agent became active).
func (s *Store) ResetHealthCounter(ctx context.Context, project, rule, target, subject string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM health_counters WHERE project = ? AND rule = ? AND target = ? AND subject = ?`,
		project, rule, target, subject)
	return err
}

// ResetHealthCounters forgets every counter of a rule for a target (all its tasks).
func (s *Store) ResetHealthCounters(ctx context.Context, project, rule, target string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM health_counters WHERE project = ? AND rule = ? AND target = ?`, project, rule, target)
	return err
}

// HealthEscalations lists the escalated counters of a board, for the orchestrator's check.
func (s *Store) HealthEscalations(ctx context.Context, project string) ([]HealthEscalation, error) {
	var out []HealthEscalation
	err := s.db.SelectContext(ctx, &out, `SELECT rule, target, subject, last_at FROM health_counters WHERE project = ? AND escalated = 1 ORDER BY last_at, rowid`, project)
	return out, err
}

// HealthEscalation is one escalated counter.
type HealthEscalation struct {
	Rule    string `db:"rule"`
	Target  string `db:"target"`
	Subject string `db:"subject"`
	At      string `db:"last_at"`
}

// TaskLastActivity is when a task last showed activity on the board: its last post, peek or
// task change (last_activity_at), else when it was claimed. Zero when it has neither.
func (s *Store) TaskLastActivity(ctx context.Context, project string, id int64) (time.Time, error) {
	var v sql.NullString
	if err := s.reader().GetContext(ctx, &v, `SELECT COALESCE(last_activity_at, claimed_at) FROM board_tasks WHERE id = ? AND board_id = ?`, id, project); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	if !v.Valid {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000000+00:00", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, v.String); err == nil {
			return t, nil
		}
	}
	return time.Time{}, nil
}

// HealthActionTaken reports whether rule has ever acted on target (within the day the store
// keeps): a once-only action checks it before acting.
func (s *Store) HealthActionTaken(ctx context.Context, project, rule, target string) (bool, error) {
	var n int
	err := s.db.GetContext(ctx, &n, `SELECT COUNT(*) FROM health_actions WHERE project = ? AND rule = ? AND target = ?`, project, rule, target)
	return n > 0, err
}

// RecordHealthAction records an action outside the limits (a marker, such as a message that
// was already the reason for a reminder).
func (s *Store) RecordHealthAction(ctx context.Context, project, rule, target string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO health_actions(project, rule, target, at) VALUES (?, ?, ?, ?)`,
		project, rule, target, now.UTC().Format(healthTimeFormat))
	return err
}
