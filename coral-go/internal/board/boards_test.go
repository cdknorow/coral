package board

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3536: at upgrade every existing board becomes a row with id = label = its name (nothing
// rewritten); idempotent, and a rename survives a restart.
func TestBoardsMigrationKeepsNamesAsIDs(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "board.db")
	s, err := NewStore(path)
	require.NoError(t, err)
	_, err = s.Subscribe(ctx, "Coral Go-To-Market Team", "Lead", "Lead", "", nil, nil, "all")
	require.NoError(t, err)
	_, err = s.PostMessage(ctx, "alpha", "Dev", "hi", nil)
	require.NoError(t, err)
	_, err = s.CreateTask(ctx, "tasks-only", "t", "", "medium", "Operator")
	require.NoError(t, err)
	// As a database from before #3536: no boards rows.
	_, err = s.db.Exec(`DELETE FROM boards`)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	s, err = NewStore(path)
	require.NoError(t, err)
	for _, name := range []string{"Coral Go-To-Market Team", "alpha", "tasks-only"} {
		b, err := s.GetBoard(ctx, name)
		require.NoError(t, err, name)
		assert.Equal(t, name, b.ID)
		assert.Equal(t, name, b.Label)
		assert.NotEmpty(t, b.CreatedAt)
	}
	require.NoError(t, s.RenameBoard(ctx, "alpha", "Alpha Team"))
	require.NoError(t, s.Close())
	s, err = NewStore(path)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	b, err := s.GetBoard(ctx, "alpha")
	require.NoError(t, err)
	assert.Equal(t, "Alpha Team", b.Label, "the rename survives a restart (the migration doesn't overwrite)")
	var n int
	require.NoError(t, s.db.Get(&n, `SELECT COUNT(*) FROM boards`))
	assert.Equal(t, 3, n)
}

func TestBoardsCreateResolveRename(t *testing.T) {
	ctx := context.Background()
	s, err := NewStore(filepath.Join(t.TempDir(), "board.db"))
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	// A legacy board "sprint" (id = name).
	_, err = s.Subscribe(ctx, "sprint", "Lead", "Lead", "", nil, nil, "all")
	require.NoError(t, err)

	b1, err := s.CreateBoard(ctx, "sprint", "Operator")
	require.NoError(t, err)
	assert.Regexp(t, regexp.MustCompile(`^b-[a-z2-7]{10}$`), b1.ID)
	assert.True(t, NewNamePattern.MatchString(b1.ID), "a new id is itself a valid name")
	b2, err := s.CreateBoard(ctx, "sprint", "Operator")
	require.NoError(t, err)
	assert.NotEqual(t, b1.ID, b2.ID)
	for _, b := range []*Board{b1, b2} {
		_, err = s.Subscribe(ctx, b.ID, "Dev", "Dev", "", nil, nil, "all")
		require.NoError(t, err)
	}

	// An exact id always wins: "sprint" stays the legacy board.
	id, _, err := s.ResolveBoard(ctx, "sprint")
	require.NoError(t, err)
	assert.Equal(t, "sprint", id)
	id, _, err = s.ResolveBoard(ctx, b1.ID)
	require.NoError(t, err)
	assert.Equal(t, b1.ID, id)

	// A label shared by two boards (neither's id) is ambiguous.
	require.NoError(t, s.RenameBoard(ctx, b1.ID, "launch"))
	require.NoError(t, s.RenameBoard(ctx, b2.ID, "launch"))
	_, cands, err := s.ResolveBoard(ctx, "launch")
	assert.True(t, errors.Is(err, ErrAmbiguousBoard))
	assert.Len(t, cands, 2)
	// A label on one board resolves to it.
	require.NoError(t, s.RenameBoard(ctx, b2.ID, "other"))
	id, _, err = s.ResolveBoard(ctx, "launch")
	require.NoError(t, err)
	assert.Equal(t, b1.ID, id)
	// A board row with nothing on it (a launch that failed after creating it) never makes a
	// name taken or ambiguous.
	empty, err := s.CreateBoard(ctx, "launch", "Operator")
	require.NoError(t, err)
	id, _, err = s.ResolveBoard(ctx, "launch")
	require.NoError(t, err)
	assert.Equal(t, b1.ID, id, "the empty %s doesn't count", empty.ID)
	_, _, err = s.ResolveBoard(ctx, "nothing")
	assert.True(t, errors.Is(err, ErrBoardNotFound))
	assert.True(t, errors.Is(s.RenameBoard(ctx, "nothing", "x"), ErrBoardNotFound))

	labels := s.BoardLabels(ctx, []string{b1.ID, "sprint", "no-row", b1.ID})
	assert.Equal(t, map[string]string{b1.ID: "launch", "sprint": "sprint", "no-row": "no-row"}, labels)
	assert.Equal(t, 1, s.BoardMembers(ctx, "sprint"))
}

// A new id never equals an existing id or label, nor a key a board table uses.
func TestNewBoardIDAvoidsTakenKeys(t *testing.T) {
	ctx := context.Background()
	s, err := NewStore(filepath.Join(t.TempDir(), "board.db"))
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		b, err := s.CreateBoard(ctx, "x", "")
		require.NoError(t, err)
		assert.False(t, seen[b.ID])
		seen[b.ID] = true
	}
}

func TestUsableBoardKey(t *testing.T) {
	for _, ok := range []string{"sprint", "Coral Go-To-Market Team", "b-7k3m9q2x4p", "team.v2"} {
		assert.True(t, UsableBoardKey(ok), ok)
	}
	for _, bad := range []string{"", ".", "..", "a/b", "x\x00", "x\ny", "evil‮txt", "a⁦b", "a‏b"} {
		assert.False(t, UsableBoardKey(bad), "%q", bad)
	}
}

// A launch's name check counts a board started in the last 10 minutes even while it is
// still empty (its launch is subscribing the agents), but not an older empty row.
func TestBoardsLabelledForLaunch(t *testing.T) {
	ctx := context.Background()
	s, err := NewStore(filepath.Join(t.TempDir(), "board.db"))
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	fresh, err := s.CreateBoard(ctx, "team", "Operator")
	require.NoError(t, err)
	got, err := s.BoardsLabelledForLaunch(ctx, "team")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, fresh.ID, got[0].ID)
	_, err = s.db.Exec(`UPDATE boards SET created_at = '2026-01-01T00:00:00Z' WHERE id = ?`, fresh.ID)
	require.NoError(t, err)
	got, err = s.BoardsLabelledForLaunch(ctx, "team")
	require.NoError(t, err)
	assert.Empty(t, got, "an old empty row (a failed launch) doesn't take the name")
}

// #3541: a legacy board whose name can't be an id in a URL ("." here) gets a fresh b- id at
// startup, keeping its name as the label; every board table follows; it's recorded for the
// server; and a second start changes nothing.
func TestUnusableLegacyBoardIsRekeyed(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "board.db")
	s, err := NewStore(path)
	require.NoError(t, err)
	_, err = s.Subscribe(ctx, ".", "Lead", "Lead", "claude-x", nil, nil, "all")
	require.NoError(t, err)
	_, err = s.PostMessage(ctx, ".", "Lead", "old message", nil)
	require.NoError(t, err)
	task, err := s.CreateTask(ctx, ".", "old task", "", "medium", "Lead")
	require.NoError(t, err)
	_, err = s.SetWorkingMode(ctx, ".", WorkingMode{Mode: "none", CustomInstructions: "KEEP"})
	require.NoError(t, err)
	_, err = s.Subscribe(ctx, "fine", "Dev", "Dev", "", nil, nil, "all")
	require.NoError(t, err)
	// As before #3536: no boards rows, no re-key yet.
	_, err = s.db.Exec(`DELETE FROM boards; DELETE FROM board_rekeys`)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	s, err = NewStore(path)
	require.NoError(t, err)
	rekeys, err := s.BoardRekeys(ctx)
	require.NoError(t, err)
	id := rekeys["."]
	require.Regexp(t, regexp.MustCompile(`^b-[a-z2-7]{10}$`), id)
	assert.Len(t, rekeys, 1, "a usable name isn't touched")
	b, err := s.GetBoard(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, ".", b.Label, "the name is kept as the label")
	_, err = s.GetBoard(ctx, ".")
	assert.True(t, errors.Is(err, ErrBoardNotFound))
	subs, err := s.ListSubscribers(ctx, id)
	require.NoError(t, err)
	assert.Len(t, subs, 1)
	got, err := s.GetTask(ctx, id, task.ID)
	require.NoError(t, err)
	assert.Equal(t, id, got.BoardID)
	wm, err := s.GetWorkingMode(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "KEEP", wm.CustomInstructions)
	for _, tc := range boardKeyColumns {
		var n int
		require.NoError(t, s.db.Get(&n, `SELECT COUNT(*) FROM `+tc[0]+` WHERE `+tc[1]+` = '.'`))
		assert.Zero(t, n, "%s.%s still has the old key", tc[0], tc[1])
	}
	require.NoError(t, s.Close())

	s, err = NewStore(path)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	again, err := s.BoardRekeys(ctx)
	require.NoError(t, err)
	assert.Equal(t, rekeys, again, "a second start re-keys nothing")
	_, err = s.GetBoard(ctx, id)
	assert.NoError(t, err)
}

// Security's #3541 review: a copy of the database is saved once, before the first real
// re-key (none when nothing needs re-keying), and a re-key that fails never keeps the board
// store from opening; the board stays as it was.
func TestRekeyBackupAndFailureAreSafe(t *testing.T) {
	ctx := context.Background()
	// Nothing to re-key: no copy.
	clean := filepath.Join(t.TempDir(), "board.db")
	s, err := NewStore(clean)
	require.NoError(t, err)
	_, err = s.Subscribe(ctx, "fine", "Dev", "Dev", "", nil, nil, "all")
	require.NoError(t, err)
	require.NoError(t, s.Close())
	s, err = NewStore(clean)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	_, err = os.Stat(clean + ".pre-rekey")
	assert.True(t, os.IsNotExist(err), "no copy without a re-key")

	// A re-key that fails: the store still opens, the board keeps its key, no record.
	path := filepath.Join(t.TempDir(), "board.db")
	s, err = NewStore(path)
	require.NoError(t, err)
	_, err = s.Subscribe(ctx, "..", "Lead", "Lead", "", nil, nil, "all")
	require.NoError(t, err)
	_, err = s.db.Exec(`CREATE TRIGGER block_rekey BEFORE UPDATE ON boards BEGIN SELECT RAISE(ABORT, 'blocked'); END`)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	require.NoError(t, os.Chmod(path, 0o600))
	s, err = NewStore(path)
	require.NoError(t, err, "a failed re-key never stops the store from opening")
	_, err = s.GetBoard(ctx, "..")
	assert.NoError(t, err, "the board stays as it was")
	rekeys, err := s.BoardRekeys(ctx)
	require.NoError(t, err)
	assert.Empty(t, rekeys)
	_, err = os.Stat(path + ".pre-rekey")
	require.NoError(t, err, "the copy was saved before the attempt")
	bfi, err := os.Stat(path + ".pre-rekey")
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), bfi.Mode().Perm(), "the copy has the database file's mode")

	// Unblocked: the next start re-keys it, and the copy isn't replaced.
	before, _ := os.Stat(path + ".pre-rekey")
	_, err = s.db.Exec(`DROP TRIGGER block_rekey`)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	s, err = NewStore(path)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	rekeys, err = s.BoardRekeys(ctx)
	require.NoError(t, err)
	assert.Contains(t, rekeys, "..")
	after, _ := os.Stat(path + ".pre-rekey")
	assert.Equal(t, before.ModTime(), after.ModTime(), "the first copy is kept")
}
