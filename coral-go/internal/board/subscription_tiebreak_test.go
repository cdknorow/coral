package board

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5580 (found by #5573): a session with two active subscriptions is addressed the same way every time. The lookups pick the newest by subscribed_at and, when that ties (two
// subscriptions made in one tick), the one made last: the highest row id. Before, a tie was broken by whatever order SQLite happened to read.

func TestSessionLookupsBreakATieOnSubscribedAtByTheNewestRow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const session = "claude-tie"
	for _, sc := range []struct{ project, id, title string }{
		{"p1", "first", "First"}, {"p1", "second", "Second"}, {"p2", "third", "Third"},
	} {
		_, err := s.Subscribe(ctx, sc.project, sc.id, sc.title, session, nil, "mentions")
		require.NoError(t, err)
	}
	// the same instant for all three: the tie the first three subscriptions of a fast wake produce
	_, err := s.db.ExecContext(ctx, "UPDATE board_subscribers SET subscribed_at = '2026-10-21T08:00:00Z'")
	require.NoError(t, err)

	for i := 0; i < 50; i++ {
		got, err := s.GetSubscriptionBySessionName(ctx, session)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, "third", got.SubscriberID, "the last made of the tied subscriptions, every time (call %d)", i)
	}
	inProject, err := s.SessionSubscription(ctx, "p1", session)
	require.NoError(t, err)
	require.NotNil(t, inProject)
	assert.Equal(t, "second", inProject.SubscriberID, "within one board too")

	batch, err := s.UnreadForSessions(ctx, []string{session})
	require.NoError(t, err)
	require.NotNil(t, batch[session].Sub)
	assert.Equal(t, "third", batch[session].Sub.SubscriberID, "the batch lookup agrees with GetSubscriptionBySessionName")
}

func TestASubscriberInTwoBoardsWithTheSameInstantIsTheNewestRow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, p := range []string{"p1", "p2", "p3"} {
		_, err := s.Subscribe(ctx, p, "dev", "Dev", "tmux-"+p, nil, "mentions")
		require.NoError(t, err)
	}
	_, err := s.db.ExecContext(ctx, "UPDATE board_subscribers SET subscribed_at = '2026-10-21T08:00:00Z'")
	require.NoError(t, err)
	for i := 0; i < 50; i++ {
		got, err := s.GetSubscription(ctx, "dev")
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, "p3", got.Project, "call %d", i)
	}
}

// A later subscribed_at still wins over a higher row id: the tie-break is only for a tie.
func TestANewerSubscribedAtStillWinsOverAHigherRow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const session = "claude-order"
	for _, id := range []string{"old-low", "new-high"} {
		_, err := s.Subscribe(ctx, "p1", id, id, session, nil, "mentions")
		require.NoError(t, err)
	}
	_, err := s.db.ExecContext(ctx, "UPDATE board_subscribers SET subscribed_at = '2026-10-21T09:00:00Z' WHERE subscriber_id = 'old-low'")
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, "UPDATE board_subscribers SET subscribed_at = '2026-10-21T08:00:00Z' WHERE subscriber_id = 'new-high'")
	require.NoError(t, err)
	got, err := s.GetSubscriptionBySessionName(ctx, session)
	require.NoError(t, err)
	assert.Equal(t, "old-low", got.SubscriberID, "the newer subscribed_at, though its row was made first")
}
