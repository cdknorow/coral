package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

var ErrUIStale = errors.New("panel missing or revision has changed")

type AgentUIPanel struct {
	SessionID string `db:"session_id" json:"session_id"`
	ID        string `db:"id" json:"id"`
	Title     string `db:"title" json:"title"`
	HTML      string `db:"html" json:"-"`
	Revision  int64  `db:"revision" json:"revision"`
	UpdatedAt string `db:"updated_at" json:"updated_at"`
}
type AgentUIEvent struct {
	ID        int64  `db:"id" json:"id"`
	Revision  int64  `db:"revision" json:"revision"`
	Action    string `db:"action" json:"action"`
	Payload   string `db:"payload" json:"-"`
	CreatedAt string `db:"created_at" json:"created_at"`
}

func (e AgentUIEvent) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID        int64           `json:"id"`
		Revision  int64           `json:"revision"`
		Action    string          `json:"action"`
		Payload   json.RawMessage `json:"payload"`
		CreatedAt string          `json:"created_at"`
	}{e.ID, e.Revision, e.Action, json.RawMessage(e.Payload), e.CreatedAt})
}
func (d *DB) ListAgentUI(ctx context.Context, sid string) ([]AgentUIPanel, error) {
	panels := []AgentUIPanel{}
	err := d.SelectContext(ctx, &panels, `SELECT session_id,id,title,revision,updated_at FROM agent_ui_panels WHERE session_id=? ORDER BY updated_at DESC,id`, sid)
	return panels, err
}
func (d *DB) GetAgentUI(ctx context.Context, sid, id string) (*AgentUIPanel, error) {
	var p AgentUIPanel
	err := d.GetContext(ctx, &p, `SELECT * FROM agent_ui_panels WHERE session_id=? AND id=?`, sid, id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &p, err
}
func (d *DB) PutAgentUI(ctx context.Context, sid, id, title, html string) (*AgentUIPanel, error) {
	var p AgentUIPanel
	err := d.GetContext(ctx, &p, `INSERT INTO agent_ui_panels(session_id,id,title,html,revision,updated_at) VALUES(?,?,?,?,1,?) ON CONFLICT(session_id,id) DO UPDATE SET title=excluded.title,html=excluded.html,revision=revision+1,updated_at=excluded.updated_at RETURNING *`, sid, id, title, html, nowUTC())
	return &p, err
}
func (d *DB) DeleteAgentUI(ctx context.Context, sid, id string) error {
	_, err := d.ExecContext(ctx, `DELETE FROM agent_ui_panels WHERE session_id=? AND id=?`, sid, id)
	return err
}
func (d *DB) AddAgentUIEvent(ctx context.Context, sid, id string, revision int64, action string, payload string) (int64, error) {
	res, err := d.ExecContext(ctx, `INSERT INTO agent_ui_events(session_id,panel_id,revision,action,payload,created_at) SELECT session_id,id,revision,?,?,? FROM agent_ui_panels WHERE session_id=? AND id=? AND revision=?`, action, payload, nowUTC(), sid, id, revision)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, ErrUIStale
	}
	return res.LastInsertId()
}
func (d *DB) AgentUIEvents(ctx context.Context, sid, id string, after int64) ([]AgentUIEvent, error) {
	events := []AgentUIEvent{}
	err := d.SelectContext(ctx, &events, `SELECT id,revision,action,payload,created_at FROM agent_ui_events WHERE session_id=? AND panel_id=? AND id>? ORDER BY id LIMIT 100`, sid, id, after)
	return events, err
}
