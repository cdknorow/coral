package store

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/jmoiron/sqlx"
)

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
		color = DefaultTagColor
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
	err := s.reader().SelectContext(ctx, &out, "SELECT path, pinned_at FROM pinned_folders ORDER BY rowid") // the order they were pinned: pinned_at is RFC3339Nano text, which does not sort as time within a second (timestamp_order_lint_test.go)
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
