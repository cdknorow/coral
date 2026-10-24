package routes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/subgentic/coral/internal/board"
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

func (h *BoardHandler) dispatchWaitTimeouts(ctx context.Context) {
	if h.bs == nil || h.terminal == nil {
		return
	}
	passCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := h.bs.DispatchExpiredWaitNotices(passCtx, func(w board.RegisteredWait) error {
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
		text := fmt.Sprintf("[Wait timed out] Registered wait #%d on '%s' for %s expired. Review your task and choose the next step.", w.ID, w.Project, w.TargetLabel())
		err = deliverAutomatedWhile(passCtx, h.terminal, w.SessionName, agentType, sessionID, fmt.Sprintf("board-wait-timeout:%d", w.ID), board.OneLine(text), "board wait timeout", func(ctx context.Context) error {
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
