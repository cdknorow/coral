package board

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mentionReaders are subscribers whose names make the mention terms awkward: spaces, case, numbers, punctuation,
// other scripts, LIKE's wildcards, and names too short for the index (which read as before).
var mentionReaders = []struct{ id, title string }{
	{"backend-dev", "Backend Dev"},
	{"Backend Dev 2", "Backend Dev"},
	{"qa-1", "QA Engineer"},
	{"dev_ops", "Dev_Ops"},
	{"zoe", "Zoë Dev"},
	{"unal", "Ünal Dev"},
	{"pct", "100% Dev"},
	{"dash", "Dash — Dev"},
	{"sec", "Security Reviewer"},
	{"al", "Al"},   // a title under three bytes: the LIKE query reads for it
	{"x", "Short"}, // an id under three bytes
	{"Frontend Dev", "Frontend Dev"},
}

// mentionFragments are pieces of text that mention, nearly mention or do not mention these readers, in every
// form containsExplicitTag knows and the near misses it must refuse.
var mentionFragments = []string{
	"@Backend Dev please look", "@backend dev", "@BACKEND DEV", "@BackendDev", "@backend-dev", "@Backend Dev 2 take this",
	"@Backend Developer", "x@backend-dev", "user@backend.com", "mybackend dev: no", "Backend Dev: please", "backend dev — ready",
	"Backend Dev—ready", "@QA Engineer", "@qa-1", "@qa", "@QAEngineer", "QA Engineer: hello", "@dev_ops", "@Dev_Ops now",
	"@devops", "dev_ops: x", "@Zoë Dev", "@zoë dev", "@ZOË DEV", "zoë dev: hi", "@Ünal Dev", "@ünal dev", "@ÜNAL DEV",
	"@100% Dev", "@100x Dev", "100% Dev: ok", "@Dash — Dev", "Dash — Dev: yes", "@Security Reviewer", "@security reviewer 3 items",
	"@Al", "@al hello", "Al: hello", "@all", "@All hands", "@notify-all", "@notify_all", "@notifyall", "@allies", "@Short", "short: ok",
	"@x marks", "x: marks", "@Frontend Dev", "@Frontend Dev 2", "nothing to see here", "build passed: 42 tests", "a — b", "12:30 standup",
	"@", "@a", "@ab", ":", "—", "ab:", "@orchestrator",
}

func buildMentionBoard(t *testing.T, s *Store, project string, n int, seed int64) {
	t.Helper()
	ctx := context.Background()
	for _, r := range mentionReaders {
		_, err := s.Subscribe(ctx, project, r.id, r.title, "sess-"+r.id, nil, "mentions")
		require.NoError(t, err)
	}
	_, err := s.Subscribe(ctx, project, "poster", "Poster", "sess-poster", nil, "mentions")
	require.NoError(t, err)
	rng := rand.New(rand.NewSource(seed))
	for i := 0; i < n; i++ {
		var parts []string
		for k := rng.Intn(4); k >= 0; k-- {
			parts = append(parts, mentionFragments[rng.Intn(len(mentionFragments))])
			if rng.Intn(3) == 0 {
				parts = append(parts, fmt.Sprintf("filler %d words about the build", rng.Intn(1000)))
			}
		}
		content := strings.Join(parts, " ")
		if i%97 == 0 { // a message with more tokens than the index keeps: the wild token
			for k := 0; k < 80; k++ {
				content += fmt.Sprintf(" k%03d:v @n%03d", k, k)
			}
		}
		poster := "poster"
		if rng.Intn(5) == 0 {
			poster = mentionReaders[rng.Intn(len(mentionReaders))].id
		}
		_, err := s.PostMessage(ctx, project, poster, content, nil)
		require.NoError(t, err)
	}
}

func readIDs(t *testing.T, s *Store, project, id string, cursor int64, limit int) []int64 {
	t.Helper()
	ctx := context.Background()
	_, err := s.db.ExecContext(ctx, "UPDATE board_subscribers SET last_read_id = ? WHERE project = ? AND subscriber_id = ?", cursor, project, id)
	require.NoError(t, err)
	msgs, err := s.ReadMessages(ctx, project, id, limit)
	require.NoError(t, err)
	out := make([]int64, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.ID)
	}
	return out
}

// The index returns what the LIKE query returned, for every reader, cursor and limit over a generated board
// (#4465): equal when the old query's window of candidates was not full, and never fewer, with the old results
// first, when it was.
func TestMentionIndexReadsWhatTheLikeQueryRead(t *testing.T) {
	s := testStore(t)
	buildMentionBoard(t, s, "p", 1500, 4465)
	var head int64
	require.NoError(t, s.db.Get(&head, "SELECT MAX(id) FROM board_messages"))
	hits, indexedReaders := 0, 0
	for _, r := range mentionReaders {
		_, ok := mentionCandidateTokens(mentionTerms(r.id, r.title))
		if ok {
			indexedReaders++
		}
		for _, cursor := range []int64{0, head / 3, head - 40, head} {
			for _, limit := range []int{1, 5, 50} {
				s.noMentionIndex = true
				old := readIDs(t, s, "p", r.id, cursor, limit)
				s.noMentionIndex = false
				got := readIDs(t, s, "p", r.id, cursor, limit)
				hits += len(got)
				assert.Equal(t, old, got, "%s (%q) cursor %d limit %d", r.id, r.title, cursor, limit)
			}
		}
	}
	assert.Greater(t, hits, 200, "the corpus has mentions to find")
	assert.Greater(t, indexedReaders, 8, "most readers are answered by the index")
	_, ok := mentionCandidateTokens(mentionTerms("al", "Al"))
	assert.False(t, ok, "a title under three bytes reads with LIKE")
}

// The old query only looked at its first 50 candidates, and when near misses filled them it never reached the
// real mention behind them. The index keeps that window as it was (near misses share the real mention's tokens),
// so what a read returns does not change, even there.
func TestMentionIndexKeepsTheOldCandidateWindow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, err := s.Subscribe(ctx, "p", "backend-dev", "Backend Dev", "s1", nil, "mentions")
	require.NoError(t, err)
	_, err = s.Subscribe(ctx, "p", "poster", "Poster", "s2", nil, "mentions")
	require.NoError(t, err)
	for i := 0; i < 80; i++ { // LIKE-matches %@backend dev%, refused by the word boundary
		_, err = s.PostMessage(ctx, "p", "poster", "@Backend Developer note", nil)
		require.NoError(t, err)
	}
	real, err := s.PostMessage(ctx, "p", "poster", "@Backend Dev this one is yours", nil)
	require.NoError(t, err)
	s.noMentionIndex = true
	old := readIDs(t, s, "p", "backend-dev", 0, 5)
	s.noMentionIndex = false
	got := readIDs(t, s, "p", "backend-dev", 0, 5)
	assert.Equal(t, old, got)
	assert.Empty(t, got, "the window of 50 is full of near misses, as before")
	// From a cursor inside the near misses the real one is within the window, in both.
	s.noMentionIndex = true
	old = readIDs(t, s, "p", "backend-dev", real.ID-30, 5)
	s.noMentionIndex = false
	assert.Equal(t, []int64{real.ID}, old)
	assert.Equal(t, old, readIDs(t, s, "p", "backend-dev", real.ID-30, 5))
}

// The tokens never miss a mention: for every fragment and every reader, if the confirming rule says the
// fragment tags the reader, the reader's candidate tokens include one of the fragment's tokens (or it is wild).
func TestMentionTokensCoverEveryTag(t *testing.T) {
	for _, r := range mentionReaders {
		terms := mentionTerms(r.id, r.title)
		cand, ok := mentionCandidateTokens(terms)
		if !ok {
			continue
		}
		want := map[string]bool{}
		for _, c := range cand {
			want[c] = true
		}
		for _, f := range mentionFragments {
			for _, content := range []string{f, "intro " + f + " outro", strings.ToUpper(f), strings.Repeat("x", 5) + " " + f} {
				if !containsExplicitTag(content, r.id, r.title) || !likeAny(content, mentionLikePatterns(r.id, r.title)) {
					continue
				}
				found := false
				for _, tk := range mentionTokens(content) {
					found = found || want[tk]
				}
				assert.True(t, found, "%q tags %s (%q) but has none of its tokens", content, r.id, r.title)
			}
		}
	}
	// More than the cap: only the wild token, which every query includes.
	var many strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&many, "k%03d:v @n%03d ", i, i)
	}
	assert.Equal(t, []string{mentionWild}, mentionTokens(many.String()))
	assert.Contains(t, mustTokens(t, "Dev", "Dev"), mentionWild)
	// A message with nothing that could be a mention has no tokens.
	assert.Empty(t, mentionTokens("nothing here at all"))
}

func mustTokens(t *testing.T, id, title string) []string {
	cand, ok := mentionCandidateTokens(mentionTerms(id, title))
	require.True(t, ok)
	return cand
}

// An existing database gets the index by catching up: a store opened on messages that were never indexed
// indexes them all, and messages written after (by any path) are indexed before the next read.
func TestMentionIndexBackfillsAnExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.db")
	s, err := NewStoreWithKey(path, "")
	require.NoError(t, err)
	buildMentionBoard(t, s, "p", 600, 7)
	// As a database from before the index: no tokens, no state.
	_, err = s.db.Exec("DROP TABLE board_message_mentions")
	require.NoError(t, err)
	_, err = s.db.Exec("DROP TABLE board_mention_index_state")
	require.NoError(t, err)
	var total int
	require.NoError(t, s.db.Get(&total, "SELECT COUNT(*) FROM board_messages"))
	require.NoError(t, s.Close())

	s, err = NewStoreWithKey(path, "")
	require.NoError(t, err)
	defer s.Close()
	var indexed, newest int64
	require.NoError(t, s.db.Get(&indexed, "SELECT indexed_to FROM board_mention_index_state WHERE id = 1"))
	require.NoError(t, s.db.Get(&newest, "SELECT MAX(id) FROM board_messages"))
	assert.Equal(t, newest, indexed, "the backfill reached the newest message")
	var rows int
	require.NoError(t, s.db.Get(&rows, "SELECT COUNT(*) FROM board_message_mentions"))
	assert.Greater(t, rows, total/2)

	// Results after the backfill equal the LIKE query's.
	for _, r := range mentionReaders {
		s.noMentionIndex = true
		old := readIDs(t, s, "p", r.id, 0, 50)
		s.noMentionIndex = false
		assert.Equal(t, old, readIDs(t, s, "p", r.id, 0, 50), r.id)
	}

	// A message written later, by another path than PostMessage, is found by the next read.
	ctx := context.Background()
	_, err = s.db.ExecContext(ctx, `INSERT INTO board_messages (project, session_id, subscriber_id, content, created_at) VALUES ('p','poster','poster','@Backend Dev late and direct','2026-10-17T00:00:00Z')`)
	require.NoError(t, err)
	var late int64
	require.NoError(t, s.db.Get(&late, "SELECT MAX(id) FROM board_messages"))
	got := readIDs(t, s, "p", "backend-dev", late-1, 50)
	assert.Equal(t, []int64{late}, got)

	// Reopening again changes nothing (the watermark holds; no row is indexed twice).
	require.NoError(t, s.Close())
	s, err = NewStoreWithKey(path, "")
	require.NoError(t, err)
	defer s.Close()
	var again int
	require.NoError(t, s.db.Get(&again, "SELECT COUNT(*) FROM board_message_mentions"))
	assert.Greater(t, again, rows)
}

// Deleting a message or a board removes its index rows.
func TestMentionIndexForgetsDeletedMessages(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	_, err := s.Subscribe(ctx, "p", "backend-dev", "Backend Dev", "s1", nil, "mentions")
	require.NoError(t, err)
	m1, err := s.PostMessage(ctx, "p", "poster", "@Backend Dev one", nil)
	require.NoError(t, err)
	m2, err := s.PostMessage(ctx, "p", "poster", "@Backend Dev two", nil)
	require.NoError(t, err)
	_, err = s.PostMessage(ctx, "q", "poster", "@Backend Dev other board", nil)
	require.NoError(t, err)
	require.Equal(t, []int64{m1.ID, m2.ID}, readIDs(t, s, "p", "backend-dev", 0, 50))
	count := func(id int64) (n int) {
		require.NoError(t, s.db.Get(&n, "SELECT COUNT(*) FROM board_message_mentions WHERE message_id = ?", id))
		return n
	}
	require.Positive(t, count(m1.ID))
	ok, err := s.DeleteMessage(ctx, "p", m1.ID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Zero(t, count(m1.ID))
	assert.Positive(t, count(m2.ID))
	require.NoError(t, s.DeleteProject(ctx, "p"))
	assert.Zero(t, count(m2.ID))
	var other int
	require.NoError(t, s.db.Get(&other, "SELECT COUNT(*) FROM board_message_mentions"))
	assert.Positive(t, other, "another board's rows stay")
}
