package routes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/clitext"
	"github.com/subgentic/coral/internal/ptymanager"
)

// RunWaitTimeouts belongs to the server's cancellable background lifecycle.
// An immediate pass recovers pending notices, including waits expired by readers.
func (h *BoardHandler) RunWaitTimeouts(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		h.dispatchWaitTimeouts(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

const waitTimeoutNoticeMaxAge = 15 * time.Minute

func (h *BoardHandler) dispatchWaitTimeouts(ctx context.Context) {
	h.dispatchWaitTimeoutsWithClock(ctx, time.Now)
}

// The clock is per pass (and its held sends), so tests can advance a notice's
// age without changing the system clock or the process-wide gate timings.
func (h *BoardHandler) dispatchWaitTimeoutsWithClock(ctx context.Context, clock func() time.Time) {
	if h.bs == nil || h.terminal == nil {
		return
	}
	passCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := h.bs.DispatchExpiredWaitNotices(passCtx, func(w board.RegisteredWait) error {
		expires, err := time.Parse(time.RFC3339, w.ExpiresAt)
		if err != nil {
			return fmt.Errorf("wait %d expiry: %w", w.ID, err)
		}
		var ageLogged atomic.Bool
		stale := func() bool {
			if !clock().After(expires.Add(waitTimeoutNoticeMaxAge)) {
				return false
			}
			if ageLogged.CompareAndSwap(false, true) {
				slog.Warn("dropped stale board wait timeout", "wait_id", w.ID, "project", w.Project)
			}
			return true
		}
		// Success here writes the durable receipt without consulting an agent
		// that may be unavailable. An already held send checks the age again.
		if stale() {
			return nil
		}
		sub, err := h.bs.GetProjectSubscription(passCtx, w.Project, w.SubscriberID)
		if err != nil {
			return err
		}
		// Never send an old wait to a replacement agent with the same role.
		if sub == nil || sub.SessionName == "" || sub.SessionName != w.SessionName {
			return nil // retired: no longer the subscriber/session that registered it
		}
		if h.isArchivedSession != nil && h.isArchivedSession(passCtx, w.SessionName) {
			return nil // archived agents must never receive a late wake
		}
		agentType, sessionID, err := h.noticeTarget(passCtx, w.Project, w.SessionName)
		if err != nil {
			if errors.Is(err, errNoticeTarget) {
				return nil
			} // retired: the original live identity is gone
			return err
		}
		w.TargetID = clitext.CleanName(w.TargetID)
		text := fmt.Sprintf("[Wait timed out] Registered wait #%d on '%s' for %s expired. Review your task and choose the next step.", w.ID, w.Project, w.TargetLabel())
		err = deliverAutomatedWhile(passCtx, h.terminal, w.SessionName, agentType, sessionID, fmt.Sprintf("board-wait-timeout:%d", w.ID), board.OneLine(text), "board wait timeout", func(ctx context.Context) error {
			if stale() {
				return ptymanager.ErrNotWanted
			}
			current, err := h.bs.WaitTimeoutCurrent(ctx, w.ID)
			if err != nil {
				return err
			}
			if !current {
				return ptymanager.ErrNotWanted
			}
			sub, err := h.bs.GetProjectSubscription(ctx, w.Project, w.SubscriberID)
			if err != nil {
				return err
			}
			if sub == nil || sub.SessionName != w.SessionName || (h.isArchivedSession != nil && h.isArchivedSession(ctx, w.SessionName)) {
				return ptymanager.ErrNotWanted
			}
			return nil
		})
		// Permanent refusals retire this wait; retrying them each second can
		// neither wake the old agent nor safely type into its shell.
		if errors.Is(err, ptymanager.ErrNotWanted) || errors.Is(err, errAgentExited) || errors.Is(err, ptymanager.ErrTerminalGone) {
			return nil
		}
		return err
	})
	if err != nil && ctx.Err() == nil {
		slog.Warn("dispatch board wait timeouts", "error", err)
	}
}
