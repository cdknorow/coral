package board

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// #4788: a mention index that is current is read without mentionMu. 97% of the mutex contention of a 250-agent run was every agent's
// ReadMessages queueing on that lock behind the one that was indexing (a write transaction, so it waits for the one writer connection).
// With the lock held by an indexer in progress, a catch-up of a current index still answers at once, and one of a stale index waits for it
// and then does its work.
func TestCatchUpOfACurrentMentionIndexDoesNotWaitForTheIndexer(t *testing.T) {
	ctx := context.Background()
	s, err := NewStoreWithKey(filepath.Join(t.TempDir(), "board.db"), "")
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	_, err = s.Subscribe(ctx, "p", "poster", "Poster", "sess-poster", nil, "all")
	require.NoError(t, err)
	_, err = s.PostMessage(ctx, "p", "poster", "hello @Reader", nil)
	require.NoError(t, err)
	require.NoError(t, s.catchUpMentionIndex(ctx)) // current

	s.mentionMu.Lock() // an indexer in progress
	done := make(chan error, 1)
	go func() { done <- s.catchUpMentionIndex(ctx) }()
	select {
	case err := <-done:
		require.NoError(t, err, "a current index needs nothing")
	case <-time.After(2 * time.Second):
		s.mentionMu.Unlock()
		t.Fatal("the catch-up of a current index waited for the lock")
	}

	// a new message makes the index stale: the catch-up now needs the lock, takes its turn, and indexes it
	_, err = s.PostMessage(ctx, "p", "poster", "hello again @Reader", nil)
	require.NoError(t, err)
	go func() { done <- s.catchUpMentionIndex(ctx) }()
	select {
	case <-done:
		s.mentionMu.Unlock()
		t.Fatal("the catch-up of a stale index did not wait for the lock")
	case <-time.After(300 * time.Millisecond):
	}
	s.mentionMu.Unlock()
	require.NoError(t, <-done)
	var newest, indexed int64
	require.NoError(t, s.reader().GetContext(ctx, &newest, `SELECT MAX(id) FROM board_messages`))
	require.NoError(t, s.reader().GetContext(ctx, &indexed, `SELECT indexed_to FROM board_mention_index_state WHERE id = 1`))
	require.Equal(t, newest, indexed, "the stale index was brought up to date")
}

// The boundary of "current" (#4788, Security's INFO): an index one message behind is stale. Messages are posted, the index caught up between, until
// the newest id is exactly the watermark plus one; then a catch-up with the lock held must wait for it (a comparison with <= from+1 would read the
// index as current and miss the message) and, once it has the lock, index that message.
func TestMentionIndexOneMessageBehindIsStale(t *testing.T) {
	ctx := context.Background()
	s, err := NewStoreWithKey(filepath.Join(t.TempDir(), "board.db"), "")
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	_, err = s.Subscribe(ctx, "p", "poster", "Poster", "sess-poster", nil, "all")
	require.NoError(t, err)
	gap := func() (int64, int64) {
		var newest, indexed int64
		require.NoError(t, s.reader().GetContext(ctx, &newest, `SELECT COALESCE(MAX(id), 0) FROM board_messages`))
		require.NoError(t, s.reader().GetContext(ctx, &indexed, `SELECT indexed_to FROM board_mention_index_state WHERE id = 1`))
		return newest, indexed
	}
	found := false
	for i := 0; i < 20 && !found; i++ {
		require.NoError(t, s.catchUpMentionIndex(ctx))
		_, err = s.PostMessage(ctx, "p", "poster", "hello @Reader again", nil)
		require.NoError(t, err)
		newest, indexed := gap()
		if newest != indexed+1 {
			continue // this post took more than one id: catch up and post again
		}
		found = true
		s.mentionMu.Lock()
		done := make(chan error, 1)
		go func() { done <- s.catchUpMentionIndex(ctx) }()
		select {
		case <-done:
			s.mentionMu.Unlock()
			t.Fatal("an index one message behind was read as current")
		case <-time.After(300 * time.Millisecond):
		}
		s.mentionMu.Unlock()
		require.NoError(t, <-done)
		newest, indexed = gap()
		require.Equal(t, newest, indexed, "the message one behind was indexed")
	}
	require.True(t, found, "a post that moved the newest id by exactly one was found")
}
