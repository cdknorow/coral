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

func mentionStmts(h *hotStmts) map[string]any {
	return map[string]any{"candidates": h.mentionCandidates, "newest": h.newestMessageID, "count": h.mentionCount,
		"chunk": h.mentionChunk, "insert": h.mentionInsert, "setmark": h.mentionSetMark, "getmark": h.mentionGetMark}
}

// The mention queries answer the same prepared as unprepared: ReadMessages' candidates, CheckUnread's count, the
// notifier's searches and the catch-up that fills the index (#4483 follow-up, after #4463).
func TestMentionStatementsAnswerAsUnprepared(t *testing.T) {
	isolateBoardTest(t)
	s := testStore(t)
	for name, st := range mentionStmts(s.stmts) {
		require.NotNil(t, st, "%s is prepared", name)
	}
	buildMentionBoard(t, s, "p", 600, 4516)
	var head int64
	require.NoError(t, s.db.Get(&head, "SELECT MAX(id) FROM board_messages"))
	type result struct {
		Answers unreadAnswers
		Read    []int64
	}
	run := func() map[string]result {
		out := map[string]result{}
		for _, r := range mentionReaders {
			for _, cursor := range []int64{0, head / 2, head - 30} {
				a := answers(t, s, "p", r.id, cursor)
				out[fmt.Sprintf("%s@%d", r.id, cursor)] = result{a, readIDs(t, s, "p", r.id, cursor, 50)}
			}
		}
		return out
	}
	prepared := run()
	assert.Positive(t, s.mentionIndexAnswers.Load(), "the index answered")
	kept := s.stmts
	s.stmts = nil // every query unprepared
	unprepared := run()
	s.stmts = kept
	assert.Equal(t, unprepared, prepared)
	require.NotEmpty(t, kept.cached, "confirmedMention's query shapes were prepared on first use")
	assert.LessOrEqual(t, len(kept.cached), maxCachedStmts)
	for q, st := range kept.cached {
		assert.NotNil(t, st, q)
	}

	// The catch-up through the prepared write statements and without them index the same tokens.
	rows := func() []string {
		var got []string
		require.NoError(t, s.db.Select(&got, "SELECT token || ':' || message_id FROM board_message_mentions ORDER BY 1"))
		return got
	}
	withStmts := rows()
	_, err := s.db.Exec("DELETE FROM board_message_mentions; UPDATE board_mention_index_state SET indexed_to = 0")
	require.NoError(t, err)
	s.mentionIndexedTo.Store(-1)
	s.stmts = nil
	require.NoError(t, s.catchUpMentionIndex(context.Background()))
	s.stmts = kept
	assert.Equal(t, withStmts, rows())
	assert.NotEmpty(t, withStmts)
}

// Used from many goroutines at once (go test -race): posts, catch-ups, reads, counts and searches.
func TestMentionStatementsConcurrentUse(t *testing.T) {
	isolateBoardTest(t)
	s := testStore(t)
	buildMentionBoard(t, s, "p", 200, 17)
	ctx := context.Background()
	var wg sync.WaitGroup
	for g := 0; g < 12; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			r := mentionReaders[g%len(mentionReaders)]
			for i := 0; i < 25; i++ {
				if g%4 == 0 {
					if _, err := s.PostMessage(ctx, "p", "poster", fmt.Sprintf("@%s round %d", r.title, i), nil); !assert.NoError(t, err) {
						return
					}
				}
				if _, err := s.ReadMessages(ctx, "p", r.id, 20); !assert.NoError(t, err) {
					return
				}
				if _, err := s.CheckUnread(ctx, "p", r.id); !assert.NoError(t, err) {
					return
				}
				if _, err := s.LatestUnreadMessageID(ctx, "p", r.id); !assert.NoError(t, err) {
					return
				}
				if _, err := s.HasUnreadMention(ctx, "p", r.id); !assert.NoError(t, err) {
					return
				}
			}
		}(g)
	}
	wg.Wait()
	var head, indexed int64
	require.NoError(t, s.db.Get(&head, "SELECT MAX(id) FROM board_messages"))
	require.NoError(t, s.catchUpMentionIndex(ctx))
	require.NoError(t, s.db.Get(&indexed, "SELECT indexed_to FROM board_mention_index_state WHERE id = 1"))
	assert.Equal(t, head, indexed)
}

// A store reopened over the same file prepares and answers as before, and a schema change made after the
// statements were prepared does not break them.
func TestMentionStatementsAfterReopenAndMigration(t *testing.T) {
	isolateBoardTest(t)
	path := filepath.Join(t.TempDir(), "board.db")
	s, err := NewStoreWithKey(path, "")
	require.NoError(t, err)
	buildMentionBoard(t, s, "p", 300, 5)
	before := answers(t, s, "p", "backend-dev", 0)
	readBefore := readIDs(t, s, "p", "backend-dev", 0, 50)
	require.NoError(t, s.Close())

	s, err = NewStoreWithKey(path, "")
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	for name, st := range mentionStmts(s.stmts) {
		require.NotNil(t, st, "%s is prepared again", name)
	}
	assert.Equal(t, before, answers(t, s, "p", "backend-dev", 0))
	assert.Equal(t, readBefore, readIDs(t, s, "p", "backend-dev", 0, 50))

	_, err = s.db.Exec(`ALTER TABLE board_messages ADD COLUMN later_note TEXT`)
	require.NoError(t, err)
	assert.Equal(t, before, answers(t, s, "p", "backend-dev", 0), "the same answers after a schema change")
}

// Close closes the mention statements too, and the lazily prepared ones.
func TestCloseClosesMentionStatements(t *testing.T) {
	isolateBoardTest(t)
	s, err := NewStoreWithKey(filepath.Join(t.TempDir(), "board.db"), "")
	require.NoError(t, err)
	buildMentionBoard(t, s, "p", 40, 3)
	_ = answers(t, s, "p", "backend-dev", 0)
	st := s.stmts.mentionCandidates
	require.NotNil(t, st)
	require.NotEmpty(t, s.stmts.cached)
	require.NoError(t, s.Close())
	for name, st := range mentionStmts(s.stmts) {
		assert.Nil(t, st, "%s dropped by Close", name)
	}
	assert.Empty(t, s.stmts.cached)
	_, err = st.QueryxContext(context.Background(), "p", 0, "x", "[]", 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

// The lazy cache is bounded: query shapes past maxCachedStmts run unprepared.
func TestCachedStatementsAreBounded(t *testing.T) {
	isolateBoardTest(t)
	s := testStore(t)
	h := s.stmts
	for i := 0; i < maxCachedStmts+5; i++ {
		st := h.cachedStmt(context.Background(), s.reader(), fmt.Sprintf("SELECT %d", i))
		if i < maxCachedStmts {
			assert.NotNil(t, st, "shape %d", i)
		} else {
			assert.Nil(t, st, "shape %d is past the bound", i)
		}
	}
	assert.Len(t, h.cached, maxCachedStmts)
	assert.Nil(t, h.cachedStmt(context.Background(), s.reader(), "SELECT broken syntax FROM"), "a query that cannot be prepared runs unprepared")
	var nilStmts *hotStmts
	assert.Nil(t, nilStmts.cachedStmt(context.Background(), s.reader(), "SELECT 1"))
}

// ReadMessages runs its candidate query through the prepared statement: with that statement closed under it the
// read fails, which an unprepared query would not.
func TestReadMessagesUsesThePreparedCandidateStatement(t *testing.T) {
	isolateBoardTest(t)
	s := testStore(t)
	buildMentionBoard(t, s, "p", 80, 9)
	_, err := s.ReadMessages(context.Background(), "p", "backend-dev", 20)
	require.NoError(t, err)
	require.NoError(t, s.stmts.mentionCandidates.Close())
	_, err = s.ReadMessages(context.Background(), "p", "backend-dev", 20)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
	s.stmts.mentionCandidates = nil // as Close would leave it: the query runs unprepared again
	_, err = s.ReadMessages(context.Background(), "p", "backend-dev", 20)
	assert.NoError(t, err)
}

// A delete that fails leaves the message indexed: its index rows are dropped in the same transaction as the message, so a
// failed delete (here a trigger that aborts it) rolls them back and the message is still found by its mention (#4523 INFO).
func TestFailedDeleteLeavesTheMessageIndexed(t *testing.T) {
	isolateBoardTest(t)
	s := testStore(t)
	ctx := context.Background()
	_, err := s.Subscribe(ctx, "p", "backend-dev", "Backend Dev", "s1", nil, "mentions")
	require.NoError(t, err)
	m, err := s.PostMessage(ctx, "p", "poster", "@Backend Dev keep this one", nil)
	require.NoError(t, err)
	require.NoError(t, s.catchUpMentionIndex(ctx))
	rows := func() (n int) {
		require.NoError(t, s.db.Get(&n, "SELECT COUNT(*) FROM board_message_mentions WHERE message_id = ?", m.ID))
		return n
	}
	before := rows()
	require.Positive(t, before)

	_, err = s.db.Exec(`CREATE TRIGGER refuse_delete BEFORE DELETE ON board_messages BEGIN SELECT RAISE(ABORT, 'refused'); END`)
	require.NoError(t, err)
	ok, err := s.DeleteMessage(ctx, "p", m.ID)
	require.Error(t, err)
	assert.False(t, ok)
	assert.Equal(t, before, rows(), "the index rows are still there after a failed delete")
	assert.Equal(t, []int64{m.ID}, readIDs(t, s, "p", "backend-dev", 0, 10), "and the mention is still found")

	_, err = s.db.Exec(`DROP TRIGGER refuse_delete`)
	require.NoError(t, err)
	ok, err = s.DeleteMessage(ctx, "p", m.ID)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Zero(t, rows(), "a delete that works forgets them")
}
