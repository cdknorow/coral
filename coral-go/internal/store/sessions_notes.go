package store

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"
)

// ── Session Notes ──────────────────────────────────────────────────────

// GetSessionNotes returns notes metadata for a session.
func (s *SessionStore) GetSessionNotes(ctx context.Context, sessionID string) (*SessionMeta, error) {
	var meta SessionMeta
	err := s.reader().GetContext(ctx, &meta,
		`SELECT notes_md, auto_summary, is_user_edited, updated_at
		 FROM session_meta WHERE session_id = ?`, sessionID)
	if err == sql.ErrNoRows {
		return &SessionMeta{SessionID: sessionID}, nil
	}
	if err != nil {
		return nil, err
	}
	meta.SessionID = sessionID
	return &meta, nil
}

// SaveSessionNotes saves user-edited notes for a session.
func (s *SessionStore) SaveSessionNotes(ctx context.Context, sessionID, notesMD string) error {
	now := nowUTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO session_meta (session_id, notes_md, is_user_edited, created_at, updated_at)
		 VALUES (?, ?, 1, ?, ?)
		 ON CONFLICT(session_id) DO UPDATE SET
		     notes_md = excluded.notes_md,
		     is_user_edited = 1,
		     updated_at = excluded.updated_at`,
		sessionID, notesMD, now, now)
	return err
}

// SaveAutoSummary saves an AI-generated summary, but only if the user hasn't edited.
func (s *SessionStore) SaveAutoSummary(ctx context.Context, sessionID, summary string) error {
	now := nowUTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO session_meta (session_id, auto_summary, created_at, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(session_id) DO UPDATE SET
		     auto_summary = excluded.auto_summary,
		     updated_at = excluded.updated_at
		 WHERE is_user_edited = 0`,
		sessionID, summary, now, now)
	return err
}

// ── Display Names ──────────────────────────────────────────────────────

// GetDisplayName returns the display name for a session.
func (s *SessionStore) GetDisplayName(ctx context.Context, sessionID string) (*string, error) {
	var name *string
	err := s.reader().GetContext(ctx, &name,
		"SELECT display_name FROM session_meta WHERE session_id = ?", sessionID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return name, err
}

// SetDisplayName sets or updates the display name for a session.
func (s *SessionStore) SetDisplayName(ctx context.Context, sessionID, displayName string) error {
	now := nowUTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO session_meta (session_id, display_name, created_at, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(session_id) DO UPDATE SET
		     display_name = excluded.display_name,
		     updated_at = excluded.updated_at`,
		sessionID, displayName, now, now)
	return err
}

// GetDisplayNames returns display names for multiple sessions.
func (s *SessionStore) GetDisplayNames(ctx context.Context, sessionIDs []string) (map[string]string, error) {
	if len(sessionIDs) == 0 {
		return map[string]string{}, nil
	}

	query, args, err := sqlx.In(
		"SELECT session_id, display_name FROM session_meta WHERE session_id IN (?) AND display_name IS NOT NULL",
		sessionIDs)
	if err != nil {
		return nil, err
	}

	var rows []struct {
		SessionID   string `db:"session_id"`
		DisplayName string `db:"display_name"`
	}
	if err := s.reader().SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}

	result := make(map[string]string, len(rows))
	for _, r := range rows {
		result[r.SessionID] = r.DisplayName
	}
	return result, nil
}

// GetLiveDisplayNames returns live_sessions.display_name for each of the
// given sessions that has one (the batched form of GetLiveSession(...).DisplayName).
func (s *SessionStore) GetLiveDisplayNames(ctx context.Context, sessionIDs []string) (map[string]string, error) {
	if len(sessionIDs) == 0 {
		return map[string]string{}, nil
	}
	query, args, err := sqlx.In(
		"SELECT session_id, display_name FROM live_sessions WHERE session_id IN (?) AND display_name IS NOT NULL",
		sessionIDs)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		SessionID   string `db:"session_id"`
		DisplayName string `db:"display_name"`
	}
	if err := s.reader().SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}
	result := make(map[string]string, len(rows))
	for _, r := range rows {
		result[r.SessionID] = r.DisplayName
	}
	return result, nil
}

// MigrateDisplayName copies a display name from an old session to a new one.
func (s *SessionStore) MigrateDisplayName(ctx context.Context, oldSessionID, newSessionID string) error {
	name, err := s.GetDisplayName(ctx, oldSessionID)
	if err != nil || name == nil {
		return err
	}
	return s.SetDisplayName(ctx, newSessionID, *name)
}
