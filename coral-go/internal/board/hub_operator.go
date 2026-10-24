package board

import (
	"context"
	"strings"
	"time"
)

// HubOperatorAction records one use of the Operator identity by a Coral Hub user through
// the tunnel (#3404): a chat post, a cross-team post or a task action. HubUser is the
// validated, tunnel-stamped X-Coral-Hub-User, never a name from the request body.
type HubOperatorAction struct {
	ID      int64  `db:"id" json:"id"`
	At      string `db:"at" json:"at"`
	HubUser string `db:"hub_user" json:"hub_user"`
	// HubUserID is the person's stable Coral Hub id (hub-uid-<32 hex>) when the machine is a team's and the stamp carried one (#7061): the label in HubUser can be renamed or reused, the id is
	// not. "" for a personal machine's actions, and for everything recorded before this.
	HubUserID string `db:"hub_user_id" json:"hub_user_id,omitempty"`
	Method    string `db:"method" json:"method"`
	Path      string `db:"path" json:"path"`
	Project   string `db:"project" json:"project"`
	Status    int    `db:"status" json:"status"`
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
	if err != nil {
		return err
	}
	// The person's stable id (#7061): added to a table made before it existed; a repeat is the "duplicate column" answer.
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE hub_operator_actions ADD COLUMN hub_user_id TEXT NOT NULL DEFAULT ''`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return err
	}
	return nil
}

// RecordHubOperatorAction stores a, keeping the newest hubOperatorActionsKept.
func (s *Store) RecordHubOperatorAction(ctx context.Context, a HubOperatorAction) error {
	if a.At == "" {
		a.At = time.Now().UTC().Format(time.RFC3339)
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO hub_operator_actions (at, hub_user, hub_user_id, method, path, project, status) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		a.At, a.HubUser, a.HubUserID, a.Method, a.Path, a.Project, a.Status)
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
		`SELECT id, at, hub_user, hub_user_id, method, path, project, status FROM hub_operator_actions ORDER BY id DESC LIMIT ?`, limit)
	return out, err
}
