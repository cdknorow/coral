package board

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The two per-team switches Coral Hub's owner may change through the dedicated routes (#5572), as board_settings keys.
const (
	SwitchMessaging = settingCrossTeamMessaging
	SwitchFiles     = settingCrossTeamArtifacts
)

// Who changed a switch: through Coral Hub (the owner's validated hub user) or on this computer.
const (
	SwitchViaHub   = "hub"
	SwitchViaLocal = "local"
)

// switchLogKept bounds the log per board: the newest rows are kept.
const switchLogKept = 200

func (s *Store) ensureSwitchLogSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS cross_team_switch_log (
		id      INTEGER PRIMARY KEY AUTOINCREMENT,
		project TEXT NOT NULL,
		key     TEXT NOT NULL,
		old     TEXT NOT NULL,
		new     TEXT NOT NULL,
		via     TEXT NOT NULL,
		who     TEXT NOT NULL,
		at      TEXT NOT NULL
	)`)
	if err != nil {
		return err
	}
	// The person's stable Coral Hub id beside the label (#7061): '' for a change made on this computer, on a personal machine, and for every row made before this. Added to a table made before it existed.
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE cross_team_switch_log ADD COLUMN who_id TEXT NOT NULL DEFAULT ''`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return err
	}
	_, err = s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_cross_team_switch_log ON cross_team_switch_log (project, key, id)`)
	return err
}

// SwitchChange is one change (or request to change) of a switch: ids and the two values, never text from a request.
type SwitchChange struct {
	Project string `json:"-"`
	Key     string `json:"key"`
	Old     bool   `json:"old"`
	New     bool   `json:"new"`
	Via     string `json:"via"`
	Who     string `json:"who"`
	// WhoID is the stable id of who changed it on a team's machine (#7061); kept in the record, never in an answer.
	WhoID string `json:"-"`
	At    string `json:"at"`
}

// SetSwitchByHub sets one switch of a board for Coral Hub's owner and records it, in ONE transaction with the Operator-actions row (hub user, method, path, board, status): when either
// record cannot be written, the switch is unchanged and the error says so (#5572, Security's condition 4). A request for the value the switch already has is recorded too (changed is
// false). The caller has checked who is asking.
func (s *Store) SetSwitchByHub(ctx context.Context, project, key string, on bool, hubUser, hubUserID, method, path string) (c SwitchChange, changed bool, err error) {
	if key != SwitchMessaging && key != SwitchFiles {
		return c, false, fmt.Errorf("not a switch: %q", key)
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return c, false, err
	}
	defer tx.Rollback()
	var raw sql.NullString
	if err := tx.GetContext(ctx, &raw, `SELECT value FROM board_settings WHERE project = ? AND key = ?`, project, key); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return c, false, err
	}
	old := raw.Valid && raw.String == "true"
	at := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO board_settings (project, key, value) VALUES (?, ?, ?)
		 ON CONFLICT (project, key) DO UPDATE SET value = excluded.value`, project, key, fmt.Sprint(on)); err != nil {
		return c, false, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO hub_operator_actions (at, hub_user, hub_user_id, method, path, project, status) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		at, hubUser, hubUserID, method, path, project, 200); err != nil {
		return c, false, err
	}
	c = SwitchChange{Project: project, Key: key, Old: old, New: on, Via: SwitchViaHub, Who: hubUser, WhoID: hubUserID, At: at}
	if err := insertSwitchLog(ctx, tx, c); err != nil {
		return c, false, err
	}
	if err := tx.Commit(); err != nil {
		return c, false, err
	}
	return c, old != on, nil
}

type switchExec interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func insertSwitchLog(ctx context.Context, x switchExec, c SwitchChange) error {
	res, err := x.ExecContext(ctx, `INSERT INTO cross_team_switch_log (project, key, old, new, via, who, who_id, at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Project, c.Key, fmt.Sprint(c.Old), fmt.Sprint(c.New), c.Via, c.Who, c.WhoID, c.At)
	if err != nil {
		return err
	}
	if id, err := res.LastInsertId(); err == nil && id > switchLogKept {
		_, err = x.ExecContext(ctx, `DELETE FROM cross_team_switch_log WHERE project = ? AND id NOT IN (SELECT id FROM cross_team_switch_log WHERE project = ? ORDER BY id DESC LIMIT ?)`,
			c.Project, c.Project, switchLogKept)
		return err
	}
	return nil
}

// NoteSwitchChangeLocal records a change of a switch made on this computer, so "last changed by" never names a hub user after the owner here changed it again. A best-effort record
// after the change: nothing depends on it.
func (s *Store) NoteSwitchChangeLocal(ctx context.Context, project, key string, old, new bool) error {
	return insertSwitchLog(ctx, s.db, SwitchChange{Project: project, Key: key, Old: old, New: new, Via: SwitchViaLocal, At: time.Now().UTC().Format(time.RFC3339)})
}

// LastSwitchChanges is the newest recorded change of each switch of a board, by key (a switch never changed is absent).
func (s *Store) LastSwitchChanges(ctx context.Context, project string) (map[string]SwitchChange, error) {
	rows, err := s.reader().QueryContext(ctx,
		`SELECT key, old, new, via, who, who_id, at FROM cross_team_switch_log WHERE project = ? AND id IN (SELECT MAX(id) FROM cross_team_switch_log WHERE project = ? GROUP BY key)`, project, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]SwitchChange{}
	for rows.Next() {
		var c SwitchChange
		var o, n string
		if err := rows.Scan(&c.Key, &o, &n, &c.Via, &c.Who, &c.WhoID, &c.At); err != nil {
			return nil, err
		}
		c.Old, c.New, c.Project = o == "true", n == "true", project
		out[c.Key] = c
	}
	return out, rows.Err()
}
