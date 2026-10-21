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
	// Team is the board (the team) the bookmark belongs to: a board message's own board, or the board its agent was on when it was saved ("" when it had none, or was
	// saved before this was kept). Author is who wrote a board message (the name the board shows for them), "" for an agent's message.
	Team   string `db:"team" json:"team"`
	Author string `db:"author" json:"author"`
	// Reason is set by the list route, not kept (#5802): "gone" when the message no longer exists, "not_reachable" when this computer holds no transcript of the agent that
	// wrote it, "" otherwise (and for a bookmark that was not checked).
	Reason string `db:"-" json:"reason"`
}

// SavedScope narrows the list and the counts to where the Operator is looking (#4792): Team keeps the bookmarks of one team (its board's messages and its agents'), Agent one
// agent's (its chat, plus the board messages Author wrote on the Team's board when both are given). The zero value is everything.
type SavedScope struct {
	Team, Agent, Author string
}

func (c SavedScope) where() (string, []any) {
	switch {
	case c.Agent != "" && c.Team != "" && c.Author != "":
		return " AND ((kind = 'agent' AND ref = ?) OR (kind = 'board' AND team = ? AND COALESCE(author, '') = ?))", []any{c.Agent, c.Team, c.Author}
	case c.Agent != "":
		return " AND kind = 'agent' AND ref = ?", []any{c.Agent}
	case c.Team != "":
		return " AND team = ?", []any{c.Team}
	}
	return "", nil
}

// MaxSavedMessages caps how many bookmarks are kept.
const MaxSavedMessages = 2000

// ErrTooManySaved is SaveMessage's error when MaxSavedMessages are already saved.
var ErrTooManySaved = errors.New("too many saved messages")

const savedCols = "id, kind, ref, msg_key, agent_type, title, snippet, said_at, created_at, done_at, team, COALESCE(author, '') AS author"

// ListSaved returns the bookmarks, newest saved first. state is "open", "done" or "" (all); kind and ref, when set, narrow it to one conversation; scope to where the
// Operator is looking.
func (s *SessionStore) ListSaved(ctx context.Context, state, kind, ref string, scope SavedScope) ([]SavedMessage, error) {
	q := "SELECT " + savedCols + " FROM saved_messages WHERE 1=1"
	var args []any
	sw, sa := scope.where()
	q += sw
	args = append(args, sa...)
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

// SavedCounts is how many bookmarks of the scope are open and how many are done.
func (s *SessionStore) SavedCounts(ctx context.Context, scope SavedScope) (open, done int, err error) {
	var c struct {
		Open int `db:"open"`
		Done int `db:"done"`
	}
	sw, args := scope.where()
	err = s.reader().GetContext(ctx, &c, "SELECT COALESCE(SUM(done_at IS NULL), 0) AS open, COALESCE(SUM(done_at IS NOT NULL), 0) AS done FROM saved_messages WHERE 1=1"+sw, args...)
	return c.Open, c.Done, err
}

// SaveMessage adds a bookmark; saving a saved message again returns the bookmark that exists (nothing changes).
func (s *SessionStore) SaveMessage(ctx context.Context, m SavedMessage) (SavedMessage, error) {
	var author any // NULL: not known (a board message's author is then looked up when asked for)
	if m.Author != "" {
		author = m.Author
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO saved_messages (kind, ref, msg_key, agent_type, title, snippet, said_at, created_at, team, author)
		 SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ? WHERE (SELECT COUNT(*) FROM saved_messages) < ? OR EXISTS (SELECT 1 FROM saved_messages WHERE kind = ? AND ref = ? AND msg_key = ?)
		 ON CONFLICT(kind, ref, msg_key) DO NOTHING`,
		m.Kind, m.Ref, m.Key, m.AgentType, m.Title, m.Snippet, m.SaidAt, time.Now().UTC().Format(time.RFC3339Nano), m.Team, author,
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

// GetSaved returns one bookmark by its id; found is false when there is none.
func (s *SessionStore) GetSaved(ctx context.Context, id int64) (SavedMessage, bool, error) {
	var out SavedMessage
	err := s.reader().GetContext(ctx, &out, "SELECT "+savedCols+" FROM saved_messages WHERE id = ?", id)
	if errors.Is(err, sql.ErrNoRows) {
		return SavedMessage{}, false, nil
	}
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

// UnknownSavedAuthors lists the board bookmarks whose author is not known yet (saved before it was kept): their id, board and message id, for the board to answer.
func (s *SessionStore) UnknownSavedAuthors(ctx context.Context, team string) ([]SavedMessage, error) {
	out := []SavedMessage{}
	err := s.reader().SelectContext(ctx, &out, "SELECT "+savedCols+" FROM saved_messages WHERE kind = 'board' AND author IS NULL AND team = ?", team)
	return out, err
}

// SetSavedAuthor records who wrote a board bookmark's message ("" when the message is gone: it is not asked again).
func (s *SessionStore) SetSavedAuthor(ctx context.Context, id int64, author string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE saved_messages SET author = ? WHERE id = ? AND author IS NULL", author, id)
	return err
}
