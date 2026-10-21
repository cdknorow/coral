package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/linktest"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// #3541: a re-keyed legacy board ("." here) moves in the sessions database (live session,
// team) and in the agents' coral-board state files too; a second run changes nothing.
func TestApplyBoardRekeys(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	bpath := filepath.Join(dir, "board.db")
	bs, err := board.NewStoreWithKey(bpath, "")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, ".", "Lead", "Lead", "claude-x", nil, "all")
	require.NoError(t, err)
	require.NoError(t, bs.Close())
	// Reopening re-keys only boards without a re-key yet; force one, as an upgrade does.
	bs, err = board.NewStoreWithKey(bpath, "")
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	rekeys, err := bs.BoardRekeys(ctx)
	require.NoError(t, err)
	id := rekeys["."]
	require.NotEmpty(t, id)

	db, err := storetest.Open(t, filepath.Join(dir, "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	dot := "."
	require.NoError(t, store.NewSessionStore(db).RegisterLiveSession(ctx, &store.LiveSession{SessionID: "s1", AgentType: "claude", AgentName: "lead", WorkingDir: dir, BoardName: &dot}))
	_, err = store.NewTeamStore(db).CreateTeam(ctx, &store.Team{Name: "."})
	require.NoError(t, err)
	state := filepath.Join(dir, "board_state_claude-x.json")
	require.NoError(t, os.WriteFile(state, []byte(`{"project":".","job_title":"Lead"}`), 0o644))
	require.NoError(t, os.WriteFile(state, []byte(`{"project":".","job_title":"Lead","n":12345678901234567890}`), 0o640))
	require.NoError(t, os.Chmod(state, 0o640))
	outside := filepath.Join(t.TempDir(), "elsewhere.json")
	require.NoError(t, os.WriteFile(outside, []byte(`{"project":"."}`), 0o644))
	linktest.Symlink(t, outside, filepath.Join(dir, "board_state_link.json"))
	other := filepath.Join(dir, "board_state_claude-y.json")
	require.NoError(t, os.WriteFile(other, []byte(`{"project":"fine","job_title":"Dev"}`), 0o644))

	for i := 0; i < 2; i++ {
		applyBoardRekeys(ctx, bs, db, dir)
	}
	ls, err := store.NewSessionStore(db).GetLiveSession(ctx, "s1")
	require.NoError(t, err)
	require.NotNil(t, ls.BoardName)
	assert.Equal(t, id, *ls.BoardName)
	team, err := store.NewTeamStore(db).GetTeam(ctx, id)
	require.NoError(t, err)
	assert.NotNil(t, team, "the team row follows")
	var st map[string]any
	data, _ := os.ReadFile(state)
	require.NoError(t, json.Unmarshal(data, &st))
	assert.Equal(t, id, st["project"])
	assert.Equal(t, "Lead", st["job_title"])
	assert.Contains(t, string(data), `"n":12345678901234567890`, "other fields are written back byte-exact")
	if runtime.GOOS != "windows" { // modes mean nothing there: a writable file is 0666 (#3914)
		fi, err := os.Stat(state)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o640), fi.Mode().Perm(), "the file keeps its mode")
	}
	data, _ = os.ReadFile(outside)
	assert.JSONEq(t, `{"project":"."}`, string(data), "a symlinked state file is never followed")
	li, err := os.Lstat(filepath.Join(dir, "board_state_link.json"))
	require.NoError(t, err)
	assert.True(t, li.Mode()&os.ModeSymlink != 0, "and the symlink itself is left alone")
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".board_state_rekey_*"))
	assert.Empty(t, leftovers)
	data, _ = os.ReadFile(other)
	assert.JSONEq(t, `{"project":"fine","job_title":"Dev"}`, string(data), "another board's state file is untouched")
}

func TestRowCount(t *testing.T) {
	assert.Equal(t, "1 row", rowCount(1))
	assert.Equal(t, "2 rows", rowCount(2))
}
