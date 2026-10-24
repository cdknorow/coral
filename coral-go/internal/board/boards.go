package board

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/subgentic/coral/internal/secfile"
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
	// UID is the board's own random UUID (canonical lower-case), made when the board is created and never changed, not even by a re-key of its id (#7087): the stable name a reference to this
	// board from another machine is made of. nil for a board made before it existed, which is not backfilled: such a board is not cross-linkable.
	UID *string `db:"board_uid" json:"board_uid"`
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
	if err != nil {
		return err
	}
	// A re-key that fails never keeps the boards from opening (Security's #3541 review): the
	// board stays as it was (listed, not openable) and the next start tries again.
	if err := s.rekeyUnusableBoards(ctx); err != nil {
		slog.Warn("[boards] re-keying a board whose name can't be an id failed; it stays as it was", "error", err)
	}
	return nil
}

// boardIDShape is the shape of a generated board id.
var boardIDShape = regexp.MustCompile(`^b-[a-z2-7]{10}$`)

// IsGeneratedBoardID reports whether id has the shape of an id Coral generated ("b-" and ten base32 characters): the only boards Coral Hub can link. A board made before ids is keyed by its
// NAME, which is not one (#5572).
func IsGeneratedBoardID(id string) bool { return boardIDShape.MatchString(id) }

// LabelProblem is NewNameProblem for a board's label (a new board's name, or a rename),
// plus: a label may not look like a generated id (QA's #3536 review), so a label can never
// pass for a new-style id. A label equal to a legacy board's name is allowed: it is just a
// shared label ("a second sprint next to the old sprint"), and an exact id still wins.
// "" when it's fine.
func (s *Store) LabelProblem(label string) string {
	if p := NewNameProblem(label); p != "" {
		return p
	}
	if boardIDShape.MatchString(label) {
		return "a board name can't look like a board id (b- and 10 letters or digits); choose another name"
	}
	return ""
}

// UsableBoardKey reports whether key can be a board id in a URL path: not empty, not "." or
// "..", no '/', '\\' or ':' (a key also names a worktree folder and files; on Windows those are
// separators or a drive, #3602 W25), and no control or bidi-formatting character. A legacy
// board whose name fails this is reachable only from lists (its id is not usable in a URL).
func UsableBoardKey(key string) bool {
	if key == "" || key == "." || key == ".." || strings.ContainsAny(key, "/\\:") || len(key) > 256 {
		return false
	}
	for _, r := range key {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || r == '\u200e' || r == '\u200f' || (r >= '\u2066' && r <= '\u2069') || (r >= '\u202a' && r <= '\u202e') {
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
	err := getHot(ctx, s.hot().boardByID, s.reader(), &b, sqlBoardByID, id)
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
	err := selectHot(ctx, s.hot().boardsLabelled, s.reader(), &out, sqlBoardsLabelled, label)
	return out, err
}

// BoardsLabelledForLaunch is BoardsLabelled for a launch's name check: a board started in
// the last 10 minutes counts too while it is still empty, since a launch that just created
// it hasn't subscribed its agents yet. An older empty row (a launch that failed) still never
// makes a name taken.
func (s *Store) BoardsLabelledForLaunch(ctx context.Context, label string) ([]Board, error) {
	since := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)
	var out []Board
	err := s.db.SelectContext(ctx, &out, `SELECT b.id, b.label, b.created_at, b.created_by, b.board_uid FROM boards b WHERE b.label = ?
		AND (b.created_at >= ?
		  OR EXISTS (SELECT 1 FROM board_subscribers WHERE project = b.id)
		  OR EXISTS (SELECT 1 FROM board_messages WHERE project = b.id)
		  OR EXISTS (SELECT 1 FROM board_tasks WHERE board_id = b.id))
		ORDER BY b.created_at, b.id`, label, since)
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
		uid := uuid.NewString()
		b := Board{ID: id, Label: label, CreatedAt: nowUTC(), CreatedBy: createdBy, UID: &uid}
		res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO boards (id, label, created_at, created_by, board_uid) VALUES (?, ?, ?, ?, ?)`, b.ID, b.Label, b.CreatedAt, b.CreatedBy, uid)
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

// boardKeyColumns are every column in messageboard.db that holds a board's key.
var boardKeyColumns = [][2]string{
	{"board_subscribers", "project"}, {"board_messages", "project"}, {"board_reminders", "project"},
	{"board_groups", "project"}, {"board_tasks", "board_id"}, {"task_dependencies", "blocked_by_board_id"},
	{"board_settings", "project"}, {"hub_operator_actions", "project"}, {"board_waits", "project"},
	{"board_working_modes", "board_id"}, {"board_workflow_presets", "board_id"}, {"board_goals", "board_id"}, {"board_goal_meta", "board_id"}, {"board_settings_log", "board_id"}, {"cross_team_switch_log", "project"},
}

// rekeyUnusableBoards gives every board whose key can't be an id in a URL ("." , "..", a
// '/', a control or bidi character; UsableBoardKey) a fresh b- id, keeping its name as the
// label (#3541, design v2 point 3), so the dashboard can open it. Every board table is
// re-keyed in one transaction per board, and the change is recorded in board_rekeys, which
// BoardRekeys returns so the server can apply it to the sessions database and the agents'
// state files at every start (idempotent).
func (s *Store) rekeyUnusableBoards(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS board_rekeys (
		old_id TEXT PRIMARY KEY, new_id TEXT NOT NULL, rekeyed_at TEXT NOT NULL)`); err != nil {
		return err
	}
	var ids []string
	if err := s.db.SelectContext(ctx, &ids, `SELECT id FROM boards`); err != nil {
		return err
	}
	backedUp := false
	for _, old := range ids {
		if UsableBoardKey(old) {
			continue
		}
		if !backedUp {
			s.backupBeforeRekey(ctx)
			backedUp = true
		}
		if err := s.rekeyBoard(ctx, old); err != nil {
			return fmt.Errorf("re-key board %q: %w", old, err)
		}
	}
	return nil
}

func (s *Store) rekeyBoard(ctx context.Context, old string) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id string
	for attempt := 0; ; attempt++ {
		if attempt == 8 {
			return fmt.Errorf("could not allocate a board id")
		}
		if id, err = newBoardID(); err != nil {
			return err
		}
		var taken int
		if err := tx.GetContext(ctx, &taken, `SELECT (SELECT COUNT(*) FROM boards WHERE id = ?1 OR label = ?1) +
			(SELECT COUNT(*) FROM board_rekeys WHERE old_id = ?1 OR new_id = ?1)`, id); err != nil {
			return err
		}
		if taken == 0 {
			break
		}
	}
	for _, tc := range boardKeyColumns {
		if _, err := tx.ExecContext(ctx, `UPDATE `+tc[0]+` SET `+tc[1]+` = ? WHERE `+tc[1]+` = ?`, id, old); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE boards SET id = ? WHERE id = ?`, id, old); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO board_rekeys (old_id, new_id, rekeyed_at) VALUES (?, ?, ?)`, old, id, nowUTC()); err != nil {
		return err
	}
	return tx.Commit()
}

// backupBeforeRekey copies messageboard.db next to itself once (messageboard.db.pre-rekey,
// with VACUUM INTO, which is consistent under WAL) before an install's first re-key, so the
// change can be undone by hand. A failure is logged and doesn't stop the re-key.
func (s *Store) backupBeforeRekey(ctx context.Context) {
	var file string
	if err := s.db.GetContext(ctx, &file, `SELECT file FROM pragma_database_list WHERE name = 'main'`); err != nil || file == "" {
		return
	}
	dst := file + ".pre-rekey"
	if _, err := os.Stat(dst); err == nil {
		return
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		slog.Warn("[boards] backup before re-keying failed; re-keying anyway", "error", err)
		return
	}
	// The copy holds the same data: give it the database file's own mode (on Windows, where the
	// mode says nothing, make it private to this user).
	if fi, err := os.Stat(file); err == nil {
		_ = os.Chmod(dst, fi.Mode().Perm())
	}
	_ = secfile.Protect(dst)
	slog.Info("[boards] saved a copy of the board database before re-keying", "copy", filepath.Base(dst))
}

// BoardRekeys returns every re-keyed board, old key to new id.
func (s *Store) BoardRekeys(ctx context.Context) (map[string]string, error) {
	var rows []struct {
		Old string `db:"old_id"`
		New string `db:"new_id"`
	}
	if err := s.reader().SelectContext(ctx, &rows, `SELECT old_id, new_id FROM board_rekeys`); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Old] = r.New
	}
	return out, nil
}
