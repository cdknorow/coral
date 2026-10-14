package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/jmoiron/sqlx"
)

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Auto-accept (#3182): a per-session switch that lets Coral answer the agent's
// approve-once permission prompts. It lives on the live session row, so a
// local enable survives a dashboard reload and a Coral restart while the same
// agent run is alive. A new row (launch, restart, resume) starts off; sleep,
// wake, archive and the end of the run clear it. An enable from the Cloud Hub
// (source "hub") is also cleared by a Coral restart, by its expiry, and when
// the owner withdraws the hub's access.

// Auto-accept sources.
const (
	AutoAcceptSourceLocal = "local"
	AutoAcceptSourceHub   = "hub"
)

// Why Coral turned auto-accept off (auto_accept_paused_code; the UI words it).
const (
	AutoAcceptRepeatLoop     = "repeat_loop"
	AutoAcceptRateCap        = "rate_cap"
	AutoAcceptExpired        = "expired"
	AutoAcceptModeLowered    = "mode_lowered"
	AutoAcceptHubAllowOff    = "hub_allow_off"
	AutoAcceptLinkRemoved    = "link_removed"
	AutoAcceptStoppedByOwner = "stopped_by_owner"
	AutoAcceptCoralRestarted = "coral_restarted"
)

// Why a new agent run starts without it (auto_accept_cleared event codes; no
// paused code: the switch simply belongs to the previous run).
const (
	AutoAcceptAgentRestarted = "agent_restarted"
	AutoAcceptAgentSlept     = "agent_slept"
	AutoAcceptAgentWoken     = "agent_woken"
	AutoAcceptAgentArchived  = "agent_archived"
	AutoAcceptRunEnded       = "run_ended"
)

// autoAcceptClearedSummary is the activity line for each cleared code.
var autoAcceptClearedSummary = map[string]string{
	AutoAcceptExpired:        "Auto-accept turned off: its time limit ran out",
	AutoAcceptModeLowered:    "Auto-accept turned off: the Coral Hub's access dropped below Launch",
	AutoAcceptHubAllowOff:    "Auto-accept turned off: Coral Hub auto-accept was turned off on this computer",
	AutoAcceptLinkRemoved:    "Auto-accept turned off: this computer was disconnected from the Coral Hub",
	AutoAcceptStoppedByOwner: "Auto-accept turned off: you stopped all auto-accept",
	AutoAcceptCoralRestarted: "Auto-accept turned off: Coral restarted (Coral Hub enables end with it)",
	AutoAcceptAgentRestarted: "Auto-accept turned off: the agent restarted",
	AutoAcceptAgentSlept:     "Auto-accept turned off: the agent went to sleep",
	AutoAcceptAgentWoken:     "Auto-accept turned off: the agent was woken (a new run)",
	AutoAcceptAgentArchived:  "Auto-accept turned off: the agent was archived",
	AutoAcceptRunEnded:       "Auto-accept turned off: the agent's run ended",
}

// AutoAcceptState is a session's auto-accept switch.
type AutoAcceptState struct {
	SessionID  string         `db:"session_id"`
	AgentType  string         `db:"agent_type"`
	AgentName  string         `db:"agent_name"`
	WorkingDir string         `db:"working_dir"`
	Enabled    bool           `db:"auto_accept"`
	Until      sql.NullInt64  `db:"auto_accept_until"` // unix seconds
	Source     sql.NullString `db:"auto_accept_source"`
	PausedCode sql.NullString `db:"auto_accept_paused_code"`
}

const autoAcceptCols = "session_id, agent_type, agent_name, working_dir, auto_accept, auto_accept_until, auto_accept_source, auto_accept_paused_code"

// EnableAutoAccept turns auto-accept on for an active, awake session. until is
// unix seconds, 0 for no limit. It reports false when no such session exists.
func (s *SessionStore) EnableAutoAccept(ctx context.Context, sessionID, source string, until int64) (bool, error) {
	var u any
	if until > 0 {
		u = until
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE live_sessions SET auto_accept = 1, auto_accept_source = ?, auto_accept_until = ?, auto_accept_paused_code = NULL
		 WHERE session_id = ? AND status = 'active' AND is_sleeping = 0`, source, u, sessionID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DisableAutoAccept turns auto-accept off at the user's request (no paused code).
func (s *SessionStore) DisableAutoAccept(ctx context.Context, sessionID string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE live_sessions SET auto_accept = 0, auto_accept_source = NULL, auto_accept_until = NULL, auto_accept_paused_code = NULL
		 WHERE session_id = ? AND status = 'active'`, sessionID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// PauseAutoAccept turns auto-accept off with a paused code (the loop guard).
// The caller records the event.
func (s *SessionStore) PauseAutoAccept(ctx context.Context, sessionID, code string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE live_sessions SET auto_accept = 0, auto_accept_source = NULL, auto_accept_until = NULL, auto_accept_paused_code = ?
		 WHERE session_id = ?`, code, sessionID)
	return err
}

// clearAutoAcceptWhere turns auto-accept off for the active sessions matching
// where (which must select rows with auto_accept = 1), records an
// auto_accept_cleared event for each, and sets the paused code when given.
// It returns how many sessions it turned off.
func (s *SessionStore) clearAutoAcceptWhere(ctx context.Context, code string, setPaused bool, where string, args ...any) (int, error) {
	n := 0
	err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		var ids []string
		if err := tx.SelectContext(ctx, &ids, "SELECT session_id FROM live_sessions WHERE auto_accept = 1 AND "+where, args...); err != nil {
			return err
		}
		for _, id := range ids {
			if err := clearAutoAcceptTx(ctx, tx, id, code, setPaused); err != nil {
				return err
			}
		}
		n = len(ids)
		return nil
	})
	return n, err
}

// clearAutoAcceptTx turns one session's auto-accept off and records why, if
// it was on. Used by the run lifecycle (sleep, wake, restart, end) inside its
// own update.
func clearAutoAcceptTx(ctx context.Context, tx execer, sessionID, code string, setPaused bool) error {
	detail, _ := json.Marshal(map[string]string{"code": code})
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agent_events (agent_name, session_id, event_type, summary, detail_json, created_at)
		 SELECT agent_name, session_id, 'auto_accept_cleared', ?, ?, ? FROM live_sessions WHERE session_id = ? AND auto_accept = 1`,
		autoAcceptClearedSummary[code], string(detail), nowUTC(), sessionID); err != nil {
		return err
	}
	var paused any
	if setPaused {
		paused = code
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE live_sessions SET auto_accept = 0, auto_accept_source = NULL, auto_accept_until = NULL,
		 auto_accept_paused_code = CASE WHEN auto_accept = 1 THEN ? ELSE NULL END WHERE session_id = ?`, paused, sessionID)
	return err
}

// ClearHubAutoAccept turns off every enable that came from the Cloud Hub
// (link removed, access lowered, the owner's allow switch off, Coral restart).
func (s *SessionStore) ClearHubAutoAccept(ctx context.Context, code string) (int, error) {
	return s.clearAutoAcceptWhere(ctx, code, true, "status = 'active' AND auto_accept_source = ?", AutoAcceptSourceHub)
}

// ClearAllAutoAccept turns auto-accept off for every agent (Stop all).
func (s *SessionStore) ClearAllAutoAccept(ctx context.Context) (int, error) {
	return s.clearAutoAcceptWhere(ctx, AutoAcceptStoppedByOwner, true, "status = 'active'")
}

// ExpireAutoAccept turns off the enables whose time limit has passed.
func (s *SessionStore) ExpireAutoAccept(ctx context.Context, now time.Time) (int, error) {
	return s.clearAutoAcceptWhere(ctx, AutoAcceptExpired, true,
		"status = 'active' AND auto_accept_until IS NOT NULL AND auto_accept_until <= ?", now.Unix())
}

// GetAutoAcceptState returns one session's auto-accept state (zero value when
// the session is unknown).
func (s *SessionStore) GetAutoAcceptState(ctx context.Context, sessionID string) (AutoAcceptState, error) {
	var st AutoAcceptState
	err := s.db.GetContext(ctx, &st, "SELECT "+autoAcceptCols+" FROM live_sessions WHERE session_id = ?", sessionID)
	if err == sql.ErrNoRows {
		return AutoAcceptState{SessionID: sessionID}, nil
	}
	return st, err
}

// GetAutoAcceptStates returns the active sessions whose auto-accept is on or
// was paused, keyed by session id. Sessions that never used it are absent.
func (s *SessionStore) GetAutoAcceptStates(ctx context.Context) (map[string]AutoAcceptState, error) {
	var rows []AutoAcceptState
	if err := s.db.SelectContext(ctx, &rows,
		"SELECT "+autoAcceptCols+` FROM live_sessions
		 WHERE status = 'active' AND (auto_accept = 1 OR auto_accept_paused_code IS NOT NULL)`); err != nil {
		return nil, err
	}
	out := make(map[string]AutoAcceptState, len(rows))
	for _, r := range rows {
		out[r.SessionID] = r
	}
	return out, nil
}

// ListAutoAcceptSessions returns the active, awake sessions with auto-accept on.
func (s *SessionStore) ListAutoAcceptSessions(ctx context.Context) ([]AutoAcceptState, error) {
	var rows []AutoAcceptState
	err := s.db.SelectContext(ctx, &rows,
		"SELECT "+autoAcceptCols+` FROM live_sessions
		 WHERE auto_accept = 1 AND status = 'active' AND is_sleeping = 0 AND is_archived = 0`)
	return rows, err
}
