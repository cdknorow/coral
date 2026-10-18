package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

var ErrUIStale = errors.New("panel missing or revision has changed")

// The bounds on what one session's panels may hold (#4602, M8): an agent that publishes panels in a loop
// otherwise grows the database without end, and the dashboard reads every panel of the session.
const (
	MaxAgentUIPanels    = 20       // panels per session
	MaxAgentUIHTMLBytes = 32 << 20 // HTML bytes in all of a session's panels
	MaxAgentUIEvents    = 1000     // events kept per panel: the newest
)

var (
	// ErrUIPanelLimit is a new panel past MaxAgentUIPanels (updating one that exists is always allowed).
	ErrUIPanelLimit = errors.New("a session can have at most 20 panels: remove one first")
	// ErrUIHTMLLimit is HTML that would take the session's panels past MaxAgentUIHTMLBytes.
	ErrUIHTMLLimit = errors.New("a session's panels can hold at most 32 MiB of HTML in all: remove one or make this one smaller")
)

type AgentUIPanel struct {
	SessionID  string `db:"session_id" json:"session_id"`
	ID         string `db:"id" json:"id"`
	Title      string `db:"title" json:"title"`
	HTML       string `db:"html" json:"-"`
	Revision   int64  `db:"revision" json:"revision"`
	UpdatedAt  string `db:"updated_at" json:"updated_at"`
	EventCount int64  `db:"-" json:"event_count"`
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
	err := d.Reader().SelectContext(ctx, &panels, `SELECT session_id,id,title,revision,updated_at FROM agent_ui_panels WHERE session_id=? ORDER BY updated_at DESC,id`, sid)
	if err != nil {
		return panels, err
	}
	for i := range panels {
		panels[i].EventCount, err = d.AgentUIEventCount(ctx, sid, panels[i].ID)
		if err != nil {
			return panels, err
		}
	}
	return panels, err
}

// AgentUIEventCount returns the number of persisted interactions for a panel.
// It intentionally includes events from older revisions so noisy panels remain
// diagnosable after an update.
func (d *DB) AgentUIEventCount(ctx context.Context, sid, id string) (int64, error) {
	var count int64
	err := d.Reader().GetContext(ctx, &count, `SELECT COUNT(*) FROM agent_ui_events WHERE session_id=? AND panel_id=?`, sid, id)
	return count, err
}
func (d *DB) GetAgentUI(ctx context.Context, sid, id string) (*AgentUIPanel, error) {
	var p AgentUIPanel
	err := d.Reader().GetContext(ctx, &p, `SELECT * FROM agent_ui_panels WHERE session_id=? AND id=?`, sid, id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &p, err
}

// PutAgentUI creates or replaces a panel, within the session's bounds: at most MaxAgentUIPanels panels (a
// replacement does not count as a new one) and MaxAgentUIHTMLBytes of HTML in all, the replaced panel's own
// HTML not counted. The count, the sum and the write are one transaction.
func (d *DB) PutAgentUI(ctx context.Context, sid, id, title, html string) (*AgentUIPanel, error) {
	tx, err := d.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var count, total, own int64
	if err := tx.GetContext(ctx, &count, `SELECT COUNT(*) FROM agent_ui_panels WHERE session_id=?`, sid); err != nil {
		return nil, err
	}
	if err := tx.GetContext(ctx, &total, `SELECT COALESCE(SUM(length(CAST(html AS BLOB))),0) FROM agent_ui_panels WHERE session_id=?`, sid); err != nil {
		return nil, err
	}
	exists := true
	if err := tx.GetContext(ctx, &own, `SELECT length(CAST(html AS BLOB)) FROM agent_ui_panels WHERE session_id=? AND id=?`, sid, id); err == sql.ErrNoRows {
		exists, own = false, 0
	} else if err != nil {
		return nil, err
	}
	if !exists && count >= MaxAgentUIPanels {
		return nil, ErrUIPanelLimit
	}
	if total-own+int64(len(html)) > MaxAgentUIHTMLBytes {
		return nil, ErrUIHTMLLimit
	}
	var p AgentUIPanel
	err = tx.GetContext(ctx, &p, `INSERT INTO agent_ui_panels(session_id,id,title,html,revision,updated_at) VALUES(?,?,?,?,1,?) ON CONFLICT(session_id,id) DO UPDATE SET title=excluded.title,html=excluded.html,revision=revision+1,updated_at=excluded.updated_at RETURNING *`, sid, id, title, html, nowUTC())
	if err != nil {
		return nil, err
	}
	return &p, tx.Commit()
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
	eventID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	// Only the newest MaxAgentUIEvents of a panel are kept (the oldest go on insert): a panel's clicks
	// are read in order from a cursor, and a flood must not fill the database.
	_, _ = d.ExecContext(ctx, `DELETE FROM agent_ui_events WHERE session_id=? AND panel_id=? AND id <= (SELECT id FROM agent_ui_events WHERE session_id=? AND panel_id=? ORDER BY id DESC LIMIT 1 OFFSET ?)`, sid, id, sid, id, MaxAgentUIEvents)
	return eventID, nil
}
func (d *DB) AgentUIEvents(ctx context.Context, sid, id string, after int64) ([]AgentUIEvent, error) {
	events := []AgentUIEvent{}
	err := d.Reader().SelectContext(ctx, &events, `SELECT id,revision,action,payload,created_at FROM agent_ui_events WHERE session_id=? AND panel_id=? AND id>? ORDER BY id LIMIT 100`, sid, id, after)
	return events, err
}
