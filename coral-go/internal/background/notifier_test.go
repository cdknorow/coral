package background

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/terminalinput"
)

// mockRuntime records SendInput calls for verification.
type mockRuntime struct {
	mu   sync.Mutex
	sent []sendCall
}

type sendCall struct {
	session string
	text    string
}

func (m *mockRuntime) SpawnAgent(_ context.Context, _, _, _, _ string) error { return nil }
func (m *mockRuntime) SendInput(_ context.Context, _ terminalinput.Origin, name, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	s, err := board.NewStoreWithKey(dbPath, "")
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
	_, err := bs.Subscribe(ctx, "board-A", "Orchestrator", "Orchestrator", "claude-aaaa-1111", nil, "")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "board-B", "Orchestrator", "Orchestrator", "claude-bbbb-2222", nil, "")
	require.NoError(t, err)

	// Post a message on board-B that mentions the Orchestrator (using @all)
	// We need another subscriber to post the message.
	_, err = bs.Subscribe(ctx, "board-B", "Lead Dev", "Lead Dev", "claude-cccc-3333", nil, "")
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

func TestBoardNotifier_SessionIdentityOverridesStaleDisplayName(t *testing.T) {
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	ctx := context.Background()
	project := "death-or-trade-ai-auto"
	n := NewBoardNotifier(bs, rt, time.Second)
	n.SetIsPausedFn(func(string) bool { return false })
	_, err := bs.Subscribe(ctx, project, "Orchestrator", "Orchestrator", "codex-orch", nil, "all")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, project, "QA Engineer", "QA Engineer", "codex-qa", nil, "mentions")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, project, "Poster", "Worker", "codex-poster", nil, "all")
	require.NoError(t, err)
	_, err = bs.PostMessage(ctx, project, "Poster", "@Orchestrator inspect the queue", nil)
	require.NoError(t, err)

	// Replacement metadata can briefly report stale display names. Exact
	// session-name subscription identity must still route unread checks to the
	// matching board subscriber, rather than nudging QA for Orchestrator mail.
	n.SetDiscoverFn(func(context.Context) ([]AgentInfo, error) {
		return []AgentInfo{
			{AgentType: "codex", SessionID: "orch", DisplayName: "QA Engineer"},
			{AgentType: "codex", SessionID: "qa", DisplayName: "Orchestrator"},
		}, nil
	})
	require.NoError(t, n.RunOnce(ctx))
	require.Len(t, rt.sent, 1)
	assert.Equal(t, "codex-orch", rt.sent[0].session)
}

func TestBoardNotifier_RejectsAbsentSessionDisplayNameFallback(t *testing.T) {
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	ctx := context.Background()
	n := NewBoardNotifier(bs, rt, time.Second)
	n.SetIsPausedFn(func(string) bool { return false })
	_, err := bs.Subscribe(ctx, "team-a", "Orchestrator", "Orchestrator", "codex-a", nil, "all")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "team-b", "QA Engineer", "QA Engineer", "codex-b", nil, "mentions")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "team-a", "Poster", "Worker", "codex-poster", nil, "all")
	require.NoError(t, err)
	_, err = bs.PostMessage(ctx, "team-a", "Poster", "@Orchestrator team-a only", nil)
	require.NoError(t, err)
	n.SetDiscoverFn(func(context.Context) ([]AgentInfo, error) {
		return []AgentInfo{
			{AgentType: "codex", SessionID: "a", DisplayName: "QA Engineer"},
			{AgentType: "codex", SessionID: "b", DisplayName: "Orchestrator"},
			{AgentType: "codex", SessionID: "missing", DisplayName: "Orchestrator"},
		}, nil
	})
	require.NoError(t, n.RunOnce(ctx))
	require.Len(t, rt.sent, 1)
	assert.Equal(t, "codex-a", rt.sent[0].session)
}

func TestBoardNotifier_RejectsUnboundLegacySubscription(t *testing.T) {
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	ctx := context.Background()
	n := NewBoardNotifier(bs, rt, time.Second)
	n.SetIsPausedFn(func(string) bool { return false })
	_, err := bs.Subscribe(ctx, "legacy-team", "Orchestrator", "Orchestrator", "", nil, "all")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "legacy-team", "Poster", "Worker", "codex-poster", nil, "all")
	require.NoError(t, err)
	_, err = bs.PostMessage(ctx, "legacy-team", "Poster", "@Orchestrator legacy binding", nil)
	require.NoError(t, err)

	// An empty session_name cannot establish which discovered session owns the
	// subscription. Withhold delivery until the agent re-registers exactly.
	n.SetDiscoverFn(func(context.Context) ([]AgentInfo, error) {
		return []AgentInfo{
			{AgentType: "codex", SessionID: "one", DisplayName: "Orchestrator"},
			{AgentType: "codex", SessionID: "two", DisplayName: "Orchestrator"},
		}, nil
	})
	require.NoError(t, n.RunOnce(ctx))
	assert.Empty(t, rt.sent)
}

func TestBoardNotifier_HealthOnlyNudgeIsDistinct(t *testing.T) {
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	ctx := context.Background()
	n := NewBoardNotifier(bs, rt, time.Second)
	_, err := bs.Subscribe(ctx, "health-team", "Orchestrator", "Orchestrator", "codex-health", nil, "all")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "health-team", "Poster", "Worker", "codex-poster", nil, "all")
	require.NoError(t, err)
	_, err = bs.PostMessage(ctx, "health-team", "Coral Health Monitor", "[Coral team status] @Orchestrator review blockers", nil)
	require.NoError(t, err)
	n.SetDiscoverFn(func(context.Context) ([]AgentInfo, error) {
		return []AgentInfo{{AgentType: "codex", SessionID: "health", DisplayName: "Orchestrator"}}, nil
	})
	require.NoError(t, n.RunOnce(ctx))
	require.Len(t, rt.sent, 1)
	assert.Contains(t, rt.sent[0].text, "Team health check for health-team")
	assert.Contains(t, rt.sent[0].text, "authoritative full content")
	assert.Contains(t, rt.sent[0].text, "coral-board read")
}

func TestBoardNotifier_NormalAndMixedBatchesPreserveClassification(t *testing.T) {
	ctx := context.Background()
	newNotifier := func(t *testing.T, project string) (*BoardNotifier, *mockRuntime, *board.Store) {
		t.Helper()
		bs := testBoardStore(t)
		rt := &mockRuntime{}
		_, err := bs.Subscribe(ctx, project, "Orchestrator", "Orchestrator", "codex-"+project, nil, "all")
		require.NoError(t, err)
		_, err = bs.Subscribe(ctx, project, "Poster", "Worker", "poster-"+project, nil, "all")
		require.NoError(t, err)
		n := NewBoardNotifier(bs, rt, time.Second)
		n.SetDiscoverFn(func(context.Context) ([]AgentInfo, error) {
			return []AgentInfo{{AgentType: "codex", SessionID: project, DisplayName: "Orchestrator"}}, nil
		})
		return n, rt, bs
	}

	normal, normalRT, normalBS := newNotifier(t, "normal-team")
	_, err := normalBS.PostMessage(ctx, "normal-team", "Poster", "[Coral team status] @Orchestrator worker text", nil)
	require.NoError(t, err)
	require.NoError(t, normal.RunOnce(ctx))
	require.Len(t, normalRT.sent, 1)
	assert.NotContains(t, normalRT.sent[0].text, "Team health check")
	assert.Contains(t, normalRT.sent[0].text, "coral-board read")

	mixed, mixedRT, mixedBS := newNotifier(t, "mixed-team")
	_, err = mixedBS.PostMessage(ctx, "mixed-team", "Poster", "ordinary worker update", nil)
	require.NoError(t, err)
	_, err = mixedBS.PostMessage(ctx, "mixed-team", "Coral Health Monitor", "[Coral board health] @Orchestrator inspect blockers", nil)
	require.NoError(t, err)
	require.NoError(t, mixed.RunOnce(ctx))
	require.Len(t, mixedRT.sent, 1)
	assert.NotContains(t, mixedRT.sent[0].text, "Team health check")
	assert.Contains(t, mixedRT.sent[0].text, "2 unread messages")
}

func TestBoardNotifier_HealthClassificationIsolatedByRegisteredTeam(t *testing.T) {
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	ctx := context.Background()
	for _, project := range []string{"team-a", "team-b"} {
		_, err := bs.Subscribe(ctx, project, "Lead Coordinator", "Orchestrator", "codex-"+project, nil, "all")
		require.NoError(t, err)
		_, err = bs.Subscribe(ctx, project, "Poster", "Worker", "poster-"+project, nil, "all")
		require.NoError(t, err)
	}
	_, err := bs.PostMessage(ctx, "team-a", "Coral Health Monitor", "[Coral team status] @Lead Coordinator review blockers", nil)
	require.NoError(t, err)
	_, err = bs.PostMessage(ctx, "team-b", "Poster", "team-b ordinary update", nil)
	require.NoError(t, err)
	n := NewBoardNotifier(bs, rt, time.Second)
	n.SetDiscoverFn(func(context.Context) ([]AgentInfo, error) {
		return []AgentInfo{
			{AgentType: "codex", SessionID: "team-a", DisplayName: "Stale"},
			{AgentType: "codex", SessionID: "team-b", DisplayName: "Stale"},
		}, nil
	})
	require.NoError(t, n.RunOnce(ctx))
	require.Len(t, rt.sent, 2)
	assert.Contains(t, rt.sent[0].text, "Team health check for team-a")
	assert.NotContains(t, rt.sent[1].text, "Team health check")
}

func TestBoardNotifier_SuppressesHealthOnlyWorkerNudgeButPreservesMixedOrdinary(t *testing.T) {
	ctx := context.Background()
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	_, err := bs.Subscribe(ctx, "private-health", "Orchestrator", "Orchestrator", "codex-orch", nil, "all")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "private-health", "Artist UI/UX", "Worker", "codex-worker", nil, "all")
	require.NoError(t, err)
	_, err = bs.PostMessage(ctx, "private-health", "Coral Health Monitor", "[Coral team status] @Orchestrator review blockers", nil)
	require.NoError(t, err)
	n := NewBoardNotifier(bs, rt, time.Second)
	n.SetDiscoverFn(func(context.Context) ([]AgentInfo, error) {
		return []AgentInfo{{AgentType: "codex", SessionID: "worker", DisplayName: "Artist UI/UX"}}, nil
	})
	require.NoError(t, n.RunOnce(ctx))
	assert.Empty(t, rt.sent, "health-only unread backlog must not nudge a worker")

	_, err = bs.PostMessage(ctx, "private-health", "Poster", "@Artist UI/UX ordinary task update", nil)
	require.NoError(t, err)
	require.NoError(t, n.RunOnce(ctx))
	require.Len(t, rt.sent, 1, "mixed health and ordinary unread batch still notifies worker")
	assert.NotContains(t, rt.sent[0].text, "Team health check")
	assert.Contains(t, rt.sent[0].text, "coral-board read")
}

func TestBoardNotifier_SuppressesHealthBacklogForMentionOnlyWorker(t *testing.T) {
	ctx := context.Background()
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	_, err := bs.Subscribe(ctx, "mention-health", "Orchestrator", "Orchestrator", "codex-orch", nil, "all")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "mention-health", "Artist UI/UX", "Worker", "codex-worker", nil, "mentions")
	require.NoError(t, err)
	_, err = bs.PostMessage(ctx, "mention-health", "Coral Health Monitor", "[Coral team status] @Orchestrator stale worker summary", nil)
	require.NoError(t, err)
	n := NewBoardNotifier(bs, rt, time.Second)
	n.SetDiscoverFn(func(context.Context) ([]AgentInfo, error) {
		return []AgentInfo{{AgentType: "codex", SessionID: "worker", DisplayName: "Artist UI/UX"}}, nil
	})
	require.NoError(t, n.RunOnce(ctx))
	assert.Empty(t, rt.sent, "health backlog not mentioning a mentions-only worker must not notify it")

	_, err = bs.PostMessage(ctx, "mention-health", "Poster", "@Artist UI/UX please review the new fixture", nil)
	require.NoError(t, err)
	require.NoError(t, n.RunOnce(ctx))
	require.Len(t, rt.sent, 1)
	assert.Contains(t, rt.sent[0].text, "1 unread message")
	assert.NotContains(t, rt.sent[0].text, "Team health check")
}

// TestBoardNotifier_NoUnreadNoNudge verifies no nudge is sent when there are
// no unread messages.
func TestBoardNotifier_NoUnreadNoNudge(t *testing.T) {
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	ctx := context.Background()

	notifier := NewBoardNotifier(bs, rt, 10*time.Second)
	notifier.SetIsPausedFn(func(_ string) bool { return false })

	_, err := bs.Subscribe(ctx, "board-X", "Worker", "Worker", "claude-xxxx-0001", nil, "")
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
	_, err := bs.Subscribe(ctx, "paused-board", "Agent", "Agent", "claude-pppp-0001", nil, "")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "paused-board", "Poster", "Poster", "claude-pppp-0002", nil, "")
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

	_, err := bs.Subscribe(ctx, "proj", "Dev", "Dev", "claude-dddd-0001", nil, "")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "proj", "Poster", "Poster", "claude-dddd-0002", nil, "")
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

func TestBoardNotifier_OrchestratorGetsNudgeForNewUnreadBatch(t *testing.T) {
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	ctx := context.Background()
	notifier := NewBoardNotifier(bs, rt, 10*time.Second)
	notifier.SetIsPausedFn(func(_ string) bool { return false })
	_, err := bs.Subscribe(ctx, "proj", "Orchestrator", "Orchestrator", "claude-orch-1", nil, "all")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "proj", "Worker", "Worker", "claude-worker-1", nil, "")
	require.NoError(t, err)
	notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
		return []AgentInfo{{AgentName: "orch", AgentType: "claude", SessionID: "orch-1", DisplayName: "Orchestrator"}}, nil
	})
	_, err = bs.PostMessage(ctx, "proj", "Worker", "first", nil)
	require.NoError(t, err)
	require.NoError(t, notifier.RunOnce(ctx))
	require.Len(t, rt.sent, 1)
	_, err = bs.PostMessage(ctx, "proj", "Worker", "second", nil)
	require.NoError(t, err)
	require.NoError(t, notifier.RunOnce(ctx))
	require.Len(t, rt.sent, 2, "new messages should immediately nudge the orchestrator")
}

func TestBoardNotifier_NewMessageAfterReadWithSameUnreadCount(t *testing.T) {
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	ctx := context.Background()
	notifier := NewBoardNotifier(bs, rt, 10*time.Second)
	notifier.SetIsPausedFn(func(_ string) bool { return false })
	_, err := bs.Subscribe(ctx, "proj", "Orchestrator", "Orchestrator", "claude-orch-same", nil, "all")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "proj", "Worker", "Worker", "claude-worker-same", nil, "")
	require.NoError(t, err)
	notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
		return []AgentInfo{{AgentName: "orch", AgentType: "claude", SessionID: "orch-same", DisplayName: "Orchestrator"}}, nil
	})
	_, err = bs.PostMessage(ctx, "proj", "Worker", "first", nil)
	require.NoError(t, err)
	require.NoError(t, notifier.RunOnce(ctx))
	require.Len(t, rt.sent, 1)
	// The first message is read, then a new message arrives before the next
	// notifier pass. Both batches have count=1, but the second ID is new.
	_, err = bs.ReadMessages(ctx, "proj", "Orchestrator", 50)
	require.NoError(t, err)
	_, err = bs.PostMessage(ctx, "proj", "Worker", "second", nil)
	require.NoError(t, err)
	require.NoError(t, notifier.RunOnce(ctx))
	require.Len(t, rt.sent, 2, "new message ID must re-notify after the prior batch was read")
}

// TestBoardNotifier_MentionAfterReadBeforeNextPass: a worker (mentions mode) reads the
// batch it was nudged for, and a new mention arrives before a notifier pass sees
// unread == 0. The new mention is a new batch: it is nudged at once, not after the
// reminder (#3480). Mentions arriving before that read still share one nudge.
func TestBoardNotifier_MentionAfterReadBeforeNextPass(t *testing.T) {
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	ctx := context.Background()

	clock := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	notifier := NewBoardNotifier(bs, rt, 10*time.Second)
	notifier.now = func() time.Time { return clock }
	notifier.SetIsPausedFn(func(_ string) bool { return false })
	notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
		return []AgentInfo{{AgentName: "worker", AgentType: "agy", SessionID: "ffff-0001", DisplayName: "Worker"}}, nil
	})
	_, err := bs.Subscribe(ctx, "proj", "Worker", "Worker", "agy-ffff-0001", nil, "mentions")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "proj", "Operator", "Operator", "", nil, "")
	require.NoError(t, err)
	mention := func(text string) {
		_, err := bs.PostMessage(ctx, "proj", "Operator", "@Worker "+text, nil)
		require.NoError(t, err)
	}

	mention("first")
	require.NoError(t, notifier.RunOnce(ctx))
	require.Len(t, rt.sent, 1)

	// A second mention before the agent reads: covered by the first nudge.
	clock = clock.Add(time.Minute)
	mention("second")
	require.NoError(t, notifier.RunOnce(ctx))
	require.Len(t, rt.sent, 1, "mentions before a read share one nudge")

	// The agent reads both; a third mention arrives before the next pass.
	_, err = bs.ReadMessages(ctx, "proj", "Worker", 50)
	require.NoError(t, err)
	clock = clock.Add(time.Minute)
	mention("third")
	require.NoError(t, notifier.RunOnce(ctx))
	require.Len(t, rt.sent, 2, "a mention after the read is nudged at once, not after remindAfter")
	assert.Contains(t, rt.sent[1].text, "1 unread message")

	// And it is not repeated on the next passes.
	clock = clock.Add(time.Minute)
	require.NoError(t, notifier.RunOnce(ctx))
	require.NoError(t, notifier.RunOnce(ctx))
	assert.Len(t, rt.sent, 2, "one nudge for the new batch")
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

	_, err := bs.Subscribe(ctx, "proj", "Dev", "Dev", "claude-eeee-0001", nil, "")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "proj", "Poster", "Poster", "claude-eeee-0002", nil, "")
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

// TestBoardNotifier_MultiSessionNoDuplicateNudge verifies that when multiple
// sessions share the same role title (e.g. Orchestrator on proj-A with 0 unreads
// and Orchestrator on proj-B with 1 unread), the 0-unread session does not
// clobber the notification state of the other session and trigger repeated nudges.
func TestBoardNotifier_MultiSessionNoDuplicateNudge(t *testing.T) {
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	ctx := context.Background()

	notifier := NewBoardNotifier(bs, rt, 10*time.Second)
	notifier.SetIsPausedFn(func(_ string) bool { return false })

	// Session 1: Orchestrator on proj-A (0 unread messages)
	_, err := bs.Subscribe(ctx, "proj-A", "Orchestrator", "Orchestrator", "codex-orch-1", nil, "all")
	require.NoError(t, err)

	// Session 2: Orchestrator on proj-B (1 unread message)
	_, err = bs.Subscribe(ctx, "proj-B", "Orchestrator", "Orchestrator", "codex-orch-2", nil, "all")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "proj-B", "Worker", "Worker", "codex-worker-2", nil, "")
	require.NoError(t, err)
	_, err = bs.PostMessage(ctx, "proj-B", "Worker", "hello orchestrator", nil)
	require.NoError(t, err)

	notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
		return []AgentInfo{
			{AgentName: "orch-1", AgentType: "codex", SessionID: "orch-1", DisplayName: "Orchestrator"},
			{AgentName: "orch-2", AgentType: "codex", SessionID: "orch-2", DisplayName: "Orchestrator"},
		}, nil
	})

	// Pass 1: should send exactly one nudge to codex-orch-2
	require.NoError(t, notifier.RunOnce(ctx))
	require.Len(t, rt.sent, 1)
	assert.Equal(t, "codex-orch-2", rt.sent[0].session)

	// Pass 2: no new messages, should NOT send another nudge
	require.NoError(t, notifier.RunOnce(ctx))
	assert.Len(t, rt.sent, 1, "should not send duplicate nudge when unread count has not changed")

	// Pass 3: still no new messages, should NOT send another nudge
	require.NoError(t, notifier.RunOnce(ctx))
	assert.Len(t, rt.sent, 1, "should not send duplicate nudge on subsequent passes")
}

// TestBoardNotifier_SeedFromDBPreventsInitialDuplicate verifies that pre-existing
// unread counts loaded on startup via SeedFromDB are keyed by session_name and
// prevent immediate duplicate nudges on the first notifier pass.
func TestBoardNotifier_SeedFromDBPreventsInitialDuplicate(t *testing.T) {
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	ctx := context.Background()

	_, err := bs.Subscribe(ctx, "proj", "Orchestrator", "Orchestrator", "codex-orch-1", nil, "all")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "proj", "Worker", "Worker", "codex-worker-1", nil, "")
	require.NoError(t, err)
	_, err = bs.PostMessage(ctx, "proj", "Worker", "pre-existing unread message", nil)
	require.NoError(t, err)

	notifier := NewBoardNotifier(bs, rt, 10*time.Second)
	notifier.SetIsPausedFn(func(_ string) bool { return false })
	notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
		return []AgentInfo{{AgentName: "orch", AgentType: "codex", SessionID: "orch-1", DisplayName: "Orchestrator"}}, nil
	})

	// Seed notifier from DB as done during startup
	notifier.SeedFromDB(ctx)

	// First pass after restart: should NOT immediately nudge for pre-existing unread messages
	require.NoError(t, notifier.RunOnce(ctx))
	assert.Empty(t, rt.sent, "pre-existing unread messages should be seeded and not trigger immediate nudge on startup")
}

// #3875: the startup seed covers the agents running now, not every subscriber a board ever
// had (seeding those made the first nudge wait minutes on a long board).
func TestBoardNotifier_SeedsOnlyRunningAgents(t *testing.T) {
	bs := testBoardStore(t)
	ctx := context.Background()
	for _, s := range []struct{ id, session string }{{"Worker", "claude-run-0001"}, {"Gone", "claude-gone-0002"}} {
		_, err := bs.Subscribe(ctx, "proj", s.id, s.id, s.session, nil, "all")
		require.NoError(t, err)
	}
	_, err := bs.PostMessage(ctx, "proj", "Lead", "unread for both", nil)
	require.NoError(t, err)
	notifier := NewBoardNotifier(bs, &mockRuntime{}, 10*time.Second)
	notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
		return []AgentInfo{{AgentName: "w", AgentType: "claude", SessionID: "run-0001", DisplayName: "Worker"}}, nil
	})
	notifier.SeedFromDB(ctx)
	notifier.notifiedMu.Lock()
	defer notifier.notifiedMu.Unlock()
	require.Len(t, notifier.notified, 1)
	require.Equal(t, 1, notifier.notified["claude-run-0001"].count)
}

// heldScreenTerm shows a permission menu, so whatever is sent to it is held.
type heldScreenTerm struct{}

func (heldScreenTerm) AgentScreen(context.Context, string, string, string) (string, error) {
	return " Do you want to proceed?\n ❯ 1. Yes\n   2. No\n\n Esc to cancel\n", nil
}

// #3482: a nudge held while the agent works (signals mode holds mid-turn) is retired
// once the notifier sees the agent read its board, instead of being typed later into
// an agent with nothing to read. Other held sends stay.
func TestBoardNotifier_RetiresAHeldNudgeOnceRead(t *testing.T) {
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	ctx := context.Background()
	notifier := NewBoardNotifier(bs, rt, 10*time.Second)
	notifier.SetIsPausedFn(func(_ string) bool { return false })
	_, err := bs.Subscribe(ctx, "board-X", "Worker", "Worker", "claude-xxxx-0001", nil, "")
	require.NoError(t, err)
	notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
		return []AgentInfo{{AgentName: "worker-agent", AgentType: "claude", SessionID: "xxxx-0001", DisplayName: "Worker"}}, nil
	})
	name := "claude-xxxx-0001"
	t.Cleanup(func() { ptymanager.DropHeld(name, "test done") })
	for _, key := range []string{"board-unread:board-X", "agent-ui"} {
		d, err := ptymanager.DeliverAutomated(ctx, ptymanager.AutomatedInput{Term: heldScreenTerm{}, Name: name, Key: key, Text: key, What: "test",
			Send: func(context.Context) error { t.Error("typed while a menu is open"); return nil }})
		require.NoError(t, err)
		require.Equal(t, ptymanager.Held, d)
	}
	require.Equal(t, 2, ptymanager.HeldCount(name))

	require.NoError(t, notifier.RunOnce(ctx)) // nothing unread
	assert.Equal(t, 1, ptymanager.HeldCount(name), "the board's stale nudge is retired, the other send stays")
}

// The reminder window is spread per agent (#3613): never before 0.8 x remindAfter, always by
// 1.2 x, so reminders set in one pass don't all come due in the same one.
func TestBoardNotifier_ReminderWindowIsJittered(t *testing.T) {
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	ctx := context.Background()
	clock := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	notifier := NewBoardNotifier(bs, rt, 10*time.Second)
	notifier.now = func() time.Time { return clock }
	notifier.SetIsPausedFn(func(_ string) bool { return false })
	notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
		return []AgentInfo{{AgentName: "dev-agent", AgentType: "claude", SessionID: "eeee-0001", DisplayName: "Dev"}}, nil
	})
	_, err := bs.Subscribe(ctx, "proj", "Dev", "Dev", "claude-eeee-0001", nil, "")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "proj", "Poster", "Poster", "claude-eeee-0002", nil, "")
	require.NoError(t, err)
	_, err = bs.PostMessage(ctx, "proj", "Poster", "@Dev hello", nil)
	require.NoError(t, err)

	start := clock
	require.NoError(t, notifier.RunOnce(ctx))
	require.Len(t, rt.sent, 1)
	clock = start.Add(defaultRemindAfter * 79 / 100)
	require.NoError(t, notifier.RunOnce(ctx))
	assert.Len(t, rt.sent, 1, "no reminder before 0.8 x remindAfter")
	clock = start.Add(defaultRemindAfter * 121 / 100)
	require.NoError(t, notifier.RunOnce(ctx))
	assert.Len(t, rt.sent, 2, "a reminder by 1.2 x remindAfter")
	w := ptymanager.JitterAround(defaultRemindAfter, "claude-eeee-0001")
	assert.True(t, w >= defaultRemindAfter*8/10 && w <= defaultRemindAfter*12/10, "%v", w)
}
