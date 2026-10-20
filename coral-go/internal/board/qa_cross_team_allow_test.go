package board

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// QA (#5508): the properties the allowlist stands on, from the angles the feature's own tests leave: an upgrade of a database made before the column existed, the log of a board that is
// re-keyed, and an id that is never reissued across many delete and create rounds.

// A database from before this release has boards and no cross_team_id: opening it gives every board a distinct id, keeps the ids on a second open, and gives a board made afterwards one too.
func TestQA_UpgradeOfADatabaseWithoutTheColumnBackfillsAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "board_old.db")
	s, err := NewStoreWithKey(path, "")
	require.NoError(t, err)
	for _, n := range []string{"alpha", "beta", "gamma"} {
		_, err := sub(s, ctx, n, "Orchestrator", "Orchestrator")
		require.NoError(t, err)
	}
	// Make it look like the old schema: no trigger, no index, no column.
	for _, stmt := range []string{`DROP TRIGGER boards_cross_team_id`, `DROP INDEX idx_boards_cross_team_id`, `ALTER TABLE boards DROP COLUMN cross_team_id`, `DROP TABLE board_settings_log`} {
		_, err := s.db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}
	require.NoError(t, s.Close())

	first := map[string]string{}
	s2, err := NewStoreWithKey(path, "")
	require.NoError(t, err)
	seen := map[string]bool{}
	for _, n := range []string{"alpha", "beta", "gamma"} {
		id, ok, err := s2.CrossTeamID(ctx, n)
		require.NoError(t, err)
		require.True(t, ok, n)
		assert.Regexp(t, `^x-[a-z2-7]{16}$`, id)
		assert.False(t, seen[id], "distinct")
		seen[id], first[n] = true, id
	}
	_, err = sub(s2, ctx, "delta", "Orchestrator", "Orchestrator") // made after the upgrade: the trigger gives it one
	require.NoError(t, err)
	_, ok, err := s2.CrossTeamID(ctx, "delta")
	require.NoError(t, err)
	assert.True(t, ok)
	require.NoError(t, s2.Close())

	s3, err := NewStoreWithKey(path, "") // a restart: nothing is re-issued
	require.NoError(t, err)
	defer s3.Close()
	for n, want := range first {
		got, ok, err := s3.CrossTeamID(ctx, n)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, want, got, "%s keeps its id across a restart", n)
	}
	list, err := s3.GetBoardSettings(ctx, "alpha")
	require.NoError(t, err)
	assert.Empty(t, list.CrossTeamAllowed, "an upgraded board starts with an empty list (default deny)")
}

// A deleted board's id is never reissued: fifty rounds of delete and create of the same name all give a new id, and none repeats.
func TestQA_FiftyDeleteAndCreateRoundsNeverReuseAnID(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	seen := map[string]int{}
	for i := 0; i < 50; i++ {
		_, err := sub(s, ctx, "phoenix", "Orchestrator", "Orchestrator")
		require.NoError(t, err)
		id, ok, err := s.CrossTeamID(ctx, "phoenix")
		require.NoError(t, err)
		require.True(t, ok)
		if prev, dup := seen[id]; dup {
			t.Fatalf("round %d reissued the id of round %d", i, prev)
		}
		seen[id] = i
		require.NoError(t, s.DeleteProject(ctx, "phoenix"))
	}
	assert.Len(t, seen, 50)
}

// A board that is re-keyed (an unusable key gets a fresh b- id) keeps what the list wrote about it: its settings log follows the board, and so does its cross_team_id.
func TestQA_ARekeyedBoardKeepsItsLogAndItsCrossTeamID(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	const old = "a/b" // not a usable key: re-keyed at the next start
	_, err := sub(s, ctx, old, "Orchestrator", "Orchestrator")
	require.NoError(t, err)
	before, ok, err := s.CrossTeamID(ctx, old)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, s.AppendEditLog(ctx, old, "Operator", "cross_team_allowed", "[]", `[{"server":"local","cross_team_id":"x-aaaaaaaaaaaaaaaa"}]`))
	require.NoError(t, s.rekeyUnusableBoards(ctx))

	rekeys, err := s.BoardRekeys(ctx)
	require.NoError(t, err)
	newID := rekeys[old]
	require.NotEmpty(t, newID, "the board was re-keyed")
	after, ok, err := s.CrossTeamID(ctx, newID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, before, after, "the cross-team id is the board's, not its key: entries naming it still match")
	log, err := s.ListLog(ctx, newID)
	require.NoError(t, err)
	assert.Len(t, log, 1, "the audit history of a re-keyed board follows it")
	orphan, err := s.ListLog(ctx, old)
	require.NoError(t, err)
	assert.Empty(t, orphan, "and is not left under the old key")
}
