package board

import (
	"bytes"
	"context"
	"log"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type unreadAnswers struct {
	count, latest int64
	mention       bool
	nonHealth     bool
}

// answers asks the notifier's questions for one reader at one cursor: CheckUnread's count (a fresh cache), the
// newest unread message that tags it, and whether any message does.
func answers(t *testing.T, s *Store, project, id string, cursor int64) unreadAnswers {
	t.Helper()
	ctx := context.Background()
	_, err := s.db.ExecContext(ctx, "UPDATE board_subscribers SET last_read_id = ? WHERE project = ? AND subscriber_id = ?", cursor, project, id)
	require.NoError(t, err)
	s.unreadCounts.invalidate()
	n, err := s.CheckUnread(ctx, project, id)
	require.NoError(t, err)
	latest, err := s.LatestUnreadMessageID(ctx, project, id)
	require.NoError(t, err)
	mention, err := s.HasUnreadMention(ctx, project, id)
	require.NoError(t, err)
	nonHealth, err := s.HasUnreadNonHealthMessage(ctx, project, id)
	require.NoError(t, err)
	return unreadAnswers{int64(n), latest, mention, nonHealth}
}

// The notifier's count (CheckUnread) and its newest-mention searches read the same answers from the mention
// index as from the OR of LIKE patterns, for every reader and cursor of a generated board (#4483).
func TestMentionIndexAnswersTheNotifiersQuestionsAsLikeDid(t *testing.T) {
	s := testStore(t)
	buildMentionBoard(t, s, "p", 1500, 4483)
	var head int64
	require.NoError(t, s.db.Get(&head, "SELECT MAX(id) FROM board_messages"))
	nonzero := 0
	for _, r := range mentionReaders {
		for _, cursor := range []int64{0, head / 3, head - 60, head - 3, head} {
			s.noMentionIndex = true
			before := s.mentionIndexAnswers.Load()
			old := answers(t, s, "p", r.id, cursor)
			require.Equal(t, before, s.mentionIndexAnswers.Load(), "the switch really reads as before")
			s.noMentionIndex = false
			before = s.mentionIndexAnswers.Load()
			got := answers(t, s, "p", r.id, cursor)
			if _, ok := mentionCandidateTokens(mentionTerms(r.id, r.title)); ok {
				assert.Positive(t, s.mentionIndexAnswers.Load()-before, "%s: the index answered", r.id)
			} else {
				assert.Equal(t, before, s.mentionIndexAnswers.Load(), "%s: a term under three bytes reads with LIKE", r.id)
			}
			assert.Equal(t, old, got, "%s (%q) cursor %d", r.id, r.title, cursor)
			if got.count > 0 {
				nonzero++
			}
		}
	}
	assert.Greater(t, nonzero, 20, "the corpus has mentions to count")
}

// Messages above the index's watermark (written since the last catch-up) are still candidates: nothing the
// index has not seen is missed, and the answers are the LIKE query's.
func TestMentionIndexUsersSeeMessagesNotIndexedYet(t *testing.T) {
	s := testStore(t)
	buildMentionBoard(t, s, "p", 300, 11)
	ctx := context.Background()
	require.NoError(t, s.catchUpMentionIndex(ctx))
	// Written by a path that does not index (and no read has caught up since).
	_, err := s.db.ExecContext(ctx, `INSERT INTO board_messages (project, session_id, subscriber_id, content, created_at) VALUES
		('p','poster','poster','@Backend Dev direct one','2026-10-17T00:00:00Z'), ('p','poster','poster','Security Reviewer: direct two','2026-10-17T00:00:01Z')`)
	require.NoError(t, err)
	var head int64
	require.NoError(t, s.db.Get(&head, "SELECT MAX(id) FROM board_messages"))
	var indexed int64
	require.NoError(t, s.db.Get(&indexed, "SELECT indexed_to FROM board_mention_index_state WHERE id = 1"))
	require.Less(t, indexed, head, "the two new messages are not indexed")

	clause := func(id string) unreadAnswers {
		// The notifier's questions without a catch-up first: the watermark stays where it is.
		_, err := s.db.ExecContext(ctx, "UPDATE board_subscribers SET last_read_id = ? WHERE project = 'p' AND subscriber_id = ?", head-2, id)
		require.NoError(t, err)
		s.unreadCounts.invalidate()
		latest, err := s.confirmedMention(ctx, "p", id, titleOf(id), "m.project=? AND m.id>? AND m.subscriber_id!=?", []interface{}{"p", head - 2, "x"}, head-2, true)
		require.NoError(t, err)
		return unreadAnswers{latest: latest}
	}
	// confirmedMention itself catches up through mentionIndexTokensJSON; stop that to look at the watermark clause.
	for _, id := range []string{"backend-dev", "sec"} {
		s.noMentionIndex = true
		old := clause(id)
		s.noMentionIndex = false
		assert.Equal(t, old, clause(id), id)
		assert.Equal(t, head-1+boolInt(id == "sec"), clause(id).latest, "%s finds its direct message", id)
	}
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func titleOf(id string) string {
	for _, r := range mentionReaders {
		if r.id == id {
			return r.title
		}
	}
	return ""
}

// The clause on its own (no catch-up before it) keeps every message above the watermark as a candidate.
func TestMentionIndexClauseKeepsMessagesAboveTheWatermark(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, err := s.Subscribe(ctx, "p", "backend-dev", "Backend Dev", "s1", nil, "mentions")
	require.NoError(t, err)
	a, err := s.PostMessage(ctx, "p", "poster", "@Backend Dev first", nil)
	require.NoError(t, err)
	require.NoError(t, s.catchUpMentionIndex(ctx))
	b, err := s.PostMessage(ctx, "p", "poster", "unrelated words only", nil) // above the watermark, indexed nowhere
	require.NoError(t, err)
	tokens, ok := mentionCandidateTokens(mentionTerms("backend-dev", "Backend Dev"))
	require.True(t, ok)
	_ = tokens
	arr := `["*"]` // no token of its own: only the watermark can keep a message
	var ids []int64
	require.NoError(t, s.db.Select(&ids, "SELECT id FROM board_messages WHERE project = 'p' AND "+mentionIndexClause("")+" ORDER BY id", arr, 0))
	assert.Equal(t, []int64{b.ID}, ids, "the indexed message has no token in the array; the unindexed one is kept by the watermark")
	require.NoError(t, s.catchUpMentionIndex(ctx))
	require.NoError(t, s.db.Select(&ids, "SELECT id FROM board_messages WHERE project = 'p' AND "+mentionIndexClause("")+" ORDER BY id", arr, 0))
	assert.Empty(t, ids, "after the catch-up neither is a candidate for an empty token set (%d, %d)", a.ID, b.ID)
}

// A store opened on a database with many messages to index says so in the log, once (Security's #4465 INFO 1).
func TestMentionBackfillLogsItsMessageCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.db")
	s, err := NewStoreWithKey(path, "")
	require.NoError(t, err)
	ctx := context.Background()
	_, err = s.Subscribe(ctx, "p", "dev", "Dev", "s1", nil, "mentions")
	require.NoError(t, err)
	for i := 0; i < mentionBackfillLogMin+5; i++ {
		_, err = s.PostMessage(ctx, "p", "dev", "filler @Dev note", nil)
		require.NoError(t, err)
	}
	_, err = s.db.Exec("UPDATE board_mention_index_state SET indexed_to = 0")
	require.NoError(t, err)
	require.NoError(t, s.Close())

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(log.Writer())
	s, err = NewStoreWithKey(path, "")
	require.NoError(t, err)
	defer s.Close()
	assert.Contains(t, buf.String(), "indexing 1005 messages")
	assert.Contains(t, buf.String(), "1005 messages indexed in")

	// A small backlog is not announced.
	buf.Reset()
	for i := 0; i < 5; i++ {
		_, err = s.PostMessage(ctx, "p", "dev", "later", nil)
		require.NoError(t, err)
	}
	require.NoError(t, s.Close())
	s, err = NewStoreWithKey(path, "")
	require.NoError(t, err)
	defer s.Close()
	assert.NotContains(t, buf.String(), "mention index")
}

// Each of the two users answers from the index on its own, and LatestUnreadMessageID is the newest message that
// tags the reader (an expectation, not a comparison with the LIKE read, which shares its ordering).
func TestMentionIndexUsersEachAnswerFromTheIndex(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, err := s.Subscribe(ctx, "p", "backend-dev", "Backend Dev", "s1", nil, "mentions")
	require.NoError(t, err)
	post := func(text string) int64 {
		m, err := s.PostMessage(ctx, "p", "poster", text, nil)
		require.NoError(t, err)
		return m.ID
	}
	first := post("@Backend Dev one")
	post("nothing about anyone")
	second := post("Backend Dev: two")
	post("@Backend Developer is a near miss")

	hits := func(f func()) int64 {
		before := s.mentionIndexAnswers.Load()
		f()
		return s.mentionIndexAnswers.Load() - before
	}
	var latest int64
	assert.Positive(t, hits(func() { latest, err = s.LatestUnreadMessageID(ctx, "p", "backend-dev") }), "the newest-mention search used the index")
	require.NoError(t, err)
	assert.Equal(t, second, latest, "the newest message that tags the reader, not the near miss after it")
	var n int
	assert.Positive(t, hits(func() { n, err = s.CheckUnread(ctx, "p", "backend-dev") }), "the count used the index")
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	_, _ = first, second
}
