package background

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBoardNotifier_IndependentSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	n := NewBoardNotifier(bs, rt, time.Second)
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	n.now = func() time.Time { return now }
	agents := []AgentInfo{
		{AgentType: "codex", SessionID: "one", DisplayName: "Dev"},
		{AgentType: "codex", SessionID: "two", DisplayName: "Dev"},
	}
	n.SetDiscoverFn(func(context.Context) ([]AgentInfo, error) { return agents, nil })
	for _, id := range []string{"one", "two"} {
		_, err := bs.Subscribe(ctx, id, "Dev", "Dev", "codex-"+id, nil, nil, "all")
		require.NoError(t, err)
		_, err = bs.Subscribe(ctx, id, "Poster", "Poster", "codex-poster-"+id, nil, nil, "all")
		require.NoError(t, err)
		_, err = bs.PostMessage(ctx, id, "Poster", "new message", nil)
		require.NoError(t, err)
	}
	require.NoError(t, n.RunOnce(ctx))
	require.Len(t, rt.sent, 2, "equal unread counts must not suppress the sibling")
	require.Equal(t, "codex-one", rt.sent[0].session)
	require.Equal(t, "codex-two", rt.sent[1].session)
	_, err := bs.ReadMessages(ctx, "one", "Dev", 100)
	require.NoError(t, err)
	require.NoError(t, n.RunOnce(ctx))
	require.Len(t, rt.sent, 2, "reading one session must not reset the sibling")
	_, err = bs.PostMessage(ctx, "one", "Poster", "after read", nil)
	require.NoError(t, err)
	require.NoError(t, n.RunOnce(ctx))
	require.Len(t, rt.sent, 3)
	require.Equal(t, "codex-one", rt.sent[2].session)
	now = now.Add(defaultRemindAfter)
	require.NoError(t, n.RunOnce(ctx))
	require.Len(t, rt.sent, 5, "both still-unread sessions receive their reminder")
	agents = agents[1:]
	require.NoError(t, n.RunOnce(ctx))
	require.NotContains(t, n.notified, "codex-one")
	require.Contains(t, n.notified, "codex-two")
	require.Len(t, rt.sent, 5, "removing sibling must retain surviving dedup state")
}
