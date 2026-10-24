package routes

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/internal/board"
)

func TestWaitTimeoutWorkerWakesOnceAndStops(t *testing.T) {
	_, h := setupBoardTestServer(t)
	ctx := context.Background()
	const name = "codex-00000002-3864-4864-8864-000000000002"
	term := newMockTerminal()
	term.addSession(name, "/tmp/timeout-fixture")
	h.SetTerminal(term)
	_, err := h.bs.Subscribe(ctx, "team", "Worker", "Worker", name, nil, "mentions")
	require.NoError(t, err)
	// A nanosecond wait is stored at whole-second precision, so it is already due.
	_, err = h.bs.RegisterWait(ctx, "team", "Worker", name, "task", "42", "", time.Nanosecond)
	require.NoError(t, err)
	_, err = h.bs.GetActiveWait(ctx, "team", "Worker")
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); h.RunWaitTimeouts(runCtx) }()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool { return len(term.sentTo(name)) == 1 }, 2*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timeout worker did not stop")
	}
	h.dispatchWaitTimeouts(ctx)
	require.Len(t, term.sentTo(name), 1)
	require.Contains(t, term.sentTo(name)[0], "[Wait timed out]")
	require.Contains(t, term.sentTo(name)[0], "task #42")
}

func TestWaitTimeoutDoesNotFollowAReboundSubscriber(t *testing.T) {
	_, h := setupBoardTestServer(t)
	ctx := context.Background()
	const old = "claude-00000002-3864-4864-8864-000000000002"
	const replacement = "claude-00000003-3864-4864-8864-000000000003"
	term := newMockTerminal()
	term.addSession(old, "/tmp/old")
	term.addSession(replacement, "/tmp/new")
	h.SetTerminal(term)
	_, err := h.bs.Subscribe(ctx, "team", "Worker", "Worker", old, nil, "mentions")
	require.NoError(t, err)
	_, err = h.bs.RegisterWait(ctx, "team", "Worker", old, "message", "Lead", "", time.Nanosecond)
	require.NoError(t, err)
	_, err = h.bs.Subscribe(ctx, "team", "Worker", "Worker", replacement, nil, "mentions")
	require.NoError(t, err)
	h.dispatchWaitTimeouts(ctx)
	require.Empty(t, term.sentTo(old))
	require.Empty(t, term.sentTo(replacement))
	// The obsolete timeout was retired, not saved for a future role-name match.
	_, err = h.bs.Subscribe(ctx, "team", "Worker", "Worker", old, nil, "mentions")
	require.NoError(t, err)
	require.NoError(t, h.bs.DispatchExpiredWaitNotices(ctx, func(board.RegisteredWait) error { t.Error("retired timeout replayed"); return nil }))
}

func TestNextTaskNudgeDoesNotInterruptBusyWorker(t *testing.T) {
	_, h := setupBoardTestServer(t)
	ctx := context.Background()
	const session = "claude-00000002-3864-4864-8864-000000000002"
	term := newMockTerminal()
	term.addSession(session, "/tmp/busy")
	h.SetTerminal(term)
	_, err := h.bs.Subscribe(ctx, "team", "Worker", "Worker", session, nil, "mentions")
	require.NoError(t, err)
	active, err := h.bs.CreateTask(ctx, "team", "Active", "", "medium", "Operator", "Worker")
	require.NoError(t, err)
	_, err = h.bs.ClaimTask(ctx, "team", "Worker", active.ID)
	require.NoError(t, err)
	_, err = h.bs.CreateTask(ctx, "team", "Deferred", "", "medium", "Operator", "Worker")
	require.NoError(t, err)
	h.nudgeNextTask(ctx, "team", "Worker")
	require.Empty(t, term.sentTo(session))
}
