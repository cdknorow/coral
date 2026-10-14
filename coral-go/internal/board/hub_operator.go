package board

import (
	"context"
	"time"
)

// HubOperatorAction records one use of the Operator identity by a Coral Hub user through
// the tunnel (#3404): a chat post, a cross-team post or a task action. HubUser is the
// validated, tunnel-stamped X-Coral-Hub-User, never a name from the request body.
type HubOperatorAction struct {
	ID      int64  `db:"id" json:"id"`
	At      string `db:"at" json:"at"`
	HubUser string `db:"hub_user" json:"hub_user"`
	Method  string `db:"method" json:"method"`
	Path    string `db:"path" json:"path"`
	Project string `db:"project" json:"project"`
	Status  int    `db:"status" json:"status"`
}

// hubOperatorActionsKept bounds the table: the newest actions are kept.
const hubOperatorActionsKept = 1000

func (s *Store) ensureHubOperatorActionsSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS hub_operator_actions (
		id       INTEGER PRIMARY KEY AUTOINCREMENT,
		at       TEXT NOT NULL,
		hub_user TEXT NOT NULL,
		method   TEXT NOT NULL,
		path     TEXT NOT NULL,
		project  TEXT NOT NULL,
		status   INTEGER NOT NULL
	)`)
	return err
}

// RecordHubOperatorAction stores a, keeping the newest hubOperatorActionsKept.
func (s *Store) RecordHubOperatorAction(ctx context.Context, a HubOperatorAction) error {
	if a.At == "" {
		a.At = time.Now().UTC().Format(time.RFC3339)
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO hub_operator_actions (at, hub_user, method, path, project, status) VALUES (?, ?, ?, ?, ?, ?)`,
		a.At, a.HubUser, a.Method, a.Path, a.Project, a.Status)
	if err != nil {
		return err
	}
	if id, err := res.LastInsertId(); err == nil && id > hubOperatorActionsKept {
		_, err = s.db.ExecContext(ctx, `DELETE FROM hub_operator_actions WHERE id <= ?`, id-hubOperatorActionsKept)
		return err
	}
	return nil
}

// RecentHubOperatorActions returns up to limit actions, newest first.
func (s *Store) RecentHubOperatorActions(ctx context.Context, limit int) ([]HubOperatorAction, error) {
	out := []HubOperatorAction{}
	err := s.db.SelectContext(ctx, &out,
		`SELECT id, at, hub_user, method, path, project, status FROM hub_operator_actions ORDER BY id DESC LIMIT ?`, limit)
	return out, err
}
