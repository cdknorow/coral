package background

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestTask1648_ComprehensiveStartupAndNotificationRetest independently exercises
// the full scope required by Task #1648.
func TestTask1648_ComprehensiveStartupAndNotificationRetest(t *testing.T) {
	ctx := context.Background()

	t.Run("1_StartupSuppressesExistingUnread_WithoutDuplicates", func(t *testing.T) {
		bs := testBoardStore(t)
		rt := &mockRuntime{}
		notifier := NewBoardNotifier(bs, rt, 10*time.Second)
		notifier.SetIsPausedFn(func(_ string) bool { return false })

		_, err := bs.Subscribe(ctx, "proj", "Orchestrator", "Orchestrator", "claude-orch-seed1", nil, nil, "all")
		require.NoError(t, err)

		notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
			return []AgentInfo{{AgentName: "orch", AgentType: "claude", SessionID: "orch-seed1", DisplayName: "Orchestrator"}}, nil
		})

		// Message 1 is in DB before startup
		_, err = bs.PostMessage(ctx, "proj", "Worker", "pre-existing unread", nil)
		require.NoError(t, err)

		// SeedFromDB simulates startup
		notifier.SeedFromDB(ctx)

		// Pass 1: existing unread batch must NOT be re-notified
		require.NoError(t, notifier.RunOnce(ctx))
		require.Len(t, rt.sent, 0, "startup must suppress nudges for pre-existing unread batch")

		// Pass 2: still no nudge
		require.NoError(t, notifier.RunOnce(ctx))
		require.Len(t, rt.sent, 0, "unchanged pre-existing unread batch must remain suppressed")
	})

	t.Run("2_StartupFollowedBySameCountArrival_NotifiesImmediately", func(t *testing.T) {
		bs := testBoardStore(t)
		rt := &mockRuntime{}
		notifier := NewBoardNotifier(bs, rt, 10*time.Second)
		notifier.SetIsPausedFn(func(_ string) bool { return false })

		_, err := bs.Subscribe(ctx, "proj", "Orchestrator", "Orchestrator", "claude-orch-seed2", nil, nil, "all")
		require.NoError(t, err)

		notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
			return []AgentInfo{{AgentName: "orch", AgentType: "claude", SessionID: "orch-seed2", DisplayName: "Orchestrator"}}, nil
		})

		// Pre-existing message 1
		_, err = bs.PostMessage(ctx, "proj", "Worker", "msg 1", nil)
		require.NoError(t, err)

		notifier.SeedFromDB(ctx)

		// Orchestrator reads message 1
		_, err = bs.ReadMessages(ctx, "proj", "Orchestrator", 50)
		require.NoError(t, err)

		// Fresh message 2 arrives before first notifier tick (count = 1)
		_, err = bs.PostMessage(ctx, "proj", "Worker", "msg 2", nil)
		require.NoError(t, err)

		// Pass: must notify because latestID > seeded latestID!
		require.NoError(t, notifier.RunOnce(ctx))
		require.Len(t, rt.sent, 1, "fresh same-count message arriving after startup must trigger notification")
		require.Contains(t, rt.sent[0].text, "1 unread message")
	})

	t.Run("3_StartupFollowedByNewArrivalWhileUnread_NotifiesImmediately", func(t *testing.T) {
		bs := testBoardStore(t)
		rt := &mockRuntime{}
		notifier := NewBoardNotifier(bs, rt, 10*time.Second)
		notifier.SetIsPausedFn(func(_ string) bool { return false })

		_, err := bs.Subscribe(ctx, "proj", "Orchestrator", "Orchestrator", "claude-orch-seed3", nil, nil, "all")
		require.NoError(t, err)

		notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
			return []AgentInfo{{AgentName: "orch", AgentType: "claude", SessionID: "orch-seed3", DisplayName: "Orchestrator"}}, nil
		})

		// Pre-existing message 1
		_, err = bs.PostMessage(ctx, "proj", "Worker", "msg 1", nil)
		require.NoError(t, err)

		notifier.SeedFromDB(ctx)

		// Message 1 is left UNREAD. Fresh message 2 arrives (count goes from 1 to 2)
		_, err = bs.PostMessage(ctx, "proj", "Worker", "msg 2", nil)
		require.NoError(t, err)

		require.NoError(t, notifier.RunOnce(ctx))
		require.Len(t, rt.sent, 1, "arrival while unread after startup must trigger nudge")
		require.Contains(t, rt.sent[0].text, "2 unread messages")
	})

	t.Run("4_EmptySeed_CleanStartupWithNoUnreads", func(t *testing.T) {
		bs := testBoardStore(t)
		rt := &mockRuntime{}
		notifier := NewBoardNotifier(bs, rt, 10*time.Second)
		notifier.SetIsPausedFn(func(_ string) bool { return false })

		_, err := bs.Subscribe(ctx, "proj", "Orchestrator", "Orchestrator", "claude-orch-empty", nil, nil, "all")
		require.NoError(t, err)

		notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
			return []AgentInfo{{AgentName: "orch", AgentType: "claude", SessionID: "orch-empty", DisplayName: "Orchestrator"}}, nil
		})

		// Seed with empty board
		notifier.SeedFromDB(ctx)

		// First message arrives
		_, err = bs.PostMessage(ctx, "proj", "Worker", "first message", nil)
		require.NoError(t, err)

		require.NoError(t, notifier.RunOnce(ctx))
		require.Len(t, rt.sent, 1, "first message on clean startup must notify immediately")
	})

	t.Run("5_InitializedIdentityAndCountConsistency", func(t *testing.T) {
		bs := testBoardStore(t)
		ctx := context.Background()

		_, err := bs.Subscribe(ctx, "proj", "Orchestrator", "Orchestrator", "claude-orch-cons", nil, nil, "all")
		require.NoError(t, err)
		_, err = bs.Subscribe(ctx, "proj", "Worker", "Worker", "claude-worker-cons", nil, nil, "mentions")
		require.NoError(t, err)

		// Post 3 messages: 1 general, 1 mentioning worker, 1 general
		_, err = bs.PostMessage(ctx, "proj", "System", "general 1", nil)
		require.NoError(t, err)
		msg2, err := bs.PostMessage(ctx, "proj", "System", "for @Worker", nil)
		require.NoError(t, err)
		msg3, err := bs.PostMessage(ctx, "proj", "System", "general 2", nil)
		require.NoError(t, err)

		states, err := bs.GetAllUnreadStates(ctx)
		require.NoError(t, err)

		// Orchestrator has receive_mode=all: sees all 3 messages
		orchState := states["claude-orch-cons"]
		require.Equal(t, 3, orchState.Count)
		require.Equal(t, msg3.ID, orchState.LatestID)

		// Worker has receive_mode=mentions: sees only msg2
		workerState := states["claude-worker-cons"]
		require.Equal(t, 1, workerState.Count)
		require.Equal(t, msg2.ID, workerState.LatestID)
	})

	t.Run("6_ConcurrentArrivalsAndPasses_RaceFree", func(t *testing.T) {
		bs := testBoardStore(t)
		rt := &mockRuntime{}
		notifier := NewBoardNotifier(bs, rt, 10*time.Second)
		notifier.SetIsPausedFn(func(_ string) bool { return false })

		_, err := bs.Subscribe(ctx, "proj", "Orchestrator", "Orchestrator", "claude-orch-race", nil, nil, "all")
		require.NoError(t, err)

		notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
			return []AgentInfo{{AgentName: "orch", AgentType: "claude", SessionID: "orch-race", DisplayName: "Orchestrator"}}, nil
		})

		var wg sync.WaitGroup
		// Concurrently post messages and run notifier passes
		for i := 0; i < 10; i++ {
			wg.Add(2)
			go func(idx int) {
				defer wg.Done()
				_, _ = bs.PostMessage(ctx, "proj", "Worker", fmt.Sprintf("race msg %d", idx), nil)
			}(i)
			go func() {
				defer wg.Done()
				_ = notifier.RunOnce(ctx)
			}()
		}
		wg.Wait()
		// Final pass to capture settled state
		require.NoError(t, notifier.RunOnce(ctx))
	})

	t.Run("7_DeliveryFailureRetry", func(t *testing.T) {
		bs := testBoardStore(t)
		frt := &faultyRuntime{}
		notifier := NewBoardNotifier(bs, frt, 10*time.Second)
		notifier.SetIsPausedFn(func(_ string) bool { return false })

		_, err := bs.Subscribe(ctx, "proj", "Orchestrator", "Orchestrator", "claude-orch-retry", nil, nil, "all")
		require.NoError(t, err)

		notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
			return []AgentInfo{{AgentName: "orch", AgentType: "claude", SessionID: "orch-retry", DisplayName: "Orchestrator"}}, nil
		})

		_, err = bs.PostMessage(ctx, "proj", "Worker", "urgent message", nil)
		require.NoError(t, err)

		// First pass fails delivery
		frt.failSend = true
		require.NoError(t, notifier.RunOnce(ctx))
		require.Len(t, frt.sent, 0, "failed delivery must not record successful send")

		// Second pass succeeds delivery
		frt.failSend = false
		require.NoError(t, notifier.RunOnce(ctx))
		require.Len(t, frt.sent, 1, "subsequent pass must retry and successfully deliver nudge")
	})

	t.Run("8_WorkerMentionCoalescingIntact", func(t *testing.T) {
		bs := testBoardStore(t)
		rt := &mockRuntime{}
		notifier := NewBoardNotifier(bs, rt, 10*time.Second)
		notifier.SetIsPausedFn(func(_ string) bool { return false })

		_, err := bs.Subscribe(ctx, "proj", "Worker", "Worker", "claude-worker-coal", nil, nil, "mentions")
		require.NoError(t, err)

		notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
			return []AgentInfo{{AgentName: "worker", AgentType: "claude", SessionID: "worker-coal", DisplayName: "Worker"}}, nil
		})

		// Unrelated message: no mention
		_, err = bs.PostMessage(ctx, "proj", "Lead", "general announcement", nil)
		require.NoError(t, err)
		require.NoError(t, notifier.RunOnce(ctx))
		require.Len(t, rt.sent, 0, "unrelated message must not nudge worker")

		// Mention arrives: nudges worker
		_, err = bs.PostMessage(ctx, "proj", "Lead", "hello @Worker please review", nil)
		require.NoError(t, err)
		require.NoError(t, notifier.RunOnce(ctx))
		require.Len(t, rt.sent, 1, "mention must trigger nudge")

		// Another non-mention arrives while unread: unread count for worker remains 1
		_, err = bs.PostMessage(ctx, "proj", "Lead", "another general message", nil)
		require.NoError(t, err)
		require.NoError(t, notifier.RunOnce(ctx))
		require.Len(t, rt.sent, 1, "worker mention batch must remain coalesced")
	})

	t.Run("9_PerSessionAndTeamIsolation", func(t *testing.T) {
		bs := testBoardStore(t)
		rt := &mockRuntime{}
		notifier := NewBoardNotifier(bs, rt, 10*time.Second)
		notifier.SetIsPausedFn(func(_ string) bool { return false })

		// Team A
		_, err := bs.Subscribe(ctx, "team-a", "Orchestrator", "Orchestrator", "claude-orch-a", nil, nil, "all")
		require.NoError(t, err)
		// Team B
		_, err = bs.Subscribe(ctx, "team-b", "Orchestrator", "Orchestrator", "claude-orch-b", nil, nil, "all")
		require.NoError(t, err)

		notifier.SetDiscoverFn(func(_ context.Context) ([]AgentInfo, error) {
			return []AgentInfo{
				{AgentName: "orchA", AgentType: "claude", SessionID: "orch-a", DisplayName: "Orchestrator"},
				{AgentName: "orchB", AgentType: "claude", SessionID: "orch-b", DisplayName: "Orchestrator"},
			}, nil
		})

		// Post only to team-a
		_, err = bs.PostMessage(ctx, "team-a", "Worker", "message for team A", nil)
		require.NoError(t, err)

		notifier.SeedFromDB(ctx)

		// Both start with seeded state. Team-a has count=1, Team-b has count=0.
		// Fresh message to Team-b:
		_, err = bs.PostMessage(ctx, "team-b", "Worker", "message for team B", nil)
		require.NoError(t, err)

		require.NoError(t, notifier.RunOnce(ctx))
		// Only team-b should be nudged; team-a was seeded and unchanged!
		require.Len(t, rt.sent, 1)
		require.Equal(t, "claude-orch-b", rt.sent[0].session)
	})
}

type faultyRuntime struct {
	mockRuntime
	failSend bool
}

func (f *faultyRuntime) SendInput(ctx context.Context, name, text string) error {
	if f.failSend {
		return fmt.Errorf("delivery failure")
	}
	return f.mockRuntime.SendInput(ctx, name, text)
}
