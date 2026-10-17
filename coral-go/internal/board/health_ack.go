package board

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Acknowledging an item of the orchestrator's check (#4175 step 3): the orchestrator marks it
// handled and it stays off the list until its state changes. Each item has a stable id (the
// check prints it) and a state (what it says now: when an escalation happened, since when an
// agent is idle, which reviews wait). The check records the state it showed; an ack copies that
// state, and the item is left out while its state is still the acknowledged one. Who may ack is
// the route's decision (Security's #4176 rule 19).

// ErrNoSuchHealthItem is an ack for an id the check has not shown on this board lately.
var ErrNoSuchHealthItem = errors.New("no such item in this board's recent health checks")

// healthItemKeep is how long an item shown once is remembered (and can be acknowledged).
const healthItemKeep = 7 * 24 * time.Hour

// NoteHealthItems records the state each item of a check has now (id -> state) and returns the
// ids whose state is the acknowledged one: those are left out of the check.
func (s *Store) NoteHealthItems(ctx context.Context, project string, items map[string]string, now time.Time) (map[string]bool, error) {
	acked := map[string]bool{}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	at := now.UTC().Format(healthTimeFormat)
	for id, state := range items {
		if _, err := tx.ExecContext(ctx, `INSERT INTO health_items (project, item, state, shown_at) VALUES (?, ?, ?, ?)
			ON CONFLICT(project, item) DO UPDATE SET state = excluded.state, shown_at = excluded.shown_at`, project, id, state, at); err != nil {
			return nil, err
		}
		var ackedState sql.NullString
		if err := tx.GetContext(ctx, &ackedState, `SELECT acked_state FROM health_items WHERE project = ? AND item = ?`, project, id); err != nil {
			return nil, err
		}
		acked[id] = ackedState.Valid && ackedState.String == state
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM health_items WHERE project = ? AND shown_at < ?`,
		project, now.Add(-healthItemKeep).UTC().Format(healthTimeFormat)); err != nil {
		return nil, err
	}
	return acked, tx.Commit()
}

// AckHealthItem marks an item handled in the state the check last showed it in.
func (s *Store) AckHealthItem(ctx context.Context, project, item, by string, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE health_items SET acked_state = state, acked_by = ?, acked_at = ?
		WHERE project = ? AND item = ? AND shown_at >= ?`, by, now.UTC().Format(healthTimeFormat),
		project, item, now.Add(-healthItemKeep).UTC().Format(healthTimeFormat))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoSuchHealthItem
	}
	return nil
}
