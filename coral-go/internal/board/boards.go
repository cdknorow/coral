package board

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/jmoiron/sqlx"
)

// Boards as their own entities (#3536, design #3534 B, operator-approved). A board has an id
// (the key every board table already uses: board_subscribers.project, board_messages.project,
// board_tasks.board_id, waits, settings, ...) and a label (its display name, not unique). At
// upgrade every existing board keeps its name as its id, so no row is rewritten; boards
// created by a launch get a fresh "b-" id. Two boards may share a label; renaming changes
// only the label.

// Board is one row of the boards table.
type Board struct {
	ID        string `db:"id" json:"board_id"`
	Label     string `db:"label" json:"board_label"`
	CreatedAt string `db:"created_at" json:"created_at"`
	CreatedBy string `db:"created_by" json:"created_by"`
}

// ErrAmbiguousBoard: a key that is no board's id names more than one board's label.
var ErrAmbiguousBoard = errors.New("ambiguous_board")

// ErrBoardNotFound: no board has that id or label.
var ErrBoardNotFound = errors.New("board_not_found")

func (s *Store) ensureBoardsSchema(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS boards (
			id         TEXT PRIMARY KEY,
			label      TEXT NOT NULL,
			created_at TEXT NOT NULL,
			created_by TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS idx_boards_label ON boards(label);
	`); err != nil {
		return err
	}
	// Every board that exists today becomes a row whose id and label are its name. Idempotent:
	// rows already there are kept (a rename survives a restart).
	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO boards (id, label, created_at)
		SELECT key, key, MIN(at) FROM (
			SELECT project AS key, subscribed_at AS at FROM board_subscribers
			UNION ALL SELECT project, created_at FROM board_messages
			UNION ALL SELECT board_id, created_at FROM board_tasks
		) WHERE key <> '' GROUP BY key`)
	return err
}

// UsableBoardKey reports whether key can be a board id in a URL path: not empty, not "." or
// "..", no '/', and no control or bidi-formatting character. A legacy board whose name fails
// this is reachable only from lists (its id is not usable in a URL).
func UsableBoardKey(key string) bool {
	if key == "" || key == "." || key == ".." || strings.ContainsRune(key, '/') || len(key) > 256 {
		return false
	}
	for _, r := range key {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || r == '‎' || r == '‏' || (r >= '⁦' && r <= '⁩') || (r >= '‪' && r <= '‮') {
			return false
		}
	}
	return true
}

// ensureBoardRow records key as a board (id = label = key) the first time anything uses it,
// as a subscribe, a message or a task did before boards had rows.
func (s *Store) ensureBoardRow(ctx context.Context, key string) {
	if key == "" {
		return
	}
	_, _ = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO boards (id, label, created_at) VALUES (?, ?, ?)`, key, key, nowUTC())
}

// GetBoard returns the board with this exact id.
func (s *Store) GetBoard(ctx context.Context, id string) (*Board, error) {
	var b Board
	err := s.reader().GetContext(ctx, &b, `SELECT id, label, created_at, created_by FROM boards WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBoardNotFound
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// BoardsLabelled returns every board in use (a subscriber, a message or a task) whose label
// is label (exact), oldest first. A row a failed launch left behind, with nothing on it,
// never makes a name taken or ambiguous.
func (s *Store) BoardsLabelled(ctx context.Context, label string) ([]Board, error) {
	var out []Board
	err := s.reader().SelectContext(ctx, &out, `SELECT b.id, b.label, b.created_at, b.created_by FROM boards b WHERE b.label = ?
		AND (EXISTS (SELECT 1 FROM board_subscribers WHERE project = b.id)
		  OR EXISTS (SELECT 1 FROM board_messages WHERE project = b.id)
		  OR EXISTS (SELECT 1 FROM board_tasks WHERE board_id = b.id))
		ORDER BY b.created_at, b.id`, label)
	return out, err
}

// ResolveBoard turns a key from a URL, a CLI or a launch into a board id: an exact id match
// always wins (a legacy board "sprint" stays "sprint" after a new board takes that label);
// otherwise a label that names exactly one board. More than one: ErrAmbiguousBoard with the
// candidates. None: ErrBoardNotFound.
func (s *Store) ResolveBoard(ctx context.Context, key string) (string, []Board, error) {
	if b, err := s.GetBoard(ctx, key); err == nil {
		return b.ID, nil, nil
	} else if !errors.Is(err, ErrBoardNotFound) {
		return "", nil, err
	}
	named, err := s.BoardsLabelled(ctx, key)
	if err != nil {
		return "", nil, err
	}
	switch len(named) {
	case 0:
		return "", nil, ErrBoardNotFound
	case 1:
		return named[0].ID, named, nil
	}
	return "", named, ErrAmbiguousBoard
}

// newBoardID is "b-" and 10 lowercase base32 characters (50 random bits).
func newBoardID() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz234567"
	buf := make([]byte, 10)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i := range buf {
		buf[i] = alphabet[buf[i]&31]
	}
	return "b-" + string(buf), nil
}

// CreateBoard makes a NEW board with a fresh id and this label (two boards may share one).
// The id never equals an existing board's id or label, nor a key some board table uses
// without a row yet.
func (s *Store) CreateBoard(ctx context.Context, label, createdBy string) (*Board, error) {
	for attempt := 0; attempt < 8; attempt++ {
		id, err := newBoardID()
		if err != nil {
			return nil, err
		}
		var taken int
		if err := s.db.GetContext(ctx, &taken, `SELECT
			(SELECT COUNT(*) FROM boards WHERE id = ?1 OR label = ?1) +
			(SELECT COUNT(*) FROM board_subscribers WHERE project = ?1) +
			(SELECT COUNT(*) FROM board_messages WHERE project = ?1) +
			(SELECT COUNT(*) FROM board_tasks WHERE board_id = ?1)`, id); err != nil {
			return nil, err
		}
		if taken > 0 {
			continue
		}
		b := Board{ID: id, Label: label, CreatedAt: nowUTC(), CreatedBy: createdBy}
		res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO boards (id, label, created_at, created_by) VALUES (?, ?, ?, ?)`, b.ID, b.Label, b.CreatedAt, b.CreatedBy)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return &b, nil
		}
	}
	return nil, fmt.Errorf("could not allocate a board id")
}

// RenameBoard changes a board's label (its id, and everything keyed by it, stays).
func (s *Store) RenameBoard(ctx context.Context, id, label string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE boards SET label = ? WHERE id = ?`, label, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrBoardNotFound
	}
	return nil
}

// BoardLabels maps each id to its label; an id without a row maps to itself.
func (s *Store) BoardLabels(ctx context.Context, ids []string) map[string]string {
	out := make(map[string]string, len(ids))
	var want []string
	for _, id := range ids {
		if _, seen := out[id]; !seen && id != "" {
			out[id] = id
			want = append(want, id)
		}
	}
	for start := 0; start < len(want); start += 500 {
		end := min(start+500, len(want))
		q, args, err := sqlx.In(`SELECT id, label FROM boards WHERE id IN (?)`, want[start:end])
		if err != nil {
			continue
		}
		var rows []struct {
			ID    string `db:"id"`
			Label string `db:"label"`
		}
		if s.reader().SelectContext(context.Background(), &rows, s.reader().Rebind(q), args...) == nil {
			for _, r := range rows {
				out[r.ID] = r.Label
			}
		}
	}
	return out
}

// BoardLabel is BoardLabels for one id.
func (s *Store) BoardLabel(ctx context.Context, id string) string {
	return s.BoardLabels(ctx, []string{id})[id]
}

// BoardMembers counts a board's active subscribers.
func (s *Store) BoardMembers(ctx context.Context, id string) int {
	var n int
	_ = s.reader().GetContext(ctx, &n, `SELECT COUNT(*) FROM board_subscribers WHERE project = ? AND is_active = 1`, id)
	return n
}
