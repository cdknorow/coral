package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/jmoiron/sqlx"
)

// SessionOrderSetting is the saved order of the agents within a group of the agent list: a JSON list of session ids, written by the
// dashboard when an agent is moved up or down. Ids it does not list sort after the listed ones.
const SessionOrderSetting = "session_order"

// A restart or resume replaces a session with a new one that has a new id, and a new session is created last. Two things of the agent
// list follow the agent's id, and both are carried to the new id (#4943), so the restarted agent stays where it was:
//
//   - its place among the agents, which is the order the server serves the list in: live_sessions.list_at, the time that place was
//     taken. A new session's is its created_at; a replacement keeps the one it replaces (created_at itself is the run's start, which
//     the "agent never started" check and the run's duration read, so it is not carried). A team's place among the teams is the
//     earliest of its agents' places.
//   - its place in the saved order (session_order), the order of the agents within a group.

// carrySessionOrderTx puts newID where oldID is in the saved order. Nothing changes when oldID is not listed or the saved order is not
// a JSON list of strings (it is the user's data: it is not rewritten).
func carrySessionOrderTx(ctx context.Context, tx *sqlx.Tx, oldID, newID string) error {
	if oldID == "" || newID == "" || oldID == newID {
		return nil
	}
	var raw string
	switch err := tx.GetContext(ctx, &raw, "SELECT value FROM user_settings WHERE key = ?", SessionOrderSetting); {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	}
	var saved []string
	if json.Unmarshal([]byte(raw), &saved) != nil {
		return nil
	}
	changed := false
	for i, id := range saved {
		if id == oldID {
			saved[i] = newID
			changed = true
		}
	}
	if !changed {
		return nil
	}
	out, err := json.Marshal(saved)
	if err != nil {
		return nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE user_settings SET value = ? WHERE key = ?`, string(out), SessionOrderSetting)
	return err
}

// LiveListAt maps each live session's id to the time its place in the agent list was taken (list_at, else created_at). The list is
// served in this order.
func (s *SessionStore) LiveListAt(ctx context.Context) (map[string]string, error) {
	var rows []struct {
		ID string `db:"session_id"`
		At string `db:"at"`
	}
	if err := s.reader().SelectContext(ctx, &rows, "SELECT session_id, COALESCE(list_at, created_at) AS at FROM live_sessions WHERE status = 'active'"); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.ID] = r.At
	}
	return out, nil
}
