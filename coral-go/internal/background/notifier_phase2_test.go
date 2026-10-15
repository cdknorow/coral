package background

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/ptymanager"
)

// #3578: with gate phase 2 on, the notifier's nudge for a receive-all agent mid-turn waits for
// the turn (routine chatter), and goes at once once an unread message tags the agent: the
// mention check is the board's own (HasUnreadMention, containsExplicitTag).
func TestBoardNotifier_Phase2RoutineWaitsMentionGoes(t *testing.T) {
	t.Setenv("CORAL_GATE_MODE", "")
	ptymanager.SetSignalsSettingFunc(func() bool { return true })
	t.Cleanup(func() { ptymanager.SetSignalsSettingFunc(nil) })
	ptymanager.SetStateFunc(func(context.Context, string, string, string) (ptymanager.AgentState, bool) {
		return ptymanager.AgentState{State: ptymanager.StateInTurn, Since: time.Now(), LastEvent: time.Now()}, true
	})
	t.Cleanup(func() { ptymanager.SetStateFunc(nil) })

	ctx := context.Background()
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	n := NewBoardNotifier(bs, rt, 10*time.Second)
	n.SetIsPausedFn(func(string) bool { return false })
	captureNotifierLogs(n)
	sid := "11111111-2222-3333-4444-000000003578"
	name := "claude-" + sid
	n.SetDiscoverFn(func(context.Context) ([]AgentInfo, error) {
		return []AgentInfo{{AgentType: "claude", AgentName: "orch", SessionID: sid, DisplayName: "Orch"}}, nil
	})
	t.Cleanup(func() { ptymanager.DropHeld(name, "test done") })
	_, err := bs.Subscribe(ctx, "team", "Orch", "Orchestrator", name, nil, nil, "all")
	require.NoError(t, err)
	sent := func() int { rt.mu.Lock(); defer rt.mu.Unlock(); return len(rt.sent) }

	_, err = bs.PostMessage(ctx, "team", "Lead", "general chatter", nil)
	require.NoError(t, err)
	require.NoError(t, n.RunOnce(ctx))
	assert.Equal(t, 0, sent(), "a routine nudge mid-turn waits")
	assert.Equal(t, 1, ptymanager.HeldCount(name))

	_, err = bs.PostMessage(ctx, "team", "Lead", "@Orch please look", nil)
	require.NoError(t, err)
	n.now = func() time.Time { return time.Now().Add(2 * defaultRemindAfter) }
	require.NoError(t, n.RunOnce(ctx))
	require.Eventually(t, func() bool { return sent() == 1 }, 5*time.Second, 20*time.Millisecond, "a mention's nudge goes mid-turn")
	assert.Equal(t, 0, ptymanager.HeldCount(name), "it replaced the routine one: one nudge, not two")
}

// The nudge's mention flag comes from the unread messages themselves (HasUnreadMention's confirmed
// tags) for every subscriber, whatever its receive mode (#3578, Backend's SHOULD 1 in Security's
// form). An Orchestrator in mentions mode isn't nudged for a routine message at all (CheckUnread
// counts its mentions only), and a tagged one goes at once mid-turn. A receive-all Orchestrator's
// routine nudge waits for the turn (TestBoardNotifier_Phase2RoutineWaitsMentionGoes).
func TestBoardNotifier_Phase2OrchestratorInMentionsMode(t *testing.T) {
	t.Setenv("CORAL_GATE_MODE", "")
	ptymanager.SetSignalsSettingFunc(func() bool { return true })
	t.Cleanup(func() { ptymanager.SetSignalsSettingFunc(nil) })
	ptymanager.SetStateFunc(func(context.Context, string, string, string) (ptymanager.AgentState, bool) {
		return ptymanager.AgentState{State: ptymanager.StateInTurn, Since: time.Now(), LastEvent: time.Now()}, true
	})
	t.Cleanup(func() { ptymanager.SetStateFunc(nil) })

	ctx := context.Background()
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	n := NewBoardNotifier(bs, rt, 10*time.Second)
	n.SetIsPausedFn(func(string) bool { return false })
	logs := captureNotifierLogs(n)
	sid := "11111111-2222-3333-4444-000000035781"
	name := "claude-" + sid
	n.SetDiscoverFn(func(context.Context) ([]AgentInfo, error) {
		return []AgentInfo{{AgentType: "claude", AgentName: "orchestrator", SessionID: sid, DisplayName: "Orchestrator"}}, nil
	})
	t.Cleanup(func() { ptymanager.DropHeld(name, "test done") })
	_, err := bs.Subscribe(ctx, "team", "Orchestrator", "Orchestrator", name, nil, nil, "mentions")
	require.NoError(t, err)
	sent := func() int { rt.mu.Lock(); defer rt.mu.Unlock(); return len(rt.sent) }

	_, err = bs.PostMessage(ctx, "team", "Lead", "routine progress note", nil)
	require.NoError(t, err)
	require.NoError(t, n.RunOnce(ctx))
	assert.Equal(t, 0, sent(), "no nudge for a routine message in mentions mode")
	assert.Equal(t, 0, ptymanager.HeldCount(name), "and nothing held")

	_, err = bs.PostMessage(ctx, "team", "Lead", "@Orchestrator please decide", nil)
	require.NoError(t, err)
	require.NoError(t, n.RunOnce(ctx))
	require.Eventually(t, func() bool { return sent() == 1 }, 5*time.Second, 20*time.Millisecond, "a tagged message's nudge goes mid-turn")
	assert.Equal(t, 0, ptymanager.HeldCount(name))
	assert.Contains(t, logs.String(), "mention=true")
}
