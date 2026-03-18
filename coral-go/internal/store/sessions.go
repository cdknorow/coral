package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// SessionMeta holds notes and summary metadata for a session.
type SessionMeta struct {
	SessionID    string `db:"session_id" json:"session_id"`
	NotesMD      string `db:"notes_md" json:"notes_md"`
	AutoSummary  string `db:"auto_summary" json:"auto_summary"`
	IsUserEdited bool   `db:"is_user_edited" json:"is_user_edited"`
	DisplayName  *string `db:"display_name" json:"display_name,omitempty"`
	UpdatedAt    *string `db:"updated_at" json:"updated_at,omitempty"`
}

// Tag represents a user-defined tag.
type Tag struct {
	ID    int64  `db:"id" json:"id"`
	Name  string `db:"name" json:"name"`
	Color string `db:"color" json:"color"`
}

// SessionIndex holds indexed session info for search and listing.
type SessionIndex struct {
	SessionID      string  `db:"session_id" json:"session_id"`
	SourceType     string  `db:"source_type" json:"source_type"`
	SourceFile     string  `db:"source_file" json:"source_file"`
	FirstTimestamp *string `db:"first_timestamp" json:"first_timestamp"`
	LastTimestamp   *string `db:"last_timestamp" json:"last_timestamp"`
	MessageCount   int     `db:"message_count" json:"message_count"`
	DisplaySummary string  `db:"display_summary" json:"summary"`
	IndexedAt      string  `db:"indexed_at" json:"-"`
	FileMtime      float64 `db:"file_mtime" json:"-"`
}

// LiveSession represents a persistent live session record.
type LiveSession struct {
	SessionID    string  `db:"session_id" json:"session_id"`
	AgentType    string  `db:"agent_type" json:"agent_type"`
	AgentName    string  `db:"agent_name" json:"agent_name"`
	WorkingDir   string  `db:"working_dir" json:"working_dir"`
	DisplayName  *string `db:"display_name" json:"display_name,omitempty"`
	ResumeFromID *string `db:"resume_from_id" json:"resume_from_id,omitempty"`
	Flags        *string `db:"flags" json:"flags,omitempty"`
	IsJob        int     `db:"is_job" json:"is_job"`
	Prompt       *string `db:"prompt" json:"prompt,omitempty"`
	BoardName    *string `db:"board_name" json:"board_name,omitempty"`
	BoardServer  *string `db:"board_server" json:"board_server,omitempty"`
	CreatedAt    string  `db:"created_at" json:"created_at"`
}

// UserSetting is a key-value pair.
type UserSetting struct {
	Key   string `db:"key" json:"key"`
	Value string `db:"value" json:"value"`
}

// SessionStore provides session-related database operations.
type SessionStore struct {
	db *DB
}

// NewSessionStore creates a new SessionStore.
func NewSessionStore(db *DB) *SessionStore {
	return &SessionStore{db: db}
}

// ── User Settings ──────────────────────────────────────────────────────

// GetSettings returns all user settings as a map.
func (s *SessionStore) GetSettings(ctx context.Context) (map[string]string, error) {
	var settings []UserSetting
	if err := s.db.SelectContext(ctx, &settings, "SELECT key, value FROM user_settings"); err != nil {
		return nil, err
	}
	result := make(map[string]string, len(settings))
	for _, s := range settings {
		result[s.Key] = s.Value
	}
	return result, nil
}

// SetSetting upserts a user setting.
func (s *SessionStore) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	return err
}

// ── Session Notes ──────────────────────────────────────────────────────

// GetSessionNotes returns notes metadata for a session.
func (s *SessionStore) GetSessionNotes(ctx context.Context, sessionID string) (*SessionMeta, error) {
	var meta SessionMeta
	err := s.db.GetContext(ctx, &meta,
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

	// Check if user has edited
	var edited int
	err := s.db.GetContext(ctx, &edited,
		"SELECT is_user_edited FROM session_meta WHERE session_id = ?", sessionID)
	if err == nil && edited == 1 {
		return nil // Don't overwrite user edits
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO session_meta (session_id, auto_summary, created_at, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(session_id) DO UPDATE SET
		     auto_summary = excluded.auto_summary,
		     updated_at = excluded.updated_at`,
		sessionID, summary, now, now)
	return err
}

// ── Display Names ──────────────────────────────────────────────────────

// GetDisplayName returns the display name for a session.
func (s *SessionStore) GetDisplayName(ctx context.Context, sessionID string) (*string, error) {
	var name *string
	err := s.db.GetContext(ctx, &name,
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
	if err := s.db.SelectContext(ctx, &rows, query, args...); err != nil {
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

// ── Tags ───────────────────────────────────────────────────────────────

// ListTags returns all defined tags.
func (s *SessionStore) ListTags(ctx context.Context) ([]Tag, error) {
	var tags []Tag
	err := s.db.SelectContext(ctx, &tags, "SELECT id, name, color FROM tags ORDER BY name")
	return tags, err
}

// CreateTag creates a new tag.
func (s *SessionStore) CreateTag(ctx context.Context, name, color string) (*Tag, error) {
	if color == "" {
		color = "#58a6ff"
	}
	result, err := s.db.ExecContext(ctx,
		"INSERT INTO tags (name, color) VALUES (?, ?)", name, color)
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()
	return &Tag{ID: id, Name: name, Color: color}, nil
}

// DeleteTag deletes a tag by ID.
func (s *SessionStore) DeleteTag(ctx context.Context, tagID int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM tags WHERE id = ?", tagID)
	return err
}

// GetSessionTags returns tags associated with a session.
func (s *SessionStore) GetSessionTags(ctx context.Context, sessionID string) ([]Tag, error) {
	var tags []Tag
	err := s.db.SelectContext(ctx, &tags,
		`SELECT t.id, t.name, t.color FROM tags t
		 JOIN session_tags st ON st.tag_id = t.id
		 WHERE st.session_id = ? ORDER BY t.name`, sessionID)
	return tags, err
}

// AddSessionTag associates a tag with a session.
func (s *SessionStore) AddSessionTag(ctx context.Context, sessionID string, tagID int64) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO session_tags (session_id, tag_id) VALUES (?, ?)",
		sessionID, tagID)
	return err
}

// RemoveSessionTag removes a tag association from a session.
func (s *SessionStore) RemoveSessionTag(ctx context.Context, sessionID string, tagID int64) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM session_tags WHERE session_id = ? AND tag_id = ?",
		sessionID, tagID)
	return err
}

// ── Folder Tags ────────────────────────────────────────────────────────

// GetFolderTags returns tags for a folder.
func (s *SessionStore) GetFolderTags(ctx context.Context, folderName string) ([]Tag, error) {
	var tags []Tag
	err := s.db.SelectContext(ctx, &tags,
		`SELECT t.id, t.name, t.color FROM tags t
		 JOIN folder_tags ft ON ft.tag_id = t.id
		 WHERE ft.folder_name = ? ORDER BY t.name`, folderName)
	return tags, err
}

// AddFolderTag associates a tag with a folder.
func (s *SessionStore) AddFolderTag(ctx context.Context, folderName string, tagID int64) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO folder_tags (folder_name, tag_id) VALUES (?, ?)",
		folderName, tagID)
	return err
}

// RemoveFolderTag removes a tag from a folder.
func (s *SessionStore) RemoveFolderTag(ctx context.Context, folderName string, tagID int64) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM folder_tags WHERE folder_name = ? AND tag_id = ?",
		folderName, tagID)
	return err
}

// GetAllFolderTags returns all folder-tag associations grouped by folder.
func (s *SessionStore) GetAllFolderTags(ctx context.Context) (map[string][]Tag, error) {
	var rows []struct {
		FolderName string `db:"folder_name"`
		ID         int64  `db:"id"`
		Name       string `db:"name"`
		Color      string `db:"color"`
	}
	err := s.db.SelectContext(ctx, &rows,
		`SELECT ft.folder_name, t.id, t.name, t.color FROM folder_tags ft
		 JOIN tags t ON t.id = ft.tag_id ORDER BY t.name`)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]Tag)
	for _, r := range rows {
		result[r.FolderName] = append(result[r.FolderName], Tag{
			ID: r.ID, Name: r.Name, Color: r.Color,
		})
	}
	return result, nil
}

// ── Session Index ──────────────────────────────────────────────────────

// UpsertSessionIndex inserts or replaces a session index entry.
func (s *SessionStore) UpsertSessionIndex(ctx context.Context, idx *SessionIndex) error {
	now := nowUTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO session_index
		 (session_id, source_type, source_file, first_timestamp, last_timestamp,
		  message_count, display_summary, indexed_at, file_mtime)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		idx.SessionID, idx.SourceType, idx.SourceFile,
		idx.FirstTimestamp, idx.LastTimestamp,
		idx.MessageCount, idx.DisplaySummary, now, idx.FileMtime)
	return err
}

// UpsertFTS updates the FTS5 index for a session.
func (s *SessionStore) UpsertFTS(ctx context.Context, sessionID, body string) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	tx.ExecContext(ctx, "DELETE FROM session_fts WHERE session_id = ?", sessionID)
	_, err = tx.ExecContext(ctx,
		"INSERT INTO session_fts (session_id, body) VALUES (?, ?)",
		sessionID, body)
	if err != nil {
		return err // FTS5 may not be available
	}
	return tx.Commit()
}

// EnqueueForSummarization adds a session to the summarization queue.
func (s *SessionStore) EnqueueForSummarization(ctx context.Context, sessionID string) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO summarizer_queue (session_id, status) VALUES (?, 'pending')",
		sessionID)
	return err
}

// MarkSummarized updates the summarization status for a session.
func (s *SessionStore) MarkSummarized(ctx context.Context, sessionID, status string, errMsg *string) error {
	now := nowUTC()
	_, err := s.db.ExecContext(ctx,
		"UPDATE summarizer_queue SET status = ?, attempted_at = ?, error_msg = ? WHERE session_id = ?",
		status, now, errMsg, sessionID)
	return err
}

// GetPendingSummaries returns session IDs pending summarization.
func (s *SessionStore) GetPendingSummaries(ctx context.Context, limit int) ([]string, error) {
	var ids []string
	err := s.db.SelectContext(ctx, &ids,
		"SELECT session_id FROM summarizer_queue WHERE status = 'pending' LIMIT ?", limit)
	return ids, err
}

// GetIndexedMtimes returns source_file -> file_mtime for all indexed sessions.
func (s *SessionStore) GetIndexedMtimes(ctx context.Context) (map[string]float64, error) {
	var rows []struct {
		SourceFile string  `db:"source_file"`
		FileMtime  float64 `db:"file_mtime"`
	}
	err := s.db.SelectContext(ctx, &rows,
		"SELECT source_file, file_mtime FROM session_index")
	if err != nil {
		return nil, err
	}
	result := make(map[string]float64, len(rows))
	for _, r := range rows {
		if existing, ok := result[r.SourceFile]; !ok || r.FileMtime > existing {
			result[r.SourceFile] = r.FileMtime
		}
	}
	return result, nil
}

// SessionListParams holds parameters for paginated session listing.
type SessionListParams struct {
	Page           int
	PageSize       int
	Search         string
	FTSMode        string // "phrase", "and", "or"
	TagIDs         []int64
	TagLogic       string // "AND" or "OR"
	SourceTypes    []string
	DateFrom       string
	DateTo         string
	MinDurationSec *int
	MaxDurationSec *int
}

// SessionListResult holds paginated session results.
type SessionListResult struct {
	Sessions []SessionListItem `json:"sessions"`
	Total    int               `json:"total"`
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
}

// SessionListItem is an enriched session for list display.
type SessionListItem struct {
	SessionID      string  `json:"session_id"`
	SourceType     string  `json:"source_type"`
	SourceFile     string  `json:"source_file"`
	FirstTimestamp *string `json:"first_timestamp"`
	LastTimestamp   *string `json:"last_timestamp"`
	MessageCount   int     `json:"message_count"`
	Summary        string  `json:"summary"`
	SummaryTitle   string  `json:"summary_title"`
	HasNotes       bool    `json:"has_notes"`
	Tags           []Tag   `json:"tags"`
	Branch         *string `json:"branch"`
	DurationSec    *int    `json:"duration_sec"`
}

// ListSessionsPaged returns a paginated, filtered list of sessions.
func (s *SessionStore) ListSessionsPaged(ctx context.Context, params SessionListParams) (*SessionListResult, error) {
	if params.Page < 1 {
		params.Page = 1
	}
	if params.PageSize < 1 {
		params.PageSize = 50
	}
	if params.FTSMode == "" {
		params.FTSMode = "and"
	}

	var args []interface{}
	var whereClauses []string
	fromClause := "session_index si"
	orderClause := "si.last_timestamp DESC"

	// Full-text search
	if params.Search != "" {
		safeQ := sanitizeFTSQuery(params.Search, params.FTSMode)
		if safeQ != "" {
			fromClause += " JOIN session_fts fts ON fts.session_id = si.session_id"
			whereClauses = append(whereClauses, "session_fts MATCH ?")
			args = append(args, safeQ)
			orderClause = "rank"
		}
	}

	// Date filters
	if params.DateFrom != "" {
		whereClauses = append(whereClauses, "si.last_timestamp >= ?")
		args = append(args, params.DateFrom+"T00:00:00")
	}
	if params.DateTo != "" {
		whereClauses = append(whereClauses, "si.last_timestamp <= ?")
		args = append(args, params.DateTo+"T23:59:59")
	}

	// Duration filters
	if params.MinDurationSec != nil {
		whereClauses = append(whereClauses,
			"(julianday(si.last_timestamp) - julianday(si.first_timestamp)) * 86400 >= ?")
		args = append(args, *params.MinDurationSec)
	}
	if params.MaxDurationSec != nil {
		whereClauses = append(whereClauses,
			"(julianday(si.last_timestamp) - julianday(si.first_timestamp)) * 86400 <= ?")
		args = append(args, *params.MaxDurationSec)
	}

	// Tag filters
	if len(params.TagIDs) > 0 && params.TagLogic == "AND" {
		for _, tid := range params.TagIDs {
			whereClauses = append(whereClauses,
				"si.session_id IN (SELECT session_id FROM session_tags WHERE tag_id = ?)")
			args = append(args, tid)
		}
	} else if len(params.TagIDs) > 0 {
		placeholders := strings.Repeat("?,", len(params.TagIDs))
		placeholders = placeholders[:len(placeholders)-1]
		whereClauses = append(whereClauses,
			fmt.Sprintf("si.session_id IN (SELECT session_id FROM session_tags WHERE tag_id IN (%s))", placeholders))
		for _, tid := range params.TagIDs {
			args = append(args, tid)
		}
	}

	// Source type filter
	if len(params.SourceTypes) > 0 {
		placeholders := strings.Repeat("?,", len(params.SourceTypes))
		placeholders = placeholders[:len(placeholders)-1]
		whereClauses = append(whereClauses, fmt.Sprintf("si.source_type IN (%s)", placeholders))
		for _, st := range params.SourceTypes {
			args = append(args, st)
		}
	}

	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = " WHERE " + strings.Join(whereClauses, " AND ")
	}

	// Count total
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM %s%s", fromClause, whereSQL)
	var total int
	if err := s.db.GetContext(ctx, &total, countSQL, args...); err != nil {
		return nil, err
	}

	// Fetch page
	offset := (params.Page - 1) * params.PageSize
	selectFields := `si.session_id, si.source_type, si.source_file,
		si.first_timestamp, si.last_timestamp, si.message_count, si.display_summary`
	query := fmt.Sprintf("SELECT %s FROM %s%s ORDER BY %s LIMIT ? OFFSET ?",
		selectFields, fromClause, whereSQL, orderClause)
	pageArgs := append(args, params.PageSize, offset)

	var rows []struct {
		SessionID      string  `db:"session_id"`
		SourceType     string  `db:"source_type"`
		SourceFile     string  `db:"source_file"`
		FirstTimestamp *string `db:"first_timestamp"`
		LastTimestamp   *string `db:"last_timestamp"`
		MessageCount   int     `db:"message_count"`
		DisplaySummary string  `db:"display_summary"`
	}
	if err := s.db.SelectContext(ctx, &rows, query, pageArgs...); err != nil {
		return nil, err
	}

	// Collect session IDs for enrichment
	sessionIDs := make([]string, len(rows))
	for i, r := range rows {
		sessionIDs[i] = r.SessionID
	}

	// Enrich with metadata
	metaMap := make(map[string]struct {
		HasNotes     bool
		IsUserEdited bool
		SummaryTitle string
	})
	tagsMap := make(map[string][]Tag)
	branchMap := make(map[string]string)

	if len(sessionIDs) > 0 {
		// Fetch notes metadata
		metaQuery, metaArgs, _ := sqlx.In(
			"SELECT session_id, notes_md, auto_summary, is_user_edited FROM session_meta WHERE session_id IN (?)",
			sessionIDs)
		var metaRows []struct {
			SessionID    string `db:"session_id"`
			NotesMD      string `db:"notes_md"`
			AutoSummary  string `db:"auto_summary"`
			IsUserEdited bool   `db:"is_user_edited"`
		}
		if err := s.db.SelectContext(ctx, &metaRows, metaQuery, metaArgs...); err == nil {
			for _, r := range metaRows {
				content := r.NotesMD
				if content == "" {
					content = r.AutoSummary
				}
				metaMap[r.SessionID] = struct {
					HasNotes     bool
					IsUserEdited bool
					SummaryTitle string
				}{
					HasNotes:     r.NotesMD != "" || r.AutoSummary != "",
					IsUserEdited: r.IsUserEdited,
					SummaryTitle: extractFirstHeader(content),
				}
			}
		}

		// Fetch tags
		tagQuery, tagArgs, _ := sqlx.In(
			`SELECT st.session_id, t.id, t.name, t.color
			 FROM session_tags st JOIN tags t ON t.id = st.tag_id
			 WHERE st.session_id IN (?) ORDER BY t.name`,
			sessionIDs)
		var tagRows []struct {
			SessionID string `db:"session_id"`
			ID        int64  `db:"id"`
			Name      string `db:"name"`
			Color     string `db:"color"`
		}
		if err := s.db.SelectContext(ctx, &tagRows, tagQuery, tagArgs...); err == nil {
			for _, r := range tagRows {
				tagsMap[r.SessionID] = append(tagsMap[r.SessionID], Tag{
					ID: r.ID, Name: r.Name, Color: r.Color,
				})
			}
		}

		// Fetch git branches
		branchQuery, branchArgs, _ := sqlx.In(
			`SELECT gs.session_id, gs.branch
			 FROM git_snapshots gs
			 INNER JOIN (
			     SELECT session_id, MAX(recorded_at) as max_ts
			     FROM git_snapshots WHERE session_id IN (?)
			     GROUP BY session_id
			 ) latest ON gs.session_id = latest.session_id AND gs.recorded_at = latest.max_ts`,
			sessionIDs)
		var branchRows []struct {
			SessionID string `db:"session_id"`
			Branch    string `db:"branch"`
		}
		if err := s.db.SelectContext(ctx, &branchRows, branchQuery, branchArgs...); err == nil {
			for _, r := range branchRows {
				branchMap[r.SessionID] = r.Branch
			}
		}
	}

	// Build results
	sessions := make([]SessionListItem, len(rows))
	for i, r := range rows {
		meta := metaMap[r.SessionID]
		tags := tagsMap[r.SessionID]
		if tags == nil {
			tags = []Tag{}
		}
		var branch *string
		if b, ok := branchMap[r.SessionID]; ok {
			branch = &b
		}

		sessions[i] = SessionListItem{
			SessionID:      r.SessionID,
			SourceType:     r.SourceType,
			SourceFile:     r.SourceFile,
			FirstTimestamp: r.FirstTimestamp,
			LastTimestamp:   r.LastTimestamp,
			MessageCount:   r.MessageCount,
			Summary:        r.DisplaySummary,
			SummaryTitle:   meta.SummaryTitle,
			HasNotes:       meta.HasNotes,
			Tags:           tags,
			Branch:         branch,
			DurationSec:    computeDuration(r.FirstTimestamp, r.LastTimestamp),
		}
	}

	return &SessionListResult{
		Sessions: sessions,
		Total:    total,
		Page:     params.Page,
		PageSize: params.PageSize,
	}, nil
}

// ── Agent Live State ──────────────────────────────────────────────────

// GetAgentSessionID returns the current session_id for a live agent.
func (s *SessionStore) GetAgentSessionID(ctx context.Context, agentName string) (*string, error) {
	var id *string
	err := s.db.GetContext(ctx, &id,
		"SELECT current_session_id FROM agent_live_state WHERE agent_name = ?", agentName)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return id, err
}

// SetAgentSessionID sets the current session_id for a live agent.
func (s *SessionStore) SetAgentSessionID(ctx context.Context, agentName, sessionID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_live_state (agent_name, current_session_id) VALUES (?, ?)
		 ON CONFLICT(agent_name) DO UPDATE SET current_session_id = excluded.current_session_id`,
		agentName, sessionID)
	return err
}

// ClearAgentSessionID clears the current session_id for a live agent.
func (s *SessionStore) ClearAgentSessionID(ctx context.Context, agentName string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_live_state (agent_name, current_session_id) VALUES (?, NULL)
		 ON CONFLICT(agent_name) DO UPDATE SET current_session_id = NULL`,
		agentName)
	return err
}

// ── Live Sessions ─────────────────────────────────────────────────────

// RegisterLiveSession registers a new live session.
func (s *SessionStore) RegisterLiveSession(ctx context.Context, ls *LiveSession) error {
	if ls.CreatedAt == "" {
		ls.CreatedAt = nowUTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO live_sessions
		 (session_id, agent_type, agent_name, working_dir, display_name, resume_from_id, flags, is_job, prompt, board_name, board_server, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ls.SessionID, ls.AgentType, ls.AgentName, ls.WorkingDir,
		ls.DisplayName, ls.ResumeFromID, ls.Flags, ls.IsJob,
		ls.Prompt, ls.BoardName, ls.BoardServer, ls.CreatedAt)
	return err
}

// UnregisterLiveSession removes a live session record.
func (s *SessionStore) UnregisterLiveSession(ctx context.Context, sessionID string) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM live_sessions WHERE session_id = ?", sessionID)
	return err
}

// GetAllLiveSessions returns all registered live sessions.
func (s *SessionStore) GetAllLiveSessions(ctx context.Context) ([]LiveSession, error) {
	var sessions []LiveSession
	err := s.db.SelectContext(ctx, &sessions,
		`SELECT session_id, agent_type, agent_name, working_dir, display_name,
		 resume_from_id, flags, is_job, prompt, board_name, board_server, created_at
		 FROM live_sessions ORDER BY created_at`)
	return sessions, err
}

// GetLiveSessionPromptInfo returns prompt, board_name, and board_server for a live session.
func (s *SessionStore) GetLiveSessionPromptInfo(ctx context.Context, sessionID string) (*LiveSession, error) {
	var ls LiveSession
	err := s.db.GetContext(ctx, &ls,
		"SELECT prompt, board_name, board_server FROM live_sessions WHERE session_id = ?", sessionID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &ls, err
}

// GetAgentTypeForSession looks up the agent type for a live session.
func (s *SessionStore) GetAgentTypeForSession(ctx context.Context, sessionID string) string {
	var agentType string
	err := s.db.GetContext(ctx, &agentType,
		"SELECT agent_type FROM live_sessions WHERE session_id = ?", sessionID)
	if err != nil {
		return "claude"
	}
	return agentType
}

// ReplaceLiveSession replaces an old session with a new one, carrying forward metadata.
func (s *SessionStore) ReplaceLiveSession(ctx context.Context, oldSessionID string, newSession *LiveSession) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Carry forward flags, prompt, board from old session if not set
	var old LiveSession
	err = tx.GetContext(ctx, &old,
		"SELECT flags, prompt, board_name, board_server FROM live_sessions WHERE session_id = ?",
		oldSessionID)
	if err == nil {
		if newSession.Flags == nil {
			newSession.Flags = old.Flags
		}
		if newSession.Prompt == nil {
			newSession.Prompt = old.Prompt
		}
		if newSession.BoardName == nil {
			newSession.BoardName = old.BoardName
		}
		if newSession.BoardServer == nil {
			newSession.BoardServer = old.BoardServer
		}
	}

	tx.ExecContext(ctx, "DELETE FROM live_sessions WHERE session_id = ?", oldSessionID)

	now := nowUTC()
	_, err = tx.ExecContext(ctx,
		`INSERT OR REPLACE INTO live_sessions
		 (session_id, agent_type, agent_name, working_dir, display_name, resume_from_id, flags, prompt, board_name, board_server, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		newSession.SessionID, newSession.AgentType, newSession.AgentName,
		newSession.WorkingDir, newSession.DisplayName, newSession.ResumeFromID,
		newSession.Flags, newSession.Prompt, newSession.BoardName,
		newSession.BoardServer, now)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// ── Live Session Flags Helper ──────────────────────────────────────────

// ParseFlags deserializes a JSON flags string into a slice.
func ParseFlags(flagsJSON *string) []string {
	if flagsJSON == nil || *flagsJSON == "" {
		return nil
	}
	var flags []string
	if err := json.Unmarshal([]byte(*flagsJSON), &flags); err != nil {
		return nil
	}
	return flags
}

// MarshalFlags serializes a flags slice to JSON.
func MarshalFlags(flags []string) *string {
	if len(flags) == 0 {
		return nil
	}
	b, err := json.Marshal(flags)
	if err != nil {
		return nil
	}
	s := string(b)
	return &s
}

// ── Helpers ────────────────────────────────────────────────────────────

var headerRE = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)

func extractFirstHeader(text string) string {
	if text == "" {
		return ""
	}
	m := headerRE.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// sanitizeFTSQuery translates a user query into a safe FTS5 expression.
func sanitizeFTSQuery(raw, mode string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	switch mode {
	case "phrase", "and", "or":
	default:
		mode = "phrase"
	}

	if mode == "phrase" {
		cleaned := strings.ReplaceAll(raw, `"`, " ")
		cleaned = strings.TrimSpace(cleaned)
		if cleaned == "" {
			return ""
		}
		return `"` + cleaned + `"`
	}

	// Tokenize: keep "quoted phrases" together, split bare words
	var tokens []string
	i := 0
	for i < len(raw) {
		if raw[i] == '"' {
			j := strings.IndexByte(raw[i+1:], '"')
			var end int
			if j == -1 {
				end = len(raw) - 1
			} else {
				end = i + 1 + j
			}
			tokens = append(tokens, raw[i:end+1])
			i = end + 1
		} else if raw[i] == ' ' || raw[i] == '\t' {
			i++
		} else {
			j := i
			for j < len(raw) && raw[j] != ' ' && raw[j] != '\t' && raw[j] != '"' {
				j++
			}
			word := raw[i:j]
			upper := strings.ToUpper(word)
			if upper != "AND" && upper != "OR" && upper != "NOT" {
				tokens = append(tokens, word)
			}
			i = j
		}
	}

	if len(tokens) == 0 {
		return ""
	}

	joiner := " AND "
	if mode == "or" {
		joiner = " OR "
	}
	return strings.Join(tokens, joiner)
}

func computeDuration(firstTS, lastTS *string) *int {
	if firstTS == nil || lastTS == nil || *firstTS == "" || *lastTS == "" {
		return nil
	}

	parseTS := func(ts string) (time.Time, error) {
		// Strip timezone suffix and fractional seconds for parsing
		base := ts
		for _, sep := range []string{"+", "Z"} {
			if idx := strings.Index(base, sep); idx > 0 {
				base = base[:idx]
			}
		}
		if idx := strings.Index(base, "."); idx > 0 {
			base = base[:idx]
		}
		return time.Parse("2006-01-02T15:04:05", base)
	}

	a, err := parseTS(*firstTS)
	if err != nil {
		return nil
	}
	b, err := parseTS(*lastTS)
	if err != nil {
		return nil
	}

	delta := int(math.Max(0, b.Sub(a).Seconds()))
	return &delta
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}
