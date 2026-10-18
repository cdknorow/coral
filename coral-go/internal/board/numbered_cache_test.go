package board

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newNumberedStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStoreWithKey(filepath.Join(t.TempDir(), "board.db"), "")
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func numberedSub(t *testing.T, s *Store, project, id string) {
	t.Helper()
	_, err := s.Subscribe(context.Background(), project, id, "", id, nil, "mentions")
	require.NoError(t, err)
}

// #4598: the board's numbered names come from a cache between reads, and every change to the
// board's active subscribers (subscribe, unsubscribe, deleting the board) makes it stale at once.
func TestNumberedNamesCached_FollowsSubscribersAndIsServedFromTheCache(t *testing.T) {
	ctx := context.Background()
	s := newNumberedStore(t)
	numberedSub(t, s, "b", "Backend Dev")
	numberedSub(t, s, "b", "Backend Dev 2")

	assert.Equal(t, map[string]bool{"backend dev 2": true}, s.numberedNamesCached(ctx, "b"))

	// Served from the cache: a row changed behind the store's back (no bump) is not seen.
	_, err := s.db.Exec(`UPDATE board_subscribers SET is_active = 0 WHERE project = 'b' AND subscriber_id = 'Backend Dev 2'`)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"backend dev 2": true}, s.numberedNamesCached(ctx, "b"), "the cached answer")
	assert.Equal(t, map[string]bool(nil), s.numberedNames(ctx, s.db, "b"), "while the database no longer has it")

	// A subscribe is seen at once, an unsubscribe too.
	numberedSub(t, s, "b", "Backend Dev 3")
	assert.Equal(t, map[string]bool{"backend dev 3": true}, s.numberedNamesCached(ctx, "b"))
	ok, err := s.Unsubscribe(ctx, "b", "Backend Dev 3")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Nil(t, s.numberedNamesCached(ctx, "b"))

	// Deleting the board.
	numberedSub(t, s, "b", "Backend Dev 4")
	assert.Equal(t, map[string]bool{"backend dev 4": true}, s.numberedNamesCached(ctx, "b"))
	require.NoError(t, s.DeleteProject(ctx, "b"))
	assert.Nil(t, s.numberedNamesCached(ctx, "b"))
}

func TestNumberedNamesCached_BoardsAreSeparateAndAnEntryExpires(t *testing.T) {
	ctx := context.Background()
	s := newNumberedStore(t)
	numberedSub(t, s, "one", "Dev 2")
	numberedSub(t, s, "two", "Tester 7")
	assert.Equal(t, map[string]bool{"dev 2": true}, s.numberedNamesCached(ctx, "one"))
	assert.Equal(t, map[string]bool{"tester 7": true}, s.numberedNamesCached(ctx, "two"))

	// An entry older than numberedMaxAge is read again (a writer that missed the bump).
	_, err := s.db.Exec(`UPDATE board_subscribers SET is_active = 0 WHERE project = 'one'`)
	require.NoError(t, err)
	s.numbered.mu.Lock()
	e := s.numbered.entries["one"]
	e.at = time.Now().Add(-numberedMaxAge - time.Second)
	s.numbered.entries["one"] = e
	s.numbered.mu.Unlock()
	assert.Nil(t, s.numberedNamesCached(ctx, "one"))
	assert.Equal(t, map[string]bool{"tester 7": true}, s.numberedNamesCached(ctx, "two"), "the other board's entry is kept")
}

// The memoized terms are the terms the builder makes, and their lowercase forms.
func TestMentionTermsOf_MatchesTheBuilderAndIsMemoized(t *testing.T) {
	for _, c := range [][2]string{{"Backend Dev", ""}, {"Backend Dev 2", "Backend Dev"}, {"", ""}, {"QA Engineer", "QA — Lead"}, {"Ünïcode Agent", "x"}} {
		got := mentionTermsOf(c[0], c[1])
		want := mentionTermsBuild(c[0], c[1])
		assert.Equal(t, want, got.terms, "%q", c)
		require.Len(t, got.lower, len(want))
		for i, w := range want {
			assert.Equal(t, strings.ToLower(w), got.lower[i])
		}
		again := mentionTermsOf(c[0], c[1])
		assert.Same(t, &got.terms[0], &again.terms[0], "the second call is the memo's")
	}
	// Past the limit the memo is dropped, and answers stay right.
	for i := 0; i < termMemoLimit+5; i++ {
		_ = mentionTermsOf("agent", string(rune('a'+i%26))+time.Duration(i).String())
	}
	assert.Equal(t, mentionTermsBuild("Backend Dev", ""), mentionTermsOf("Backend Dev", "").terms)
}
