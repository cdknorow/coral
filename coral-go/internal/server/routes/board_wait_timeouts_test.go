package routes

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"fmt"
	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/ptymanager"
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

func TestHeldWaitTimeoutRechecksItsWaitBeforeTyping(t *testing.T) {
	for i, action := range []string{"cancel", "replace", "rebind", "archive", "deliver"} {
		t.Run(action, func(t *testing.T) {
			_, h := setupBoardTestServer(t)
			ctx := context.Background()
			name := fmt.Sprintf("claude-71050000-0000-4000-8000-%012d", i+1)
			mock := newMockTerminal()
			mock.addSession(name, "/tmp/timeout-held")
			term := &screenTerminal{fgSessionTerminal: newFGTerminal(mock), screen: permissionMenu}
			term.setFG(name, "node")
			h.SetTerminal(term)
			var archived atomic.Bool
			h.isArchivedSession = func(context.Context, string) bool { return archived.Load() }
			t.Cleanup(func() { ptymanager.DropHeld(name, "test done") })
			_, err := h.bs.Subscribe(ctx, "team", "Worker", "Worker", name, nil, "mentions")
			require.NoError(t, err)
			_, err = h.bs.RegisterWait(ctx, "team", "Worker", name, "message", "Lead", "", time.Nanosecond)
			require.NoError(t, err)
			h.dispatchWaitTimeouts(ctx)
			require.Equal(t, 1, ptymanager.HeldCount(name))
			require.Empty(t, mock.sentTo(name))
			switch action {
			case "cancel":
				require.NoError(t, h.bs.CancelActiveWait(ctx, "team", "Worker"))
			case "replace":
				_, err = h.bs.RegisterWait(ctx, "team", "Worker", name, "task", "77", "", time.Hour)
				require.NoError(t, err)
			case "rebind":
				_, err = h.bs.Subscribe(ctx, "team", "Worker", "Worker", "claude-71050000-0000-4000-8000-999999999999", nil, "mentions")
				require.NoError(t, err)
			case "archive":
				archived.Store(true)
			}
			term.show(idleInputBox)
			require.Eventually(t, func() bool { return ptymanager.HeldCount(name) == 0 }, 10*time.Second, 10*time.Millisecond)
			if action == "deliver" {
				require.Len(t, mock.sentTo(name), 1)
			} else {
				require.Empty(t, mock.sentTo(name))
			}
			h.dispatchWaitTimeouts(ctx)
			if action == "deliver" {
				require.Len(t, mock.sentTo(name), 1)
			} else {
				require.Empty(t, mock.sentTo(name))
			}
		})
	}
}
