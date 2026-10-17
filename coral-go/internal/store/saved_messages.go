package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Save for Later (#4510): the Operator's bookmarks on messages of an agent's chat or a board.

// SavedMessage is one bookmark.
type SavedMessage struct {
	ID        int64   `db:"id" json:"id"`
	Kind      string  `db:"kind" json:"kind"` // "agent" or "board"
	Ref       string  `db:"ref" json:"ref"`   // the agent's session id, or the board's name
	Key       string  `db:"msg_key" json:"key"`
	AgentType string  `db:"agent_type" json:"agent_type"`
	Title     string  `db:"title" json:"title"` // the agent's or the board's name when it was saved
	Snippet   string  `db:"snippet" json:"snippet"`
	SaidAt    string  `db:"said_at" json:"said_at"`
	CreatedAt string  `db:"created_at" json:"saved_at"`
	DoneAt    *string `db:"done_at" json:"done_at"`
}

// MaxSavedMessages caps how many bookmarks are kept.
const MaxSavedMessages = 2000

// ErrTooManySaved is SaveMessage's error when MaxSavedMessages are already saved.
var ErrTooManySaved = errors.New("too many saved messages")

const savedCols = "id, kind, ref, msg_key, agent_type, title, snippet, said_at, created_at, done_at"

// ListSaved returns the bookmarks, newest saved first. state is "open", "done" or "" (all); kind and ref, when set, narrow it to one conversation.
func (s *SessionStore) ListSaved(ctx context.Context, state, kind, ref string) ([]SavedMessage, error) {
	q := "SELECT " + savedCols + " FROM saved_messages WHERE 1=1"
	var args []any
	switch state {
	case "open":
		q += " AND done_at IS NULL"
	case "done":
		q += " AND done_at IS NOT NULL"
	}
	if kind != "" {
		q += " AND kind = ?"
		args = append(args, kind)
	}
	if ref != "" {
		q += " AND ref = ?"
		args = append(args, ref)
	}
	q += " ORDER BY created_at DESC, id DESC"
	out := []SavedMessage{}
	err := s.reader().SelectContext(ctx, &out, q, args...)
	return out, err
}

// SavedCounts is how many bookmarks are open and how many are done.
func (s *SessionStore) SavedCounts(ctx context.Context) (open, done int, err error) {
	var c struct {
		Open int `db:"open"`
		Done int `db:"done"`
	}
	err = s.reader().GetContext(ctx, &c, "SELECT COALESCE(SUM(done_at IS NULL), 0) AS open, COALESCE(SUM(done_at IS NOT NULL), 0) AS done FROM saved_messages")
	return c.Open, c.Done, err
}

// SaveMessage adds a bookmark; saving a saved message again returns the bookmark that exists (nothing changes).
func (s *SessionStore) SaveMessage(ctx context.Context, m SavedMessage) (SavedMessage, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO saved_messages (kind, ref, msg_key, agent_type, title, snippet, said_at, created_at)
		 SELECT ?, ?, ?, ?, ?, ?, ?, ? WHERE (SELECT COUNT(*) FROM saved_messages) < ? OR EXISTS (SELECT 1 FROM saved_messages WHERE kind = ? AND ref = ? AND msg_key = ?)
		 ON CONFLICT(kind, ref, msg_key) DO NOTHING`,
		m.Kind, m.Ref, m.Key, m.AgentType, m.Title, m.Snippet, m.SaidAt, time.Now().UTC().Format(time.RFC3339Nano),
		MaxSavedMessages, m.Kind, m.Ref, m.Key)
	if err != nil {
		return SavedMessage{}, err
	}
	var out SavedMessage
	err = s.db.GetContext(ctx, &out, "SELECT "+savedCols+" FROM saved_messages WHERE kind = ? AND ref = ? AND msg_key = ?", m.Kind, m.Ref, m.Key)
	if errors.Is(err, sql.ErrNoRows) {
		if n, _ := res.RowsAffected(); n == 0 {
			return SavedMessage{}, ErrTooManySaved
		}
	}
	return out, err
}

// SetSavedDone marks a bookmark done or open again; it reports whether there is such a bookmark.
func (s *SessionStore) SetSavedDone(ctx context.Context, id int64, done bool) (SavedMessage, bool, error) {
	var at any
	if done {
		at = time.Now().UTC().Format(time.RFC3339Nano)
	}
	res, err := s.db.ExecContext(ctx, "UPDATE saved_messages SET done_at = ? WHERE id = ?", at, id)
	if err != nil {
		return SavedMessage{}, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return SavedMessage{}, false, nil
	}
	var out SavedMessage
	err = s.db.GetContext(ctx, &out, "SELECT "+savedCols+" FROM saved_messages WHERE id = ?", id)
	return out, err == nil, err
}

// DeleteSaved removes a bookmark; it reports whether there was one.
func (s *SessionStore) DeleteSaved(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM saved_messages WHERE id = ?", id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
