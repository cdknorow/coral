package board

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolateBoardTest points every temp and home folder at a test folder (the #4381 env rules).
func isolateBoardTest(t *testing.T) {
	t.Helper()
	iso := t.TempDir()
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, iso)
	}
	t.Setenv("HOME", filepath.Join(iso, "home"))
	t.Setenv("USERPROFILE", filepath.Join(iso, "home"))
	t.Setenv("CORAL_DATA_DIR", filepath.Join(iso, "data"))
}

// seedHotQueries is a board store with what the hot queries read: two boards sharing a label, one with a
// distinct label, two subscribers (one gone) and messages from each.
func seedHotQueries(t *testing.T, s *Store) (alpha, beta, gamma *Board) {
	t.Helper()
	ctx := context.Background()
	var err error
	alpha, err = s.CreateBoard(ctx, "shared", "test")
	require.NoError(t, err)
	beta, err = s.CreateBoard(ctx, "shared", "test")
	require.NoError(t, err)
	gamma, err = s.CreateBoard(ctx, "solo", "test")
	require.NoError(t, err)
	for _, b := range []*Board{alpha, beta, gamma} {
		_, err = sub(s, ctx, b.ID, "Dev", "Developer")
		require.NoError(t, err)
		_, err = sub(s, ctx, b.ID, "Lead", "Orchestrator")
		require.NoError(t, err)
		for i := 0; i < 3; i++ {
			_, err = s.PostMessage(ctx, b.ID, "Dev", fmt.Sprintf("dev %d on %s", i, b.ID), nil)
			require.NoError(t, err)
		}
		_, err = s.PostMessage(ctx, b.ID, "Lead", "lead note @Dev", nil)
		require.NoError(t, err)
	}
	_, err = s.Unsubscribe(ctx, gamma.ID, "Lead")
	require.NoError(t, err)
	return alpha, beta, gamma
}

// hotResults is what every hot query answers for a set of inputs, found or not.
func hotResults(t *testing.T, s *Store, alpha, gamma *Board) map[string]any {
	t.Helper()
	ctx := context.Background()
	out := map[string]any{}
	for _, name := range []string{"tmux-Dev", "tmux-Lead", "tmux-nobody"} {
		got, err := s.GetSubscriptionBySessionName(ctx, name)
		out["sub "+name] = []any{got, errText(err)}
	}
	for _, id := range []string{alpha.ID, gamma.ID, "b-missing"} {
		got, err := s.GetBoard(ctx, id)
		out["board "+id] = []any{got, errText(err)}
	}
	for _, key := range []string{alpha.ID, "shared", "solo", "nothing"} {
		id, cands, err := s.ResolveBoard(ctx, key)
		out["resolve "+key] = []any{id, cands, errText(err)}
	}
	for _, label := range []string{"shared", "solo", "nothing"} {
		got, err := s.BoardsLabelled(ctx, label)
		out["labelled "+label] = []any{got, errText(err)}
	}
	var ownMax int64
	require.NoError(t, getHot(ctx, s.hot().ownMaxMessageID, s.reader(), &ownMax, sqlOwnMaxMessageID, alpha.ID, "Dev"))
	out["ownmax"] = ownMax
	return out
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// #4463: the hot queries answer the same prepared as unprepared, found and not found.
func TestHotStatementsAnswerAsUnprepared(t *testing.T) {
	isolateBoardTest(t)
	s := testStore(t)
	require.NotNil(t, s.stmts)
	for name, st := range map[string]any{"subscription": s.stmts.subscriptionBySessionName, "board": s.stmts.boardByID,
		"labelled": s.stmts.boardsLabelled, "ownmax": s.stmts.ownMaxMessageID} {
		require.NotNil(t, st, "%s is prepared", name)
	}
	alpha, _, gamma := seedHotQueries(t, s)
	prepared := hotResults(t, s, alpha, gamma)

	kept := s.stmts
	s.stmts = nil // the unprepared path
	unprepared := hotResults(t, s, alpha, gamma)
	s.stmts = kept
	assert.Equal(t, unprepared, prepared)
	assert.Equal(t, ErrAmbiguousBoard.Error(), prepared["resolve shared"].([]any)[2], "a shared label is ambiguous")
	assert.Len(t, prepared["resolve shared"].([]any)[1], 2)
	assert.Equal(t, ErrBoardNotFound.Error(), prepared["board b-missing"].([]any)[1])
	assert.Nil(t, prepared["sub tmux-nobody"].([]any)[0])
	assert.NotNil(t, prepared["sub tmux-Dev"].([]any)[0])

	// ReadMessages uses the own-max lookup: a read returns the other agent's message and moves past its own.
	msgs, err := s.ReadMessages(context.Background(), alpha.ID, "Dev", 50)
	require.NoError(t, err)
	require.NotEmpty(t, msgs)
	again, err := s.ReadMessages(context.Background(), alpha.ID, "Dev", 50)
	require.NoError(t, err)
	assert.Empty(t, again, "the cursor moved past every message, its own included")
}

// Used from many goroutines at once (go test -race), the prepared statements give each caller its own answer.
func TestHotStatementsConcurrentUse(t *testing.T) {
	isolateBoardTest(t)
	s := testStore(t)
	alpha, beta, gamma := seedHotQueries(t, s)
	ctx := context.Background()
	var wg sync.WaitGroup
	for g := 0; g < 24; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				b := []*Board{alpha, beta, gamma}[(g+i)%3]
				got, err := s.GetBoard(ctx, b.ID)
				if !assert.NoError(t, err) || !assert.Equal(t, b.ID, got.ID) {
					return
				}
				id, _, err := s.ResolveBoard(ctx, "solo")
				assert.NoError(t, err)
				assert.Equal(t, gamma.ID, id)
				sb, err := s.GetSubscriptionBySessionName(ctx, "tmux-Dev")
				assert.NoError(t, err)
				assert.NotNil(t, sb)
			}
		}(g)
	}
	wg.Wait()
}

// A store opened again over the same file (its migrations run again) prepares and answers as before, and a
// schema change made after the statements were prepared doesn't break them (SQLite re-prepares).
func TestHotStatementsAfterReopenAndMigration(t *testing.T) {
	isolateBoardTest(t)
	path := filepath.Join(t.TempDir(), "board.db")
	s, err := NewStoreWithKey(path, "")
	require.NoError(t, err)
	alpha, _, gamma := seedHotQueries(t, s)
	before := hotResults(t, s, alpha, gamma)
	require.NoError(t, s.Close())

	s, err = NewStoreWithKey(path, "")
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	require.NotNil(t, s.stmts.boardByID)
	assert.Equal(t, before, hotResults(t, s, alpha, gamma), "the same answers after reopening")

	_, err = s.db.Exec(`ALTER TABLE boards ADD COLUMN later_note TEXT`)
	require.NoError(t, err)
	assert.Equal(t, before, hotResults(t, s, alpha, gamma), "the same answers after a schema change")
}

// Close closes the prepared statements before the databases; the store's queries then fail as a closed store's do.
func TestCloseClosesHotStatements(t *testing.T) {
	isolateBoardTest(t)
	s, err := NewStoreWithKey(filepath.Join(t.TempDir(), "board.db"), "")
	require.NoError(t, err)
	st := s.stmts.boardByID
	require.NotNil(t, st)
	require.NoError(t, s.Close())
	assert.Nil(t, s.stmts.boardByID, "Close drops the statements")
	var b Board
	err = st.GetContext(context.Background(), &b, "x")
	require.Error(t, err, "the statement was closed")
	assert.Contains(t, err.Error(), "closed")
}
