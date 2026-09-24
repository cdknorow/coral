package background

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/board"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockRuntime records SendInput calls for verification.
type mockRuntime struct {
	sent []sendCall
}

type sendCall struct {
	session string
	text    string
}

func (m *mockRuntime) SpawnAgent(_ context.Context, _, _, _, _ string) error { return nil }
func (m *mockRuntime) SendInput(_ context.Context, name, text string) error {
	m.sent = append(m.sent, sendCall{session: name, text: text})
	return nil
}
func (m *mockRuntime) KillAgent(_ context.Context, _ string) error { return nil }
func (m *mockRuntime) IsAlive(_ context.Context, _ string) bool    { return true }
func (m *mockRuntime) ListAgents(_ context.Context) ([]AgentInfo, error) {
	return nil, nil
}

func testBoardStore(t *testing.T) *board.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "notifier_test.db")
	s, err := board.NewStore(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

// TestBoardNotifier_StaleSubscription verifies that when the same subscriber_id
// ("Orchestrator") exists on two boards, the notifier uses the session_name
// lookup to find the correct board, not the stale one.
func TestBoardNotifier_StaleSubscription(t *testing.T) {
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	ctx := context.Background()

	notifier := NewBoardNotifier(bs, rt, 10*time.Second)
	notifier.SetIsPausedFn(func(_ string) bool { return false })

	// Subscribe "Orchestrator" to two different boards with distinct session names.
	// board-A is the stale/old subscription, board-B is the current one.
	_, err := bs.Subscribe(ctx, "board-A", "Orchestrator", "Orchestrator", "claude-aaaa-1111", nil, nil, "")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "board-B", "Orchestrator", "Orchestrator", "claude-bbbb-2222", nil, nil, "")
	require.NoError(t, err)

	// Post a message on board-B that mentions the Orchestrator (using @all)
	// We need another subscriber to post the message.
	_, err = bs.Subscribe(ctx, "board-B", "Lead Dev", "Lead Dev", "claude-cccc-3333", nil, nil, "")
	require.NoError(t, err)
	_, err = bs.PostMessage(ctx, "board-B", "Lead Dev", "@Orchestrator please review", nil)
	require.NoError(t, err)

	// Set up discovery to return an agent whose session name maps to board-B.
	// The agent's DisplayName is "Orchestrator" and its session ID produces
	// session name "claude-bbbb-2222" via naming.SessionName("claude", "bbbb-2222").
	notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
		return []AgentInfo{
			{
				AgentName:   "orchestrator-agent",
				AgentType:   "claude",
				SessionID:   "bbbb-2222",
				DisplayName: "Orchestrator",
			},
		}, nil
	})

	// Run one notification pass.
	err = notifier.RunOnce(ctx)
	require.NoError(t, err)

	// The notifier should have sent a nudge to the correct session (board-B's session).
	require.Len(t, rt.sent, 1, "expected exactly one nudge")
	assert.Equal(t, "claude-bbbb-2222", rt.sent[0].session,
		"nudge should target the session for board-B, not the stale board-A")
	assert.Contains(t, rt.sent[0].text, "unread message",
		"nudge text should mention unread messages")
}

// TestBoardNotifier_NoUnreadNoNudge verifies no nudge is sent when there are
// no unread messages.
func TestBoardNotifier_NoUnreadNoNudge(t *testing.T) {
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	ctx := context.Background()

	notifier := NewBoardNotifier(bs, rt, 10*time.Second)
	notifier.SetIsPausedFn(func(_ string) bool { return false })

	_, err := bs.Subscribe(ctx, "board-X", "Worker", "Worker", "claude-xxxx-0001", nil, nil, "")
	require.NoError(t, err)

	notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
		return []AgentInfo{
			{
				AgentName:   "worker-agent",
				AgentType:   "claude",
				SessionID:   "xxxx-0001",
				DisplayName: "Worker",
			},
		}, nil
	})

	err = notifier.RunOnce(ctx)
	require.NoError(t, err)

	assert.Empty(t, rt.sent, "no nudge expected when no unread messages")
}

// TestBoardNotifier_PausedBoardSkipped verifies nudges are not sent for paused boards.
func TestBoardNotifier_PausedBoardSkipped(t *testing.T) {
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	ctx := context.Background()

	notifier := NewBoardNotifier(bs, rt, 10*time.Second)
	notifier.SetIsPausedFn(func(project string) bool {
		return project == "paused-board"
	})

	// Create subscription and unread message on a paused board.
	_, err := bs.Subscribe(ctx, "paused-board", "Agent", "Agent", "claude-pppp-0001", nil, nil, "")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "paused-board", "Poster", "Poster", "claude-pppp-0002", nil, nil, "")
	require.NoError(t, err)
	_, err = bs.PostMessage(ctx, "paused-board", "Poster", "@Agent wake up", nil)
	require.NoError(t, err)

	notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
		return []AgentInfo{
			{
				AgentName:   "agent",
				AgentType:   "claude",
				SessionID:   "pppp-0001",
				DisplayName: "Agent",
			},
		}, nil
	})

	err = notifier.RunOnce(ctx)
	require.NoError(t, err)

	assert.Empty(t, rt.sent, "no nudge expected for paused board")
}

// TestBoardNotifier_DeduplicatesNotifications verifies the same unread count
// doesn't trigger repeated nudges.
func TestBoardNotifier_DeduplicatesNotifications(t *testing.T) {
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	ctx := context.Background()

	notifier := NewBoardNotifier(bs, rt, 10*time.Second)
	notifier.SetIsPausedFn(func(_ string) bool { return false })

	_, err := bs.Subscribe(ctx, "proj", "Dev", "Dev", "claude-dddd-0001", nil, nil, "")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "proj", "Poster", "Poster", "claude-dddd-0002", nil, nil, "")
	require.NoError(t, err)
	_, err = bs.PostMessage(ctx, "proj", "Poster", "@Dev check this", nil)
	require.NoError(t, err)

	notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
		return []AgentInfo{
			{
				AgentName:   "dev-agent",
				AgentType:   "claude",
				SessionID:   "dddd-0001",
				DisplayName: "Dev",
			},
		}, nil
	})

	// First pass: should send nudge.
	err = notifier.RunOnce(ctx)
	require.NoError(t, err)
	require.Len(t, rt.sent, 1)

	// Second pass with same unread count: should NOT send again.
	err = notifier.RunOnce(ctx)
	require.NoError(t, err)
	assert.Len(t, rt.sent, 1, "should not re-nudge for the same unread count")
}

// TestBoardNotifier_OneNudgePerBatch verifies more messages arriving before
// the agent reads do not stack up nudges, a reminder goes out once they have
// sat unread for remindAfter, and reading resets it.
func TestBoardNotifier_OneNudgePerBatch(t *testing.T) {
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	ctx := context.Background()

	clock := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	notifier := NewBoardNotifier(bs, rt, 10*time.Second)
	notifier.now = func() time.Time { return clock }
	notifier.SetIsPausedFn(func(_ string) bool { return false })
	notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
		return []AgentInfo{{AgentName: "dev-agent", AgentType: "claude", SessionID: "eeee-0001", DisplayName: "Dev"}}, nil
	})

	_, err := bs.Subscribe(ctx, "proj", "Dev", "Dev", "claude-eeee-0001", nil, nil, "")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "proj", "Poster", "Poster", "claude-eeee-0002", nil, nil, "")
	require.NoError(t, err)
	post := func() {
		_, err := bs.PostMessage(ctx, "proj", "Poster", "@Dev another one", nil)
		require.NoError(t, err)
	}

	post()
	require.NoError(t, notifier.RunOnce(ctx))
	require.Len(t, rt.sent, 1)

	// Eight more arrive while the agent is busy: still one nudge
	for i := 0; i < 8; i++ {
		post()
		clock = clock.Add(time.Minute)
		require.NoError(t, notifier.RunOnce(ctx))
	}
	assert.Len(t, rt.sent, 1, "new messages before a read do not re-nudge")

	// Still unread well after the nudge: one reminder with the current count
	clock = clock.Add(defaultRemindAfter)
	require.NoError(t, notifier.RunOnce(ctx))
	require.Len(t, rt.sent, 2)
	assert.Contains(t, rt.sent[1].text, "9 unread messages")
	require.NoError(t, notifier.RunOnce(ctx))
	assert.Len(t, rt.sent, 2, "the reminder is not repeated right away")

	// Reading clears it; the next message nudges again
	_, err = bs.ReadMessages(ctx, "proj", "Dev", 100)
	require.NoError(t, err)
	require.NoError(t, notifier.RunOnce(ctx))
	post()
	require.NoError(t, notifier.RunOnce(ctx))
	require.Len(t, rt.sent, 3)
	assert.Contains(t, rt.sent[2].text, "1 unread message")
}
