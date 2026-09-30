package background

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestTask1645_ComprehensiveNotifierVerification tests all scenarios required by Task #1645.
func TestTask1645_ComprehensiveNotifierVerification(t *testing.T) {
	ctx := context.Background()

	t.Run("1_OriginalHypothesis_Orchestrator_UnreadA_ThenB", func(t *testing.T) {
		bs := testBoardStore(t)
		rt := &mockRuntime{}
		notifier := NewBoardNotifier(bs, rt, 10*time.Second)
		notifier.SetIsPausedFn(func(_ string) bool { return false })

		_, err := bs.Subscribe(ctx, "proj", "Orchestrator", "Orchestrator", "claude-orch-hypo", nil, nil, "all")
		require.NoError(t, err)
		_, err = bs.Subscribe(ctx, "proj", "Worker", "Worker", "claude-worker-hypo", nil, nil, "")
		require.NoError(t, err)

		notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
			return []AgentInfo{{AgentName: "orch", AgentType: "claude", SessionID: "orch-hypo", DisplayName: "Orchestrator"}}, nil
		})

		// Message A arrives
		_, err = bs.PostMessage(ctx, "proj", "Worker", "message A", nil)
		require.NoError(t, err)

		require.NoError(t, notifier.RunOnce(ctx))
		require.Len(t, rt.sent, 1, "nudge 1 sent for message A")
		require.Contains(t, rt.sent[0].text, "1 unread message")

		// Message A is LEFT UNREAD. Message B arrives!
		_, err = bs.PostMessage(ctx, "proj", "Worker", "message B", nil)
		require.NoError(t, err)

		require.NoError(t, notifier.RunOnce(ctx))
		require.Len(t, rt.sent, 2, "nudge 2 sent for message B when A left unread for receive_mode=all")
		require.Contains(t, rt.sent[1].text, "2 unread messages")
	})

	t.Run("2_OriginalHypothesis_WorkerMentions_UnreadA_ThenB_Behavior", func(t *testing.T) {
		bs := testBoardStore(t)
		rt := &mockRuntime{}
		notifier := NewBoardNotifier(bs, rt, 10*time.Second)
		notifier.SetIsPausedFn(func(_ string) bool { return false })

		_, err := bs.Subscribe(ctx, "proj", "Worker", "Worker", "claude-worker-sub", nil, nil, "mentions")
		require.NoError(t, err)

		notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
			return []AgentInfo{{AgentName: "worker", AgentType: "claude", SessionID: "worker-sub", DisplayName: "Worker"}}, nil
		})

		// Message A mentions worker
		_, err = bs.PostMessage(ctx, "proj", "Operator", "@Worker first task update", nil)
		require.NoError(t, err)

		require.NoError(t, notifier.RunOnce(ctx))
		require.Len(t, rt.sent, 1, "nudge 1 sent for message A")

		// Message A is LEFT UNREAD. Message B arrives also mentioning worker
		_, err = bs.PostMessage(ctx, "proj", "Operator", "@Worker urgent second task update", nil)
		require.NoError(t, err)

		require.NoError(t, notifier.RunOnce(ctx))
		// Under Lead's candidate (#1643), worker subscriptions retain count-based coalescing
		// where sub.ReceiveMode != "all" causes suppression.
		t.Logf("Worker nudges after B arrives unread: %d", len(rt.sent))
		// Notice: len(rt.sent) is 1 because worker was already notified for batch!
	})

	t.Run("3_LeadReproducedCase_SameCountAfterRead_SucceedsInCandidate", func(t *testing.T) {
		bs := testBoardStore(t)
		rt := &mockRuntime{}
		notifier := NewBoardNotifier(bs, rt, 10*time.Second)
		notifier.SetIsPausedFn(func(_ string) bool { return false })

		_, err := bs.Subscribe(ctx, "proj", "Orchestrator", "Orchestrator", "claude-orch-lead", nil, nil, "all")
		require.NoError(t, err)
		_, err = bs.Subscribe(ctx, "proj", "Worker", "Worker", "claude-worker-lead", nil, nil, "")
		require.NoError(t, err)

		notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
			return []AgentInfo{{AgentName: "orch", AgentType: "claude", SessionID: "orch-lead", DisplayName: "Orchestrator"}}, nil
		})

		// Message 1 arrives
		_, err = bs.PostMessage(ctx, "proj", "Worker", "msg 1", nil)
		require.NoError(t, err)
		require.NoError(t, notifier.RunOnce(ctx))
		require.Len(t, rt.sent, 1)

		// Message 1 is read
		_, err = bs.ReadMessages(ctx, "proj", "Orchestrator", 50)
		require.NoError(t, err)

		// Message 2 arrives (same unread count = 1, but new ID)
		_, err = bs.PostMessage(ctx, "proj", "Worker", "msg 2", nil)
		require.NoError(t, err)

		// With Lead's #1643 fix, latestID > last.latestID is true, so nudge 2 is delivered!
		require.NoError(t, notifier.RunOnce(ctx))
		require.Len(t, rt.sent, 2, "new batch with same count after read must be notified via latestID watermark")
	})

	t.Run("4_UnchangedUnreadSet_NoDuplicatesWithinReminderWindow", func(t *testing.T) {
		bs := testBoardStore(t)
		rt := &mockRuntime{}
		notifier := NewBoardNotifier(bs, rt, 10*time.Second)
		notifier.SetIsPausedFn(func(_ string) bool { return false })

		_, err := bs.Subscribe(ctx, "proj", "Orchestrator", "Orchestrator", "claude-orch-dup", nil, nil, "all")
		require.NoError(t, err)

		notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
			return []AgentInfo{{AgentName: "orch", AgentType: "claude", SessionID: "orch-dup", DisplayName: "Orchestrator"}}, nil
		})

		_, err = bs.PostMessage(ctx, "proj", "System", "msg 1", nil)
		require.NoError(t, err)

		require.NoError(t, notifier.RunOnce(ctx))
		require.Len(t, rt.sent, 1)

		// Run 5 consecutive passes without reading or new messages
		for i := 0; i < 5; i++ {
			require.NoError(t, notifier.RunOnce(ctx))
		}
		require.Len(t, rt.sent, 1, "unchanged unread set must not trigger duplicate nudges within reminder window")
	})

	t.Run("5_BurstCoalescing_SingleNudgeWithTotalCount", func(t *testing.T) {
		bs := testBoardStore(t)
		rt := &mockRuntime{}
		notifier := NewBoardNotifier(bs, rt, 10*time.Second)
		notifier.SetIsPausedFn(func(_ string) bool { return false })

		_, err := bs.Subscribe(ctx, "proj", "Orchestrator", "Orchestrator", "claude-orch-burst", nil, nil, "all")
		require.NoError(t, err)

		notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
			return []AgentInfo{{AgentName: "orch", AgentType: "claude", SessionID: "orch-burst", DisplayName: "Orchestrator"}}, nil
		})

		// 5 messages posted before first notifier tick
		for i := 1; i <= 5; i++ {
			_, err = bs.PostMessage(ctx, "proj", "Worker", fmt.Sprintf("burst %d", i), nil)
			require.NoError(t, err)
		}

		require.NoError(t, notifier.RunOnce(ctx))
		require.Len(t, rt.sent, 1, "burst must coalesce into single nudge")
		require.Contains(t, rt.sent[0].text, "5 unread messages")
	})

	t.Run("6_FailedDelivery_RetriesOnNextPass", func(t *testing.T) {
		bs := testBoardStore(t)
		rt := &mockFailingRuntime{failCount: 1} // fails first time, succeeds second
		notifier := NewBoardNotifier(bs, rt, 10*time.Second)
		notifier.SetIsPausedFn(func(_ string) bool { return false })

		_, err := bs.Subscribe(ctx, "proj", "Orchestrator", "Orchestrator", "claude-orch-fail", nil, nil, "all")
		require.NoError(t, err)

		notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
			return []AgentInfo{{AgentName: "orch", AgentType: "claude", SessionID: "orch-fail", DisplayName: "Orchestrator"}}, nil
		})

		_, err = bs.PostMessage(ctx, "proj", "Worker", "fail test", nil)
		require.NoError(t, err)

		// First pass fails delivery
		require.NoError(t, notifier.RunOnce(ctx))
		require.Len(t, rt.sent, 0, "first pass failed")

		// Second pass succeeds
		require.NoError(t, notifier.RunOnce(ctx))
		require.Len(t, rt.sent, 1, "second pass must retry and succeed")
	})

	t.Run("7_SystemMessagesFiltered_DoNotTriggerNudges", func(t *testing.T) {
		bs := testBoardStore(t)
		rt := &mockRuntime{}
		notifier := NewBoardNotifier(bs, rt, 10*time.Second)
		notifier.SetIsPausedFn(func(_ string) bool { return false })

		_, err := bs.Subscribe(ctx, "proj", "Orchestrator", "Orchestrator", "claude-orch-sys", nil, nil, "all")
		require.NoError(t, err)

		notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
			return []AgentInfo{{AgentName: "orch", AgentType: "claude", SessionID: "orch-sys", DisplayName: "Orchestrator"}}, nil
		})

		// Post from Coral Task Queue (system sender)
		_, err = bs.PostMessage(ctx, "proj", "Coral Task Queue", "Task #123 completed", nil)
		require.NoError(t, err)

		require.NoError(t, notifier.RunOnce(ctx))
		require.Len(t, rt.sent, 0, "system sender messages must not trigger user unread nudges")
	})

	t.Run("8_PerSessionAndTeamIsolation", func(t *testing.T) {
		bs := testBoardStore(t)
		rt := &mockRuntime{}
		notifier := NewBoardNotifier(bs, rt, 10*time.Second)
		notifier.SetIsPausedFn(func(_ string) bool { return false })

		_, err := bs.Subscribe(ctx, "teamA", "Orchestrator", "Orchestrator", "claude-orch-A", nil, nil, "all")
		require.NoError(t, err)
		_, err = bs.Subscribe(ctx, "teamB", "Orchestrator", "Orchestrator", "claude-orch-B", nil, nil, "all")
		require.NoError(t, err)

		notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
			return []AgentInfo{
				{AgentName: "orchA", AgentType: "claude", SessionID: "orch-A", DisplayName: "Orchestrator"},
				{AgentName: "orchB", AgentType: "claude", SessionID: "orch-B", DisplayName: "Orchestrator"},
			}, nil
		})

		// Post only to teamA
		_, err = bs.PostMessage(ctx, "teamA", "Worker", "msg in teamA", nil)
		require.NoError(t, err)

		require.NoError(t, notifier.RunOnce(ctx))
		require.Len(t, rt.sent, 1)
		require.Equal(t, "claude-orch-A", rt.sent[0].session)
	})
}

type mockFailingRuntime struct {
	mu        sync.Mutex
	failCount int
	sent      []sendCall
}

func (m *mockFailingRuntime) SpawnAgent(_ context.Context, _, _, _, _ string) error { return nil }
func (m *mockFailingRuntime) SendInput(_ context.Context, session, content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failCount > 0 {
		m.failCount--
		return errors.New("simulated runtime send failure")
	}
	m.sent = append(m.sent, sendCall{session: session, text: content})
	return nil
}
func (m *mockFailingRuntime) KillAgent(_ context.Context, _ string) error { return nil }
func (m *mockFailingRuntime) IsAlive(_ context.Context, _ string) bool    { return true }
func (m *mockFailingRuntime) ListAgents(_ context.Context) ([]AgentInfo, error) {
	return nil, nil
}

// TestSeedFromDB_NotifiesSubsequentSameCountBatch verifies startup seeds both
// count and latest eligible message ID, so a later same-count arrival nudges.
func TestSeedFromDB_NotifiesSubsequentSameCountBatch(t *testing.T) {
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	ctx := context.Background()
	notifier := NewBoardNotifier(bs, rt, 10*time.Second)
	notifier.SetIsPausedFn(func(_ string) bool { return false })

	_, err := bs.Subscribe(ctx, "proj", "Orchestrator", "Orchestrator", "claude-orch-seed", nil, nil, "all")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "proj", "Worker", "Worker", "claude-worker-seed", nil, nil, "")
	require.NoError(t, err)

	notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
		return []AgentInfo{{AgentName: "orch", AgentType: "claude", SessionID: "orch-seed", DisplayName: "Orchestrator"}}, nil
	})

	// Message 1 is posted before notifier starts
	_, err = bs.PostMessage(ctx, "proj", "Worker", "pre-existing message", nil)
	require.NoError(t, err)

	// SeedFromDB is called (simulating server startup)
	notifier.SeedFromDB(ctx)

	// Orchestrator reads message 1
	_, err = bs.ReadMessages(ctx, "proj", "Orchestrator", 50)
	require.NoError(t, err)

	// Message 2 arrives (same count = 1, but new message ID)
	_, err = bs.PostMessage(ctx, "proj", "Worker", "fresh post after startup", nil)
	require.NoError(t, err)

	// Run notifier pass
	require.NoError(t, notifier.RunOnce(ctx))

	require.Len(t, rt.sent, 1, "fresh same-count arrival after startup must notify")
}
