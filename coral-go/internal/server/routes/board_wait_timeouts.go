package routes

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/subgentic/coral/internal/board"
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
			return err
		}
		text := fmt.Sprintf("[Wait timed out] Registered wait #%d on '%s' for %s expired. Review your task and choose the next step.", w.ID, w.Project, w.TargetLabel())
		return deliverAutomated(passCtx, h.terminal, w.SessionName, agentType, sessionID, fmt.Sprintf("board-wait-timeout:%d", w.ID), board.OneLine(text), "board wait timeout")
	})
	if err != nil && ctx.Err() == nil {
		slog.Warn("dispatch board wait timeouts", "error", err)
	}
}
