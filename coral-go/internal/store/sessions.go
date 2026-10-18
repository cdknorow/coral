package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/cdknorow/coral/internal/ptr"
)

// SessionMeta holds notes and summary metadata for a session.
type SessionMeta struct {
	SessionID    string  `db:"session_id" json:"session_id"`
	NotesMD      string  `db:"notes_md" json:"notes_md"`
	AutoSummary  string  `db:"auto_summary" json:"auto_summary"`
	IsUserEdited bool    `db:"is_user_edited" json:"is_user_edited"`
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
	LastTimestamp  *string `db:"last_timestamp" json:"last_timestamp"`
	MessageCount   int     `db:"message_count" json:"message_count"`
	DisplaySummary string  `db:"display_summary" json:"summary"`
	AgentName      string  `db:"agent_name" json:"agent_name,omitempty"`
	DisplayName    string  `db:"display_name" json:"display_name,omitempty"`
	IndexedAt      string  `db:"indexed_at" json:"-"`
	FileMtime      float64 `db:"file_mtime" json:"-"`
}

// LiveSession represents a persistent live session record.
type LiveSession struct {
	SessionID     string  `db:"session_id" json:"session_id"`
	AgentType     string  `db:"agent_type" json:"agent_type"`
	AgentName     string  `db:"agent_name" json:"agent_name"`
	WorkingDir    string  `db:"working_dir" json:"working_dir"`
	DisplayName   *string `db:"display_name" json:"display_name,omitempty"`
	ResumeFromID  *string `db:"resume_from_id" json:"resume_from_id,omitempty"`
	Flags         *string `db:"flags" json:"flags,omitempty"`
	IsJob         int     `db:"is_job" json:"is_job"`
	Prompt        *string `db:"prompt" json:"prompt,omitempty"`
	BoardName     *string `db:"board_name" json:"board_name,omitempty"`
	BoardServer   *string `db:"board_server" json:"board_server,omitempty"`
	Backend       *string `db:"backend" json:"backend,omitempty"`
	Icon          *string `db:"icon" json:"icon,omitempty"`
	NameColor     *string `db:"name_color" json:"name_color,omitempty"`
	IsSleeping    int     `db:"is_sleeping" json:"is_sleeping"`
	IsArchived    int     `db:"is_archived" json:"is_archived"`
	SleepReason   *string `db:"sleep_reason" json:"sleep_reason,omitempty"`
	BoardType     *string `db:"board_type" json:"board_type,omitempty"`
	GitDiffMode   *string `db:"git_diff_mode" json:"git_diff_mode,omitempty"`
	Capabilities  *string `db:"capabilities" json:"capabilities,omitempty"`
	Model         *string `db:"model" json:"model,omitempty"`
	ContextWindow int     `db:"context_window" json:"context_window,omitempty"`
	// ObservedModel is the model a hook last reported (agy); display only, never a launch setting.
	ObservedModel *string `db:"observed_model" json:"-"`
	Tools         *string `db:"tools" json:"tools,omitempty"`
	PID           int     `db:"pid" json:"pid,omitempty"`
	WorktreePath  *string `db:"worktree_path" json:"worktree_path,omitempty"`
	WorktreeRepo  *string `db:"worktree_repo" json:"worktree_repo,omitempty"`
	TeamID        *int64  `db:"team_id" json:"team_id,omitempty"`
	CreatedAt     string  `db:"created_at" json:"created_at"`
	StoppedAt     *string `db:"stopped_at" json:"stopped_at,omitempty"`
	Status        string  `db:"status" json:"status,omitempty"`
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

// DB returns the underlying database connection.
func (s *SessionStore) DB() *DB {
	return s.db
}

func (s *SessionStore) reader() *sqlx.DB {
	return s.db.Reader()
}

// ── User Settings ──────────────────────────────────────────────────────

// GetSettings returns all user settings as a map.
func (s *SessionStore) GetSettings(ctx context.Context) (map[string]string, error) {
	var settings []UserSetting
	if err := s.reader().SelectContext(ctx, &settings, "SELECT key, value FROM user_settings"); err != nil {
		return nil, err
	}
	result := make(map[string]string, len(settings))
	for _, s := range settings {
		result[s.Key] = s.Value
	}
	return result, nil
}

// settingsForCLITimeout bounds GetSettingsForCLI.
const settingsForCLITimeout = 10 * time.Second

// GetSettingsForCLI is GetSettings for a caller that is about to run an agent CLI and must not
// fall back to the one on PATH when the read fails (#4350): it reads with a context the caller's
// cancellation cannot end (a client that hangs up in the middle of a team launch), bounded by
// settingsForCLITimeout so a stuck database cannot hold the launch for ever. A caller treats an
// error as "do not run anything".
func (s *SessionStore) GetSettingsForCLI(ctx context.Context) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settingsForCLITimeout)
	defer cancel()
	return s.GetSettings(ctx)
}

// SetSetting upserts a user setting.
func (s *SessionStore) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	return err
}

// DeleteSetting deletes a user setting. Returns true if the key existed.
func (s *SessionStore) DeleteSetting(ctx context.Context, key string) (bool, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM user_settings WHERE key = ?", key)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

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

// MigrateSessionTags gives newSessionID the tags oldSessionID has (a restart or
// resume replaces the session row; the agent keeps its tags, #3216).
func (s *SessionStore) MigrateSessionTags(ctx context.Context, oldSessionID, newSessionID string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO session_tags (session_id, tag_id) SELECT ?, tag_id FROM session_tags WHERE session_id = ?",
			newSessionID, oldSessionID); err != nil {
			return err
		}
		// The old session's rows are orphans unless its history entry still shows
		// them (session history lists tags by session id): drop only the orphans (#3273).
		_, err := tx.ExecContext(ctx,
			"DELETE FROM session_tags WHERE session_id = ? AND ? <> ? AND NOT EXISTS (SELECT 1 FROM session_index WHERE session_id = ?)",
			oldSessionID, oldSessionID, newSessionID, oldSessionID)
		return err
	})
}

// ── Tags ───────────────────────────────────────────────────────────────

// ListTags returns all defined tags.
func (s *SessionStore) ListTags(ctx context.Context) ([]Tag, error) {
	var tags []Tag
	err := s.reader().SelectContext(ctx, &tags, "SELECT id, name, color FROM tags ORDER BY name")
	return tags, err
}

// ErrTagColor is CreateTag's error for a colour that is not #rgb, #rrggbb or #rrggbbaa: the dashboard puts it into a style attribute (#4875).
var ErrTagColor = errors.New("a tag colour is #rgb, #rrggbb or #rrggbbaa")

var tagColorRe = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)

// ValidTagColor reports whether c is a colour a tag may have: #rgb, #rrggbb or #rrggbbaa.
func ValidTagColor(c string) bool { return tagColorRe.MatchString(c) }

// CreateTag creates a new tag.
func (s *SessionStore) CreateTag(ctx context.Context, name, color string) (*Tag, error) {
	if color == "" {
		color = "#58a6ff"
	}
	if !ValidTagColor(color) {
		return nil, ErrTagColor
	}
	result, err := s.db.ExecContext(ctx,
		"INSERT INTO tags (name, color) VALUES (?, ?)", name, color)
	if err != nil {
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
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
	err := s.reader().SelectContext(ctx, &tags,
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
	err := s.reader().SelectContext(ctx, &tags,
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

// ── Pinned folders (#3630) ─────────────────────────────────────────────

// PinnedFolder is a folder the Operator pinned to the Agents list.
type PinnedFolder struct {
	Path     string `db:"path" json:"path"`
	PinnedAt string `db:"pinned_at" json:"pinned_at"`
}

// MaxPinnedFolders caps how many folders can be pinned.
const MaxPinnedFolders = 100

// ErrTooManyPinnedFolders is PinFolder's error when MaxPinnedFolders are already pinned.
var ErrTooManyPinnedFolders = errors.New("too many pinned folders")

// ListPinnedFolders returns the pinned folders, oldest pin first.
func (s *SessionStore) ListPinnedFolders(ctx context.Context) ([]PinnedFolder, error) {
	out := []PinnedFolder{}
	err := s.reader().SelectContext(ctx, &out, "SELECT path, pinned_at FROM pinned_folders ORDER BY pinned_at, path")
	return out, err
}

// PinFolder pins path (already checked by the caller). Pinning a pinned folder changes nothing.
func (s *SessionStore) PinFolder(ctx context.Context, path string) error {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO pinned_folders (path, pinned_at)
		 SELECT ?, ? WHERE (SELECT COUNT(*) FROM pinned_folders) < ? OR EXISTS (SELECT 1 FROM pinned_folders WHERE path = ?)
		 ON CONFLICT(path) DO NOTHING`,
		path, time.Now().UTC().Format(time.RFC3339Nano), MaxPinnedFolders, path)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var exists int
		if err := s.db.GetContext(ctx, &exists, "SELECT COUNT(*) FROM pinned_folders WHERE path = ?", path); err == nil && exists == 0 {
			return ErrTooManyPinnedFolders
		}
	}
	return nil
}

// UnpinFolder removes a pin; it reports whether path was pinned.
func (s *SessionStore) UnpinFolder(ctx context.Context, path string) (bool, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM pinned_folders WHERE path = ?", path)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// GetAllFolderTags returns all folder-tag associations grouped by folder.
func (s *SessionStore) GetAllFolderTags(ctx context.Context) (map[string][]Tag, error) {
	var rows []struct {
		FolderName string `db:"folder_name"`
		ID         int64  `db:"id"`
		Name       string `db:"name"`
		Color      string `db:"color"`
	}
	err := s.reader().SelectContext(ctx, &rows,
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
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// A Codex rollout file represents exactly one session. Remove stale aliases
		// created if an older parser extracted a malformed Coral session marker.
		if idx.SourceType == "codex" {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM session_fts WHERE session_id IN
				 (SELECT session_id FROM session_index WHERE source_type = 'codex'
				  AND source_file = ? AND session_id <> ?)`,
				idx.SourceFile, idx.SessionID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM session_index WHERE source_type = 'codex'
				 AND source_file = ? AND session_id <> ?`,
				idx.SourceFile, idx.SessionID); err != nil {
				return err
			}
		}

		_, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO session_index
			 (session_id, source_type, source_file, first_timestamp, last_timestamp,
			  message_count, display_summary, agent_name, display_name, indexed_at, file_mtime)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			idx.SessionID, idx.SourceType, idx.SourceFile,
			idx.FirstTimestamp, idx.LastTimestamp,
			idx.MessageCount, idx.DisplaySummary,
			idx.AgentName, idx.DisplayName, now, idx.FileMtime)
		return err
	})
}

// GetIndexedSession returns the persisted source metadata for a history ID.
// History IDs may be Coral marker IDs rather than provider-native IDs, so
// callers must use the source file to recover provider-specific identity.
func (s *SessionStore) GetIndexedSession(ctx context.Context, sessionID string) (*SessionIndex, error) {
	var idx SessionIndex
	err := s.reader().GetContext(ctx, &idx,
		`SELECT session_id, source_type, source_file, first_timestamp, last_timestamp,
			message_count, display_summary, agent_name, display_name, indexed_at, file_mtime
		 FROM session_index WHERE session_id = ?`, sessionID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &idx, nil
}

// UpsertFTS updates the FTS5 index for a session.
func (s *SessionStore) UpsertFTS(ctx context.Context, sessionID, body string) error {
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM session_fts WHERE session_id = ?", sessionID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			"INSERT INTO session_fts (session_id, body) VALUES (?, ?)",
			sessionID, body)
		return err // FTS5 may not be available
	})
}

// FTSItem is a session's search text for UpsertFTSBatch.
type FTSItem struct {
	SessionID string
	Body      string
}

// ftsBatchIDs is how many sessions' old rows one DELETE removes: session_fts is an FTS5 table, so a delete by session id is a scan of
// every transcript's text, and one statement with many ids scans once for all of them (#4669; UpsertFTS made one scan per session).
const ftsBatchIDs = 100

// UpsertFTSBatch is UpsertFTS for many sessions in few transactions: each chunk of ftsBatchIDs sessions is one transaction, one DELETE of
// their old rows (one scan of the table) and one INSERT each. A session named twice in items keeps its last body. The result is what
// calling UpsertFTS for each, in order, gives.
func (s *SessionStore) UpsertFTSBatch(ctx context.Context, items []FTSItem) error {
	last := make(map[string]int, len(items)) // the last body of each session
	for i, it := range items {
		last[it.SessionID] = i
	}
	var kept []FTSItem
	for i, it := range items {
		if last[it.SessionID] == i {
			kept = append(kept, it)
		}
	}
	for start := 0; start < len(kept); start += ftsBatchIDs {
		chunk := kept[start:min(start+ftsBatchIDs, len(kept))]
		err := s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
			ids := make([]any, len(chunk))
			for i, it := range chunk {
				ids[i] = it.SessionID
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM session_fts WHERE session_id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+")", ids...); err != nil {
				return err
			}
			for _, it := range chunk {
				if _, err := tx.ExecContext(ctx, "INSERT INTO session_fts (session_id, body) VALUES (?, ?)", it.SessionID, it.Body); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// EnqueueForSummarization adds a session to the summarization queue.
// If the session already exists with a 'failed' status, it is reset to 'pending'
// so it will be retried (e.g. after re-indexing with a corrected source_file).
func (s *SessionStore) EnqueueForSummarization(ctx context.Context, sessionID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO summarizer_queue (session_id, status) VALUES (?, 'pending')
		 ON CONFLICT(session_id) DO UPDATE SET status = 'pending', error_msg = NULL
		 WHERE status = 'failed'`,
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
	err := s.reader().SelectContext(ctx, &ids,
		"SELECT session_id FROM summarizer_queue WHERE status = 'pending' LIMIT ?", limit)
	return ids, err
}

// GetAgentNames returns agent_name and display_name for sessions from live_sessions.
func (s *SessionStore) GetAgentNames(ctx context.Context, sessionIDs []string) (map[string][2]string, error) {
	if len(sessionIDs) == 0 {
		return map[string][2]string{}, nil
	}
	query, args, err := sqlx.In(
		"SELECT session_id, agent_name, display_name FROM live_sessions WHERE session_id IN (?)",
		sessionIDs)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		SessionID   string  `db:"session_id"`
		AgentName   string  `db:"agent_name"`
		DisplayName *string `db:"display_name"`
	}
	if err := s.reader().SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}
	result := make(map[string][2]string, len(rows))
	for _, r := range rows {
		dn := ""
		if r.DisplayName != nil {
			dn = *r.DisplayName
		}
		result[r.SessionID] = [2]string{r.AgentName, dn}
	}
	return result, nil
}

// indexedMtimesSQL lists the indexed files that also have a session_fts row. The set of session ids in session_fts is read once
// (an uncorrelated IN): session_fts is an FTS5 table, so a lookup by session_id is a scan of its whole content, and a correlated
// EXISTS made one scan, of every transcript's text, for each indexed session on every indexer pass (4.1 s for 250 sessions of
// 1 MB; it grew with the square of the history, #4669).
const indexedMtimesSQL = `SELECT si.source_file, si.file_mtime FROM session_index si
	 WHERE si.session_id IN (SELECT session_id FROM session_fts)`

// GetIndexedMtimes returns source_file -> file_mtime for sessions that are
// fully indexed, so the indexer can skip re-reading files that have not changed.
//
// A session is only "fully indexed" if it also has a full-text row. Sessions
// indexed before FTSBody was populated have a session_index row and no
// session_fts row; without this join they would be skipped forever on mtime
// and stay permanently unsearchable. Excluding them here makes the next
// indexer pass re-read exactly those files and backfill them, once, with no
// migration and no flag.
func (s *SessionStore) GetIndexedMtimes(ctx context.Context) (map[string]float64, error) {
	var rows []struct {
		SourceFile string  `db:"source_file"`
		FileMtime  float64 `db:"file_mtime"`
	}
	err := s.reader().SelectContext(ctx, &rows, indexedMtimesSQL)
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
	AgentName      string
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
	LastTimestamp  *string `json:"last_timestamp"`
	MessageCount   int     `json:"message_count"`
	Summary        string  `json:"summary"`
	SummaryTitle   string  `json:"summary_title"`
	HasNotes       bool    `json:"has_notes"`
	Tags           []Tag   `json:"tags"`
	Branch         *string `json:"branch"`
	DurationSec    *int    `json:"duration_sec"`
	AgentName      string  `json:"agent_name,omitempty"`
	DisplayName    string  `json:"display_name,omitempty"`
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
		// Natural-language searches should return the best partial matches instead
		// of disappearing as soon as one query word is absent. FTS rank still puts
		// sessions matching more (and rarer) terms first; callers can explicitly
		// request "and" when every word is required.
		params.FTSMode = "or"
	}
	q := sessionListQuery(params)
	total, rows, err := s.pageSessions(ctx, q, params)
	if err != nil {
		return nil, err
	}
	sessionIDs := make([]string, len(rows))
	for i, r := range rows {
		sessionIDs[i] = r.SessionID
	}
	var more sessionListExtras
	if len(sessionIDs) > 0 {
		more = s.sessionListExtras(ctx, sessionIDs)
	}
	sessions := make([]SessionListItem, len(rows))
	for i, r := range rows {
		sessions[i] = more.item(r)
	}
	return &SessionListResult{
		Sessions: sessions,
		Total:    total,
		Page:     params.Page,
		PageSize: params.PageSize,
	}, nil
}

// sessionListSQL is a session list's FROM, WHERE (with its arguments, a slice for an IN) and ORDER BY.
type sessionListSQL struct {
	from, where, order string
	args               []interface{}
}

// sessionListQuery turns the list's filters into SQL: a full-text search (ordered by rank), the dates, the
// duration, the tags (all or any), the source types and the agent name; newest first without a search.
func sessionListQuery(params SessionListParams) sessionListSQL {
	q := sessionListSQL{from: "session_index si", order: "si.last_timestamp DESC"}
	var whereClauses []string
	where := func(clause string, arg interface{}) {
		whereClauses = append(whereClauses, clause)
		q.args = append(q.args, arg)
	}
	if params.Search != "" {
		if safeQ := sanitizeFTSQuery(params.Search, params.FTSMode); safeQ != "" {
			q.from += " JOIN session_fts fts ON fts.session_id = si.session_id"
			where("session_fts MATCH ?", safeQ)
			q.order = "rank"
		}
	}
	if params.DateFrom != "" {
		where("si.last_timestamp >= ?", params.DateFrom+"T00:00:00")
	}
	if params.DateTo != "" {
		// Through the end of the date_to day, fractions of a second included: "~" sorts after every character of a timestamp, so
		// "< date~" is every timestamp that begins with a date up to date_to (a session ending at 23:59:59.5 is not after "T23:59:59").
		where("si.last_timestamp < ?", params.DateTo+"~")
	}
	if params.MinDurationSec != nil {
		where("(julianday(si.last_timestamp) - julianday(si.first_timestamp)) * 86400 >= ?", *params.MinDurationSec)
	}
	if params.MaxDurationSec != nil {
		where("(julianday(si.last_timestamp) - julianday(si.first_timestamp)) * 86400 <= ?", *params.MaxDurationSec)
	}
	if len(params.TagIDs) > 0 && params.TagLogic == "AND" {
		for _, tid := range params.TagIDs {
			where("si.session_id IN (SELECT session_id FROM session_tags WHERE tag_id = ?)", tid)
		}
	} else if len(params.TagIDs) > 0 {
		where("si.session_id IN (SELECT session_id FROM session_tags WHERE tag_id IN (?))", params.TagIDs)
	}
	if len(params.SourceTypes) > 0 {
		where("si.source_type IN (?)", params.SourceTypes)
	}
	if params.AgentName != "" {
		where("si.agent_name = ?", params.AgentName)
	}
	if len(whereClauses) > 0 {
		q.where = " WHERE " + strings.Join(whereClauses, " AND ")
	}
	return q
}

// sessionPageRow is one session_index row of a list page.
type sessionPageRow struct {
	SessionID      string  `db:"session_id"`
	SourceType     string  `db:"source_type"`
	SourceFile     string  `db:"source_file"`
	FirstTimestamp *string `db:"first_timestamp"`
	LastTimestamp  *string `db:"last_timestamp"`
	MessageCount   int     `db:"message_count"`
	DisplaySummary string  `db:"display_summary"`
	AgentName      *string `db:"agent_name"`
	DisplayName    *string `db:"display_name"`
}

// pageSessions counts the sessions the query matches and reads the requested page of them.
func (s *SessionStore) pageSessions(ctx context.Context, q sessionListSQL, params SessionListParams) (int, []sessionPageRow, error) {
	countSQL, countArgs, err := sqlx.In(fmt.Sprintf("SELECT COUNT(*) FROM %s%s", q.from, q.where), q.args...)
	if err != nil {
		return 0, nil, err
	}
	var total int
	if err := s.reader().GetContext(ctx, &total, countSQL, countArgs...); err != nil {
		return 0, nil, err
	}
	offset := (params.Page - 1) * params.PageSize
	selectFields := `si.session_id, si.source_type, si.source_file,
		si.first_timestamp, si.last_timestamp, si.message_count, si.display_summary,
		si.agent_name, si.display_name`
	query, pageArgs, err := sqlx.In(fmt.Sprintf("SELECT %s FROM %s%s ORDER BY %s LIMIT ? OFFSET ?", selectFields, q.from, q.where, q.order),
		append(q.args, params.PageSize, offset)...)
	if err != nil {
		return 0, nil, err
	}
	var rows []sessionPageRow
	if err := s.reader().SelectContext(ctx, &rows, query, pageArgs...); err != nil {
		return 0, nil, err
	}
	return total, rows, nil
}

// sessionListExtras is what a list page shows beyond session_index, per session id: notes, tags, the latest git
// branch, and the live session's agent and display names. A lookup that fails leaves its part empty.
type sessionListExtras struct {
	meta   map[string]sessionNotesMeta
	tags   map[string][]Tag
	branch map[string]string
	agent  map[string]liveAgentNames
}

type sessionNotesMeta struct {
	HasNotes     bool
	IsUserEdited bool
	SummaryTitle string
}

type liveAgentNames struct {
	AgentName   string
	DisplayName string
}

func (s *SessionStore) sessionListExtras(ctx context.Context, sessionIDs []string) sessionListExtras {
	return sessionListExtras{
		meta:   s.sessionNotesMeta(ctx, sessionIDs),
		tags:   s.sessionTagsOf(ctx, sessionIDs),
		branch: s.sessionLatestBranches(ctx, sessionIDs),
		agent:  s.liveAgentNamesOf(ctx, sessionIDs),
	}
}

// sessionNotesMeta is whether each session has notes (its own or an auto summary) and its title: the first header
// of its notes, else of its auto summary.
func (s *SessionStore) sessionNotesMeta(ctx context.Context, sessionIDs []string) map[string]sessionNotesMeta {
	out := map[string]sessionNotesMeta{}
	metaQuery, metaArgs, _ := sqlx.In(
		"SELECT session_id, notes_md, auto_summary, is_user_edited FROM session_meta WHERE session_id IN (?)",
		sessionIDs)
	var metaRows []struct {
		SessionID    string `db:"session_id"`
		NotesMD      string `db:"notes_md"`
		AutoSummary  string `db:"auto_summary"`
		IsUserEdited bool   `db:"is_user_edited"`
	}
	if err := s.reader().SelectContext(ctx, &metaRows, metaQuery, metaArgs...); err == nil {
		for _, r := range metaRows {
			content := r.NotesMD
			if content == "" {
				content = r.AutoSummary
			}
			out[r.SessionID] = sessionNotesMeta{
				HasNotes:     r.NotesMD != "" || r.AutoSummary != "",
				IsUserEdited: r.IsUserEdited,
				SummaryTitle: extractFirstHeader(content),
			}
		}
	}
	return out
}

// sessionTagsOf is each session's tags, by name.
func (s *SessionStore) sessionTagsOf(ctx context.Context, sessionIDs []string) map[string][]Tag {
	out := map[string][]Tag{}
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
	if err := s.reader().SelectContext(ctx, &tagRows, tagQuery, tagArgs...); err == nil {
		for _, r := range tagRows {
			out[r.SessionID] = append(out[r.SessionID], Tag{ID: r.ID, Name: r.Name, Color: r.Color})
		}
	}
	return out
}

// sessionLatestBranches is each session's git branch at its latest snapshot.
func (s *SessionStore) sessionLatestBranches(ctx context.Context, sessionIDs []string) map[string]string {
	out := map[string]string{}
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
	if err := s.reader().SelectContext(ctx, &branchRows, branchQuery, branchArgs...); err == nil {
		for _, r := range branchRows {
			out[r.SessionID] = r.Branch
		}
	}
	return out
}

// liveAgentNamesOf is each live session's agent and display names.
func (s *SessionStore) liveAgentNamesOf(ctx context.Context, sessionIDs []string) map[string]liveAgentNames {
	out := map[string]liveAgentNames{}
	agentQuery, agentArgs, _ := sqlx.In(
		`SELECT session_id, agent_name, display_name FROM live_sessions WHERE session_id IN (?)`,
		sessionIDs)
	var agentRows []struct {
		SessionID   string  `db:"session_id"`
		AgentName   string  `db:"agent_name"`
		DisplayName *string `db:"display_name"`
	}
	if err := s.reader().SelectContext(ctx, &agentRows, agentQuery, agentArgs...); err == nil {
		for _, r := range agentRows {
			names := liveAgentNames{AgentName: r.AgentName}
			if r.DisplayName != nil {
				names.DisplayName = *r.DisplayName
			}
			out[r.SessionID] = names
		}
	}
	return out
}

// item is one list row: session_index's fields, its notes, tags and branch, and its names (session_index's, with
// a live session's non-empty ones over them: more current for active agents).
func (x sessionListExtras) item(r sessionPageRow) SessionListItem {
	tags := x.tags[r.SessionID]
	if tags == nil {
		tags = []Tag{}
	}
	var branch *string
	if b, ok := x.branch[r.SessionID]; ok {
		branch = &b
	}
	agentName, displayName := ptr.Value(r.AgentName), ptr.Value(r.DisplayName)
	if info, ok := x.agent[r.SessionID]; ok {
		if info.AgentName != "" {
			agentName = info.AgentName
		}
		if info.DisplayName != "" {
			displayName = info.DisplayName
		}
	}
	meta := x.meta[r.SessionID]
	return SessionListItem{
		SessionID:      r.SessionID,
		SourceType:     r.SourceType,
		SourceFile:     r.SourceFile,
		FirstTimestamp: r.FirstTimestamp,
		LastTimestamp:  r.LastTimestamp,
		MessageCount:   r.MessageCount,
		Summary:        r.DisplaySummary,
		SummaryTitle:   meta.SummaryTitle,
		HasNotes:       meta.HasNotes,
		Tags:           tags,
		Branch:         branch,
		DurationSec:    computeDuration(r.FirstTimestamp, r.LastTimestamp),
		AgentName:      agentName,
		DisplayName:    displayName,
	}
}

// ── Live Sessions ─────────────────────────────────────────────────────

// RegisterLiveSession registers a new live session.
// SetCLIVersion records the version of the CLI a session's agent runs (#3481).
func (s *SessionStore) SetCLIVersion(ctx context.Context, sessionID, version string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE live_sessions SET cli_version = NULLIF(?, '') WHERE session_id = ?", version, sessionID)
	return err
}

// SetHooksRev records the hook set a session's agent runs (#3665): at launch, from the set Coral
// installed, and from the revision its own hook events carry. It only ever goes up: an older or
// missing value never replaces a newer one (an old Coral hook left in the user's global
// settings would otherwise flip it).
func (s *SessionStore) SetHooksRev(ctx context.Context, sessionID string, rev int) error {
	if rev <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, "UPDATE live_sessions SET hooks_rev = ? WHERE session_id = ? AND (hooks_rev IS NULL OR hooks_rev < ?)", rev, sessionID, rev)
	return err
}

// HooksRevs returns the recorded hook set of every live session that has one.
func (s *SessionStore) HooksRevs(ctx context.Context) (map[string]int, error) {
	var rows []struct {
		ID  string `db:"session_id"`
		Rev int    `db:"hooks_rev"`
	}
	if err := s.db.Reader().SelectContext(ctx, &rows, "SELECT session_id, hooks_rev FROM live_sessions WHERE hooks_rev IS NOT NULL"); err != nil {
		return nil, err
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.ID] = r.Rev
	}
	return out, nil
}

// CLIVersion returns the recorded CLI version of one session, or "".
func (s *SessionStore) CLIVersion(ctx context.Context, sessionID string) string {
	var v sql.NullString
	_ = s.db.Reader().GetContext(ctx, &v, "SELECT cli_version FROM live_sessions WHERE session_id = ?", sessionID)
	return v.String
}

// CLIVersions returns the recorded CLI version of each live session that has one.
func (s *SessionStore) CLIVersions(ctx context.Context) (map[string]string, error) {
	var rows []struct {
		SessionID string `db:"session_id"`
		Version   string `db:"cli_version"`
	}
	if err := s.db.Reader().SelectContext(ctx, &rows, "SELECT session_id, cli_version FROM live_sessions WHERE cli_version IS NOT NULL"); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.SessionID] = r.Version
	}
	return out, nil
}

func (s *SessionStore) RegisterLiveSession(ctx context.Context, ls *LiveSession) error {
	if ls.CreatedAt == "" {
		ls.CreatedAt = nowUTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO live_sessions
		 (session_id, agent_type, agent_name, working_dir, display_name, resume_from_id, flags, is_job, prompt, board_name, board_server, backend, icon, is_sleeping, board_type, capabilities, model, context_window, tools, pid, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?)`,
		ls.SessionID, ls.AgentType, ls.AgentName, ls.WorkingDir,
		ls.DisplayName, ls.ResumeFromID, ls.Flags, ls.IsJob,
		ls.Prompt, ls.BoardName, ls.BoardServer, ls.Backend, ls.Icon, ls.IsSleeping, ls.BoardType,
		ls.Capabilities, ls.Model, ls.ContextWindow, ls.Tools, ls.PID, ls.CreatedAt)
	return err
}

// UnregisterLiveSession marks a live session as inactive (preserves the record for history).
func (s *SessionStore) UnregisterLiveSession(ctx context.Context, sessionID string) error {
	defer s.db.InvalidateSessionTokens() // after the commit (or the rollback): never before it (#4303)
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if err := clearAutoAcceptTx(ctx, tx, sessionID, AutoAcceptRunEnded, false); err != nil {
			return err
		}
		// The session is over: so is its agent token (#3387).
		if err := revokeSessionTokensTx(ctx, tx, sessionID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			"UPDATE live_sessions SET status = 'inactive', stopped_at = ? WHERE session_id = ?",
			nowUTC(), sessionID)
		return err
	})
}

// GetStoppedSessions returns, for the given session IDs, the ones whose
// live_sessions row is marked inactive, mapped to their stopped_at timestamp
// (empty string if none recorded). Used to recognise orphaned runtime
// sessions: tmux/PTY sessions that are still running even though Coral has
// already stopped them.
func (s *SessionStore) GetStoppedSessions(ctx context.Context, sessionIDs []string) (map[string]string, error) {
	result := map[string]string{}
	if len(sessionIDs) == 0 {
		return result, nil
	}
	query, args, err := sqlx.In(
		"SELECT session_id, COALESCE(stopped_at, '') AS stopped_at FROM live_sessions WHERE status = 'inactive' AND session_id IN (?)",
		sessionIDs)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		SessionID string `db:"session_id"`
		StoppedAt string `db:"stopped_at"`
	}
	if err := s.reader().SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}
	for _, r := range rows {
		result[r.SessionID] = r.StoppedAt
	}
	return result, nil
}

// GetAllLiveSessions returns all registered live sessions.
func (s *SessionStore) GetAllLiveSessions(ctx context.Context) ([]LiveSession, error) {
	var sessions []LiveSession
	err := s.reader().SelectContext(ctx, &sessions,
		`SELECT session_id, agent_type, agent_name, working_dir, display_name,
		 resume_from_id, flags, is_job, prompt, board_name, board_server, backend, icon, name_color, is_sleeping, is_archived, sleep_reason, board_type, capabilities, model, observed_model, context_window, tools, pid, worktree_path, worktree_repo, team_id, created_at, stopped_at
		 FROM live_sessions WHERE status = 'active' ORDER BY created_at`)
	return sessions, err
}

// CountActiveLiveSessions returns the number of non-sleeping live sessions.
func (s *SessionStore) CountActiveLiveSessions(ctx context.Context) (int, error) {
	var count int
	err := s.reader().GetContext(ctx, &count,
		"SELECT COUNT(*) FROM live_sessions WHERE is_sleeping = 0 AND status = 'active'")
	return count, err
}

// GetBoardSessions returns all live sessions on a given board.
func (s *SessionStore) GetBoardSessions(ctx context.Context, boardName string) ([]LiveSession, error) {
	var sessions []LiveSession
	err := s.reader().SelectContext(ctx, &sessions,
		`SELECT session_id, agent_type, agent_name, working_dir, display_name,
		 resume_from_id, flags, is_job, prompt, board_name, board_server, backend, icon, is_sleeping, is_archived, board_type, capabilities, model, tools, pid, worktree_path, worktree_repo, team_id, created_at, stopped_at
		 FROM live_sessions WHERE board_name = ? AND status = 'active' ORDER BY created_at`, boardName)
	return sessions, err
}

// CountLiveSessions returns the total number of live sessions (including sleeping).
func (s *SessionStore) CountLiveSessions(ctx context.Context) (int, error) {
	var count int
	err := s.reader().GetContext(ctx, &count,
		"SELECT COUNT(*) FROM live_sessions WHERE status = 'active'")
	return count, err
}

// UpdateSessionPID stores the shell process PID for a live session.
func (s *SessionStore) UpdateSessionPID(ctx context.Context, sessionID string, pid int) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE live_sessions SET pid = ? WHERE session_id = ?", pid, sessionID)
	return err
}

// SetTeamID sets the team_id on a live session.
func (s *SessionStore) SetTeamID(ctx context.Context, sessionID string, teamID int64) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE live_sessions SET team_id = ? WHERE session_id = ?", teamID, sessionID)
	return err
}

// LiveSessionsByPIDs returns every active session whose recorded process is one of pids.
// A recorded pid is only where the session's process WAS: once that process ends the
// system can reuse the number for anything, so a caller must check the session's live
// process before trusting a match (#3864).
func (s *SessionStore) LiveSessionsByPIDs(ctx context.Context, pids []int) ([]LiveSession, error) {
	if len(pids) == 0 {
		return nil, fmt.Errorf("no PIDs provided")
	}
	query, args, err := sqlx.In("SELECT session_id, agent_type, agent_name, working_dir, display_name, board_name, backend, pid FROM live_sessions WHERE pid IN (?) AND pid > 0 AND status = 'active'", pids)
	if err != nil {
		return nil, err
	}
	var out []LiveSession
	err = s.reader().SelectContext(ctx, &out, s.db.Rebind(query), args...)
	return out, err
}

// CountBoardSessions returns the number of live sessions on a given board.
func (s *SessionStore) CountBoardSessions(ctx context.Context, boardName string) (int, error) {
	var count int
	err := s.reader().GetContext(ctx, &count,
		"SELECT COUNT(*) FROM live_sessions WHERE board_name = ? AND status = 'active'", boardName)
	return count, err
}

// GetLiveSessionPromptInfo returns prompt, board_name, and board_server for a live session.
func (s *SessionStore) GetLiveSessionPromptInfo(ctx context.Context, sessionID string) (*LiveSession, error) {
	var ls LiveSession
	err := s.reader().GetContext(ctx, &ls,
		"SELECT prompt, board_name, board_server, board_type FROM live_sessions WHERE session_id = ?", sessionID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &ls, err
}

// LiveSessionState is what the terminal-log cleanup needs to know about a session: whether it has ended.
type LiveSessionState struct {
	AgentType string `db:"agent_type"`
	Status    string `db:"status"`
	Sleeping  bool   `db:"is_sleeping"`
	Archived  bool   `db:"is_archived"`
}

// LiveSessionStateByID is one session row's state, ok false when there is no such row: asked again for each terminal log just before it is removed (#4447).
func (s *SessionStore) LiveSessionStateByID(ctx context.Context, id string) (LiveSessionState, bool, error) {
	var rows []LiveSessionState
	if err := s.reader().SelectContext(ctx, &rows, "SELECT agent_type, status, is_sleeping, is_archived FROM live_sessions WHERE session_id = ?", id); err != nil {
		return LiveSessionState{}, false, err
	}
	if len(rows) != 1 {
		return LiveSessionState{}, false, nil
	}
	return rows[0], true, nil
}

// LiveSessionStates returns every session row's state by session id, whatever its status: the cleanup of an ended agent's terminal log (#4447) asks
// this and keeps anything it cannot match to a row that is stopped, awake-less (not sleeping) and not archived.
func (s *SessionStore) LiveSessionStates(ctx context.Context) (map[string]LiveSessionState, error) {
	var rows []struct {
		SessionID string `db:"session_id"`
		LiveSessionState
	}
	if err := s.reader().SelectContext(ctx, &rows, "SELECT session_id, agent_type, status, is_sleeping, is_archived FROM live_sessions"); err != nil {
		return nil, err
	}
	out := make(map[string]LiveSessionState, len(rows))
	for _, r := range rows {
		out[r.SessionID] = r.LiveSessionState
	}
	return out, nil
}

// GetLiveSession returns a full live session record by session_id.
func (s *SessionStore) GetLiveSession(ctx context.Context, sessionID string) (*LiveSession, error) {
	var ls LiveSession
	err := s.reader().GetContext(ctx, &ls,
		`SELECT session_id, agent_type, agent_name, working_dir, display_name,
		 resume_from_id, flags, is_job, prompt, board_name, board_server, backend, icon, name_color, is_sleeping, is_archived, sleep_reason, board_type, git_diff_mode, capabilities, model, observed_model, context_window, tools, pid, worktree_path, worktree_repo, team_id, created_at, stopped_at, status
		 FROM live_sessions WHERE session_id = ?`, sessionID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &ls, err
}

// ReplaceLiveSession replaces an old session with a new one, carrying forward metadata.
func (s *SessionStore) ReplaceLiveSession(ctx context.Context, oldSessionID string, newSession *LiveSession) error {
	defer s.db.InvalidateSessionTokens() // after the commit (or the rollback): never before it (#4303)
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		// A restart or resume replaces the session: the old one's token stops working (#3387).
		if err := revokeSessionTokensTx(ctx, tx, oldSessionID); err != nil {
			return err
		}
		// Carry forward flags, prompt, board from old session if not set
		var old LiveSession
		err := tx.GetContext(ctx, &old,
			"SELECT agent_type, flags, prompt, board_name, board_server, icon, name_color, is_sleeping, board_type, display_name, is_job, backend, capabilities, model, context_window, tools FROM live_sessions WHERE session_id = ?",
			oldSessionID)
		if err == nil {
			sameProvider := newSession.AgentType == "" || newSession.AgentType == old.AgentType
			if newSession.Flags == nil && sameProvider {
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
			if newSession.Icon == nil {
				newSession.Icon = old.Icon
			}
			if newSession.NameColor == nil {
				newSession.NameColor = old.NameColor
			}
			if newSession.BoardType == nil {
				newSession.BoardType = old.BoardType
			}
			if newSession.DisplayName == nil {
				newSession.DisplayName = old.DisplayName
			}
			if newSession.Capabilities == nil && sameProvider {
				newSession.Capabilities = old.Capabilities
			}
			if newSession.Model == nil && sameProvider {
				newSession.Model = old.Model
			}
			if newSession.Model == nil && newSession.ContextWindow == 0 && sameProvider {
				newSession.ContextWindow = old.ContextWindow
			}
			if newSession.Tools == nil {
				newSession.Tools = old.Tools
			}
			if newSession.Backend == nil {
				newSession.Backend = old.Backend
			}
			newSession.IsSleeping = old.IsSleeping
			if newSession.IsJob == 0 {
				newSession.IsJob = old.IsJob
			}
		}

		// The new row starts without auto-accept (a new agent run).
		if err := clearAutoAcceptTx(ctx, tx, oldSessionID, AutoAcceptAgentRestarted, false); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE live_sessions SET status = 'inactive', stopped_at = ? WHERE session_id = ?", nowUTC(), oldSessionID); err != nil {
			return err
		}
		// A team member follows its agent to the new session (#4074): kill, resurrect and the
		// knowledge distiller find the member by its current session id.
		if _, err := tx.ExecContext(ctx, "UPDATE team_members SET session_id = ? WHERE session_id = ?", newSession.SessionID, oldSessionID); err != nil {
			return err
		}

		now := nowUTC()
		_, err = tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO live_sessions
			 (session_id, agent_type, agent_name, working_dir, display_name, resume_from_id, flags, is_job, prompt, board_name, board_server, backend, icon, name_color, is_sleeping, board_type, capabilities, model, context_window, tools, pid, status, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?)`,
			newSession.SessionID, newSession.AgentType, newSession.AgentName,
			newSession.WorkingDir, newSession.DisplayName, newSession.ResumeFromID,
			newSession.Flags, newSession.IsJob, newSession.Prompt, newSession.BoardName,
			newSession.BoardServer, newSession.Backend, newSession.Icon, newSession.NameColor, newSession.IsSleeping, newSession.BoardType,
			newSession.Capabilities, newSession.Model, newSession.ContextWindow, newSession.Tools, newSession.PID, now)
		return err
	})
}

// CountSessionsWithWorktree returns the number of live sessions using a given worktree path.
func (s *SessionStore) CountSessionsWithWorktree(ctx context.Context, worktreePath string) (int, error) {
	var count int
	err := s.db.GetContext(ctx, &count,
		"SELECT COUNT(*) FROM live_sessions WHERE worktree_path = ? AND status = 'active'", worktreePath)
	return count, err
}

// UpdateWorktreeInfo sets the worktree path and source repo on a live session.
func (s *SessionStore) UpdateWorktreeInfo(ctx context.Context, sessionID, worktreePath, repoPath string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE live_sessions SET worktree_path = ?, worktree_repo = ? WHERE session_id = ?",
		worktreePath, repoPath, sessionID)
	return err
}

// SetIcon sets or clears the emoji icon for a live session.
func (s *SessionStore) SetIcon(ctx context.Context, sessionID string, icon *string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE live_sessions SET icon = ? WHERE session_id = ?",
		icon, sessionID)
	return err
}

// UpdateContextWindow updates the context window and optionally the model for a live session.
func (s *SessionStore) UpdateContextWindow(ctx context.Context, sessionID string, contextWindow int, model string) error {
	if model != "" {
		_, err := s.db.ExecContext(ctx,
			"UPDATE live_sessions SET context_window = ?, model = ? WHERE session_id = ?",
			contextWindow, model, sessionID)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		"UPDATE live_sessions SET context_window = ? WHERE session_id = ?",
		contextWindow, sessionID)
	return err
}

// SetObservedModel records the model a hook reported for a live session (display only).
func (s *SessionStore) SetObservedModel(ctx context.Context, sessionID, model string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE live_sessions SET observed_model = ? WHERE session_id = ?", model, sessionID)
	return err
}

// SetNameColor sets or clears the sidebar name colour for a live session.
func (s *SessionStore) SetNameColor(ctx context.Context, sessionID string, color *string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE live_sessions SET name_color = ? WHERE session_id = ?",
		color, sessionID)
	return err
}

// GetNameColors returns {session_id: name_color} for sessions that have one set.
func (s *SessionStore) GetNameColors(ctx context.Context, sessionIDs []string) (map[string]string, error) {
	if len(sessionIDs) == 0 {
		return map[string]string{}, nil
	}
	query, args, err := sqlx.In(
		"SELECT session_id, name_color FROM live_sessions WHERE session_id IN (?) AND name_color IS NOT NULL AND name_color != ''",
		sessionIDs)
	if err != nil {
		return nil, err
	}
	rows, err := s.reader().QueryxContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var sid, color string
		if err := rows.Scan(&sid, &color); err == nil {
			result[sid] = color
		}
	}
	return result, nil
}

// GetIcons returns {session_id: icon} for sessions that have an icon set.
func (s *SessionStore) GetIcons(ctx context.Context, sessionIDs []string) (map[string]string, error) {
	if len(sessionIDs) == 0 {
		return map[string]string{}, nil
	}
	query, args, err := sqlx.In(
		"SELECT session_id, icon FROM live_sessions WHERE session_id IN (?) AND icon IS NOT NULL AND icon != ''",
		sessionIDs)
	if err != nil {
		return nil, err
	}
	rows, err := s.reader().QueryxContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var sid, icon string
		if err := rows.Scan(&sid, &icon); err == nil {
			result[sid] = icon
		}
	}
	return result, nil
}

// ── Sleep/Wake ────────────────────────────────────────────────────────

// SetBoardSleeping sets is_sleeping for all sessions on a board. Returns the count of affected rows.
func (s *SessionStore) SetBoardSleeping(ctx context.Context, boardName string, sleeping bool) (int, error) {
	val := 0
	if sleeping {
		val = 1
	}
	query := "UPDATE live_sessions SET is_sleeping = ? WHERE board_name = ?"
	if !sleeping {
		query = "UPDATE live_sessions SET is_sleeping = ?, is_archived = 0 WHERE board_name = ?"
	}
	result, err := s.db.ExecContext(ctx, query, val, boardName)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// SetSessionSleeping sets is_sleeping for a single session. Waking also clears
// is_archived: an archived session is always a sleeping one.
func (s *SessionStore) SetSessionSleeping(ctx context.Context, sessionID string, sleeping bool) error {
	return s.setSessionSleeping(ctx, sessionID, sleeping, "")
}

// SleepReasonTerminalGone: Coral put the agent to sleep because its terminal session no
// longer exists (killed, or its tmux server died), not because the user asked (#3344).
const SleepReasonTerminalGone = "terminal_gone"

// SleepReasonCoralStopped: the agent was running when a pty Coral stopped (cleanly, or a crash
// found at the next start), so Coral may wake it again at its next start (#3636). Only Coral's
// own shutdown and startup code writes it; no request can (an Operator's Sleep is reason "").
const SleepReasonCoralStopped = "coral_stopped"

// SleepReasonHooksUpdate: Coral is resuming the agent on the current hook set (#3665): Sleep
// then Wake. Only that route writes it; a crash between the two leaves it for the next start's
// auto-wake, as an agent Coral stopped.
const SleepReasonHooksUpdate = "hooks_update"

// SetSessionSleepingFor puts an agent to sleep for a reason Coral found (#3344), shown in the
// dashboard instead of a plain "Sleeping". Waking clears it.
func (s *SessionStore) SetSessionSleepingFor(ctx context.Context, sessionID, reason string) error {
	return s.setSessionSleeping(ctx, sessionID, true, reason)
}

func (s *SessionStore) setSessionSleeping(ctx context.Context, sessionID string, sleeping bool, reason string) error {
	query := "UPDATE live_sessions SET is_sleeping = 1, sleep_reason = NULLIF(?, '') WHERE session_id = ?"
	args := []any{reason, sessionID}
	code := AutoAcceptAgentSlept
	if !sleeping {
		query = "UPDATE live_sessions SET is_sleeping = 0, is_archived = 0, sleep_reason = NULL WHERE session_id = ?"
		args = []any{sessionID}
		code = AutoAcceptAgentWoken
	}
	// Sleeping stops the agent process and waking starts a new one: either way
	// auto-accept is cleared, so it is never on for a run that did not enable it.
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if err := clearAutoAcceptTx(ctx, tx, sessionID, code, false); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	})
}

// SetSessionArchived archives a session (archived implies sleeping) or
// unarchives it, which leaves it sleeping. It does not stop a running agent;
// the caller does that first.
func (s *SessionStore) SetSessionArchived(ctx context.Context, sessionID string, archived bool) error {
	if !archived {
		_, err := s.db.ExecContext(ctx, "UPDATE live_sessions SET is_archived = 0 WHERE session_id = ?", sessionID)
		return err
	}
	return s.db.WithTx(ctx, func(tx *sqlx.Tx) error {
		if err := clearAutoAcceptTx(ctx, tx, sessionID, AutoAcceptAgentArchived, false); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE live_sessions SET is_sleeping = 1, is_archived = 1 WHERE session_id = ?", sessionID)
		return err
	})
}

// IsSessionArchived reports whether an active live session is archived.
func (s *SessionStore) IsSessionArchived(ctx context.Context, sessionID string) (bool, error) {
	var archived bool
	err := s.reader().GetContext(ctx, &archived,
		"SELECT EXISTS(SELECT 1 FROM live_sessions WHERE session_id = ? AND status = 'active' AND is_archived = 1)", sessionID)
	return archived, err
}

// GetSleepingBoardNames returns board names where ALL sessions are sleeping.
// A board with any awake session is not considered sleeping.
func (s *SessionStore) GetSleepingBoardNames(ctx context.Context) ([]string, error) {
	var names []string
	err := s.reader().SelectContext(ctx, &names,
		`SELECT board_name FROM live_sessions WHERE board_name IS NOT NULL AND status = 'active'
		 GROUP BY board_name HAVING COUNT(*) = SUM(CASE WHEN is_sleeping = 1 THEN 1 ELSE 0 END)`)
	if err != nil {
		return nil, err
	}
	return names, nil
}

// CleanupOrphanedSleeping removes sleeping session rows where an awake
// version with the same display_name and board_name already exists.
// This cleans up duplicates from old wake code that created new sessions.
// Archived rows are kept: the operator put that copy away on purpose.
func (s *SessionStore) CleanupOrphanedSleeping(ctx context.Context) (int, error) {
	now := nowUTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE live_sessions SET status = 'inactive', stopped_at = ? WHERE is_sleeping = 1 AND status = 'active'
		AND session_id IN (
			SELECT sleeping.session_id FROM live_sessions sleeping
			INNER JOIN live_sessions awake
			ON sleeping.board_name = awake.board_name
			AND sleeping.display_name = awake.display_name
			AND sleeping.is_sleeping = 1
			AND sleeping.is_archived = 0
			AND awake.is_sleeping = 0
			AND sleeping.session_id != awake.session_id
			AND sleeping.status = 'active'
			AND awake.status = 'active'
		)`, now)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// GetSessionDuration returns the duration of a session in seconds.
// For active sessions, duration is calculated from created_at to now.
// For stopped sessions, duration is calculated from created_at to stopped_at.
func (s *SessionStore) GetSessionDuration(ctx context.Context, sessionID string) (float64, error) {
	var result struct {
		CreatedAt string  `db:"created_at"`
		StoppedAt *string `db:"stopped_at"`
		Status    string  `db:"status"`
	}
	err := s.reader().GetContext(ctx, &result,
		"SELECT created_at, stopped_at, status FROM live_sessions WHERE session_id = ?", sessionID)
	if err != nil {
		return 0, err
	}

	start, err := time.Parse(time.RFC3339Nano, result.CreatedAt)
	if err != nil {
		start, err = time.Parse("2006-01-02T15:04:05Z", result.CreatedAt)
		if err != nil {
			return 0, fmt.Errorf("parse created_at: %w", err)
		}
	}

	var end time.Time
	if result.StoppedAt != nil && *result.StoppedAt != "" {
		end, err = time.Parse(time.RFC3339Nano, *result.StoppedAt)
		if err != nil {
			end, err = time.Parse("2006-01-02T15:04:05Z", *result.StoppedAt)
			if err != nil {
				return 0, fmt.Errorf("parse stopped_at: %w", err)
			}
		}
	} else {
		end = time.Now().UTC()
	}

	return end.Sub(start).Seconds(), nil
}

// GetRecentStoppedSessions returns recently stopped sessions for timeline display.
func (s *SessionStore) GetRecentStoppedSessions(ctx context.Context, since string, limit int) ([]LiveSession, error) {
	var sessions []LiveSession
	query := `SELECT session_id, agent_type, agent_name, working_dir, display_name,
		 board_name, icon, created_at, stopped_at
		 FROM live_sessions WHERE status = 'inactive' AND stopped_at IS NOT NULL`
	args := []interface{}{}
	if since != "" {
		query += " AND stopped_at >= ?"
		args = append(args, since)
	}
	query += " ORDER BY stopped_at DESC LIMIT ?"
	args = append(args, limit)
	err := s.reader().SelectContext(ctx, &sessions, query, args...)
	return sessions, err
}

// ── Live Session Flags Helper ──────────────────────────────────────────

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

// MarshalCapabilities serializes capabilities to a JSON string pointer for DB storage.
func MarshalCapabilities(caps any) *string {
	if caps == nil {
		return nil
	}
	b, err := json.Marshal(caps)
	if err != nil {
		return nil
	}
	s := string(b)
	return &s
}

// UnmarshalFlags deserializes a JSON flags string to a slice.
func UnmarshalFlags(flags *string) []string {
	if flags == nil || *flags == "" {
		return nil
	}
	var result []string
	if err := json.Unmarshal([]byte(*flags), &result); err != nil {
		return nil
	}
	return result
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

var tsFormats = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
}

func parseTimestamp(ts string) (time.Time, error) {
	for _, fmt := range tsFormats {
		if t, err := time.Parse(fmt, ts); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized timestamp format: %s", ts)
}

func computeDuration(firstTS, lastTS *string) *int {
	if firstTS == nil || lastTS == nil || *firstTS == "" || *lastTS == "" {
		return nil
	}

	a, err := parseTimestamp(*firstTS)
	if err != nil {
		return nil
	}
	b, err := parseTimestamp(*lastTS)
	if err != nil {
		return nil
	}

	delta := int(b.Sub(a).Seconds())
	if delta < 0 {
		delta = 0
	}
	return &delta
}

// ISOFormat matches Python's datetime.isoformat() which includes microseconds and uses +00:00 instead of Z for UTC.
const ISOFormat = "2006-01-02T15:04:05.000000+00:00"

func nowUTC() string {
	return time.Now().UTC().Format(ISOFormat)
}

// NowUTC returns the current time as an ISO 8601 UTC string (exported for use by route handlers).
func NowUTC() string {
	return nowUTC()
}

// SessionLineageItem represents an authenticated session or ancestor in a resume chain.
type SessionLineageItem struct {
	SessionID    string  `db:"session_id" json:"session_id"`
	AgentType    string  `db:"agent_type" json:"agent_type"`
	AgentName    string  `db:"agent_name" json:"agent_name"`
	WorkingDir   string  `db:"working_dir" json:"working_dir"`
	DisplayName  *string `db:"display_name" json:"display_name,omitempty"`
	ResumeFromID *string `db:"resume_from_id" json:"resume_from_id,omitempty"`
	SourceFile   string  `db:"source_file" json:"source_file,omitempty"`
	IsCurrent    bool    `db:"-" json:"is_current"`
}

// GetSessionAncestry returns the caller session and all proven ancestor sessions
// reached via resume_from_id links in live_sessions and session_index.
func (s *SessionStore) GetSessionAncestry(ctx context.Context, sessionID string) ([]SessionLineageItem, error) {
	if sessionID == "" {
		return nil, nil
	}

	ls, err := s.GetLiveSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if ls == nil {
		// Not in live_sessions; check if it exists in session_index
		idx, err := s.GetIndexedSession(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		if idx == nil {
			return nil, nil
		}
		var dispName *string
		if idx.DisplayName != "" {
			dispName = &idx.DisplayName
		}
		return []SessionLineageItem{{
			SessionID:   idx.SessionID,
			AgentType:   idx.SourceType,
			AgentName:   idx.AgentName,
			DisplayName: dispName,
			SourceFile:  idx.SourceFile,
			IsCurrent:   true,
		}}, nil
	}

	rootItem := SessionLineageItem{
		SessionID:    ls.SessionID,
		AgentType:    ls.AgentType,
		AgentName:    ls.AgentName,
		WorkingDir:   ls.WorkingDir,
		DisplayName:  ls.DisplayName,
		ResumeFromID: ls.ResumeFromID,
		IsCurrent:    true,
	}
	if idx, _ := s.GetIndexedSession(ctx, ls.SessionID); idx != nil {
		rootItem.SourceFile = idx.SourceFile
	}
	lineage := []SessionLineageItem{rootItem}

	visited := map[string]bool{ls.SessionID: true}
	currResume := ls.ResumeFromID

	for i := 0; i < 50 && currResume != nil && *currResume != ""; i++ {
		ancestorID := strings.TrimSpace(*currResume)
		if ancestorID == "" || visited[ancestorID] {
			break
		}
		visited[ancestorID] = true

		// 1. Try live_sessions first
		ancLS, err := s.GetLiveSession(ctx, ancestorID)
		if err == nil && ancLS != nil {
			item := SessionLineageItem{
				SessionID:    ancLS.SessionID,
				AgentType:    ancLS.AgentType,
				AgentName:    ancLS.AgentName,
				WorkingDir:   ancLS.WorkingDir,
				DisplayName:  ancLS.DisplayName,
				ResumeFromID: ancLS.ResumeFromID,
				IsCurrent:    false,
			}
			if idx, _ := s.GetIndexedSession(ctx, ancLS.SessionID); idx != nil {
				item.SourceFile = idx.SourceFile
			}
			lineage = append(lineage, item)
			currResume = ancLS.ResumeFromID
			continue
		}

		// 2. Try session_index by exact session_id
		ancIdx, err := s.GetIndexedSession(ctx, ancestorID)
		if err == nil && ancIdx != nil {
			var dispName *string
			if ancIdx.DisplayName != "" {
				dispName = &ancIdx.DisplayName
			}
			item := SessionLineageItem{
				SessionID:   ancIdx.SessionID,
				AgentType:   ancIdx.SourceType,
				AgentName:   ancIdx.AgentName,
				DisplayName: dispName,
				SourceFile:  ancIdx.SourceFile,
				IsCurrent:   false,
			}
			lineage = append(lineage, item)
			break
		}

		// 3. Try session_index by native identifier in source_file
		var matchedIdx SessionIndex
		err = s.db.GetContext(ctx, &matchedIdx,
			`SELECT session_id, source_type, source_file, first_timestamp, last_timestamp,
			        message_count, display_summary, agent_name, display_name, indexed_at, file_mtime
			 FROM session_index WHERE source_file LIKE ? LIMIT 1`, "%"+ancestorID+"%")
		if err == nil && matchedIdx.SessionID != "" {
			var dispName *string
			if matchedIdx.DisplayName != "" {
				dispName = &matchedIdx.DisplayName
			}
			item := SessionLineageItem{
				SessionID:   matchedIdx.SessionID,
				AgentType:   matchedIdx.SourceType,
				AgentName:   matchedIdx.AgentName,
				DisplayName: dispName,
				SourceFile:  matchedIdx.SourceFile,
				IsCurrent:   false,
			}
			lineage = append(lineage, item)
			break
		}

		break
	}

	return lineage, nil
}
