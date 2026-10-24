package routes

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/ptymanager"
)

type timeoutAgeLogs struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *timeoutAgeLogs) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *timeoutAgeLogs) text() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }
func captureTimeoutAgeLogs(t *testing.T) *timeoutAgeLogs {
	t.Helper()
	b := &timeoutAgeLogs{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(b, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return b
}
func setupTimeoutAgeWait(t *testing.T, h *BoardHandler, name, target string) time.Time {
	t.Helper()
	ctx := context.Background()
	_, err := h.bs.Subscribe(ctx, "team", "Worker", "Worker", name, nil, "mentions")
	require.NoError(t, err)
	w, err := h.bs.RegisterWait(ctx, "team", "Worker", name, "message", target, "", time.Nanosecond)
	require.NoError(t, err)
	expiry, err := time.Parse(time.RFC3339, w.ExpiresAt)
	require.NoError(t, err)
	return expiry
}

func TestOldWaitTimeoutIsDroppedOnceWithReceipt(t *testing.T) {
	_, h := setupBoardTestServer(t)
	ctx := context.Background()
	logs := captureTimeoutAgeLogs(t)
	const name = "codex-71460000-0000-4000-8000-000000000001"
	term := newMockTerminal()
	term.addSession(name, "/tmp/age-fixture")
	h.SetTerminal(term)
	expiry := setupTimeoutAgeWait(t, h, name, "Lead")
	clock := func() time.Time { return expiry.Add(15*time.Minute + time.Nanosecond) }
	for i := 0; i < 5; i++ {
		h.dispatchWaitTimeoutsWithClock(ctx, clock)
	}
	require.Empty(t, term.sentTo(name))
	require.Equal(t, 1, strings.Count(logs.text(), "dropped stale board wait timeout"))
	require.NoError(t, h.bs.DispatchExpiredWaitNotices(ctx, func(board.RegisteredWait) error { t.Error("old timeout has no receipt"); return nil }))
}

func TestWaitTimeoutInsideAgeWindowRetriesAndBoundaryIsAllowed(t *testing.T) {
	for _, age := range []time.Duration{time.Minute, 15 * time.Minute} {
		t.Run(age.String(), func(t *testing.T) {
			_, h := setupBoardTestServer(t)
			ctx := context.Background()
			logs := captureTimeoutAgeLogs(t)
			const name = "codex-71460000-0000-4000-8000-000000000002"
			term := &waitTimeoutFaultTerminal{mockSessionTerminal: newMockTerminal(), sendErr: errors.New("temporary transport failure")}
			term.addSession(name, "/tmp/age-retry")
			h.SetTerminal(term)
			expiry := setupTimeoutAgeWait(t, h, name, "Lead")
			clock := func() time.Time { return expiry.Add(age) }
			h.dispatchWaitTimeoutsWithClock(ctx, clock)
			require.Empty(t, term.sentTo(name))
			term.sendErr = nil
			h.dispatchWaitTimeoutsWithClock(ctx, clock)
			require.Len(t, term.sentTo(name), 1)
			h.dispatchWaitTimeoutsWithClock(ctx, clock)
			require.Len(t, term.sentTo(name), 1)
			require.NotContains(t, logs.text(), "dropped stale board wait timeout")
		})
	}
}

func TestHeldWaitTimeoutExpiresBeforePromptCloses(t *testing.T) {
	_, h := setupBoardTestServer(t)
	ctx := context.Background()
	logs := captureTimeoutAgeLogs(t)
	const name = "claude-71460000-0000-4000-8000-000000000003"
	mock := newMockTerminal()
	mock.addSession(name, "/tmp/age-held")
	term := &screenTerminal{fgSessionTerminal: newFGTerminal(mock), screen: permissionMenu}
	term.setFG(name, "node")
	h.SetTerminal(term)
	t.Cleanup(func() { ptymanager.DropHeld(name, "test complete") })
	expiry := setupTimeoutAgeWait(t, h, name, "Lead")
	var nanos atomic.Int64
	nanos.Store(expiry.Add(time.Minute).UnixNano())
	clock := func() time.Time { return time.Unix(0, nanos.Load()) }
	h.dispatchWaitTimeoutsWithClock(ctx, clock)
	require.Equal(t, 1, ptymanager.HeldCount(name))
	require.Empty(t, mock.sentTo(name))
	nanos.Store(expiry.Add(16 * time.Minute).UnixNano())
	term.show(idleInputBox)
	require.Eventually(t, func() bool { return ptymanager.HeldCount(name) == 0 }, 10*time.Second, 10*time.Millisecond)
	require.Empty(t, mock.sentTo(name))
	require.Equal(t, 1, strings.Count(logs.text(), "dropped stale board wait timeout"))
	h.dispatchWaitTimeoutsWithClock(ctx, clock)
	require.Equal(t, 1, strings.Count(logs.text(), "dropped stale board wait timeout"))
}

func TestWaitTimeoutCleansTargetNameBeforeTyping(t *testing.T) {
	_, h := setupBoardTestServer(t)
	ctx := context.Background()
	const name = "codex-71460000-0000-4000-8000-000000000004"
	term := newMockTerminal()
	term.addSession(name, "/tmp/clean-target")
	h.SetTerminal(term)
	setupTimeoutAgeWait(t, h, name, "Le\u200bad\u202e\u200d\ufe0f\u2800")
	h.dispatchWaitTimeouts(ctx)
	require.Len(t, term.sentTo(name), 1)
	require.Contains(t, term.sentTo(name)[0], "message from 'Lead'")
	require.NotContains(t, term.sentTo(name)[0], "\u200b")
	require.NotContains(t, term.sentTo(name)[0], "\u202e")
}
