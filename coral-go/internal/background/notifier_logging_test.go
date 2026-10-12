package background

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func captureNotifierLogs(n *BoardNotifier) *bytes.Buffer {
	var buf bytes.Buffer
	n.logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return &buf
}

// A session that never joined a board is normal: no INFO line per pass.
func TestBoardNotifier_UnsubscribedSessionNotLoggedAtInfo(t *testing.T) {
	bs := testBoardStore(t)
	n := NewBoardNotifier(bs, &mockRuntime{}, 10*time.Second)
	n.SetDiscoverFn(func(context.Context) ([]AgentInfo, error) {
		return []AgentInfo{{AgentType: "claude", AgentName: "solo", SessionID: "11111111-2222-3333-4444-555555555555"}}, nil
	})
	logs := captureNotifierLogs(n)

	require.NoError(t, n.RunOnce(context.Background()))
	assert.NotContains(t, logs.String(), "no subscription found")
	assert.NotContains(t, logs.String(), "subscription lookup failed")
}

// A real lookup error still surfaces, at WARN, with the error.
func TestBoardNotifier_SubscriptionLookupErrorLoggedAtWarn(t *testing.T) {
	bs := testBoardStore(t)
	n := NewBoardNotifier(bs, &mockRuntime{}, 10*time.Second)
	n.SetDiscoverFn(func(context.Context) ([]AgentInfo, error) {
		return []AgentInfo{{AgentType: "claude", AgentName: "solo", SessionID: "11111111-2222-3333-4444-555555555555"}}, nil
	})
	logs := captureNotifierLogs(n)
	require.NoError(t, bs.Close())

	require.NoError(t, n.RunOnce(context.Background()))
	out := logs.String()
	assert.Contains(t, out, "level=WARN msg=\"subscription lookup failed\"")
	assert.Contains(t, out, "error=")
}

// Mentions-mode nudges with the incremental unread count: chatter and system
// messages never nudge, each new mention re-nudges with the current count, and
// deleting or reading messages is reflected on the next pass.
func TestBoardNotifier_MentionsNudgesTrackUnreadCount(t *testing.T) {
	ctx := context.Background()
	bs := testBoardStore(t)
	rt := &mockRuntime{}
	n := NewBoardNotifier(bs, rt, 10*time.Second)
	n.SetIsPausedFn(func(string) bool { return false })
	captureNotifierLogs(n)
	sid := "11111111-2222-3333-4444-555555555555"
	n.SetDiscoverFn(func(context.Context) ([]AgentInfo, error) {
		return []AgentInfo{{AgentType: "claude", AgentName: "dev", SessionID: sid, DisplayName: "Dev"}}, nil
	})
	_, err := bs.Subscribe(ctx, "team", "Dev", "Developer", "claude-"+sid, nil, nil, "mentions")
	require.NoError(t, err)
	post := func(from, text string) int64 {
		m, err := bs.PostMessage(ctx, "team", from, text, nil)
		require.NoError(t, err)
		return m.ID
	}
	lastNudge := func() string {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		if len(rt.sent) == 0 {
			return ""
		}
		return rt.sent[len(rt.sent)-1].text
	}
	pass := func() { require.NoError(t, n.RunOnce(ctx)) }

	post("Lead", "general chatter")
	post("Coral Task Queue", "@Dev task assigned")
	post("Dev", "@Dev talking to myself")
	pass()
	assert.Empty(t, rt.sent)

	first := post("Lead", "@Dev please look")
	pass()
	assert.Contains(t, lastNudge(), "You have 1 unread message on")

	post("Lead", "Dev: and this too")
	n.now = func() time.Time { return time.Now().Add(2 * defaultRemindAfter) }
	pass()
	assert.Contains(t, lastNudge(), "You have 2 unread messages")

	_, err = bs.DeleteMessage(ctx, first)
	require.NoError(t, err)
	n.now = func() time.Time { return time.Now().Add(4 * defaultRemindAfter) }
	pass()
	assert.Contains(t, lastNudge(), "You have 1 unread message on")

	_, err = bs.ReadMessages(ctx, "team", "Dev", 50)
	require.NoError(t, err)
	sent := len(rt.sent)
	n.now = func() time.Time { return time.Now().Add(6 * defaultRemindAfter) }
	pass()
	assert.Len(t, rt.sent, sent, "no nudge once everything is read")
}
