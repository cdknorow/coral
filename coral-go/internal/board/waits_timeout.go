package board

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// DispatchExpiredWaitNotices expires due waits and delivers each pending timeout
// notice. The durable receipt is independent of status: a passive getter may
// already have expired the row. Failed deliveries remain pending for the next
// pass (also after restart). Cancel/replacement serialize with this operation.
// deliver must not register/cancel waits; it may look up subscriptions.
func (s *Store) DispatchExpiredWaitNotices(ctx context.Context, deliver func(RegisteredWait) error) error {
	s.waitDispatchMu.Lock()
	defer s.waitDispatchMu.Unlock()
	now := nowUTC()
	result, err := s.db.ExecContext(ctx, "UPDATE board_waits SET status='expired', resolved_at=? WHERE status='active' AND expires_at<=?", now, now)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n > 0 {
		s.waitChanges.signal()
	}
	var waits []RegisteredWait
	const pending = `SELECT id,project,subscriber_id,session_name,wait_type,target_id,reason,status,created_at,resolved_at,expires_at
 FROM board_waits WHERE status='expired' AND timeout_notified_at IS NULL AND id>? ORDER BY id LIMIT 100`
	err = s.db.SelectContext(ctx, &waits, pending, s.waitTimeoutCursor)
	if err != nil {
		return err
	}
	if len(waits) == 0 && s.waitTimeoutCursor != 0 {
		s.waitTimeoutCursor = 0
		err = s.db.SelectContext(ctx, &waits, pending, s.waitTimeoutCursor)
		if err != nil {
			return err
		}
	}
	var failures []error
	for _, w := range waits {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		s.waitTimeoutCursor = w.ID
		if err := deliver(w); err != nil {
			failures = append(failures, fmt.Errorf("timeout notice %d: %w", w.ID, err))
			continue
		}
		// A server stop immediately after delivery must still persist its receipt.
		ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		_, err := s.db.ExecContext(ackCtx, "UPDATE board_waits SET timeout_notified_at=? WHERE id=? AND status='expired' AND timeout_notified_at IS NULL", nowUTC(), w.ID)
		cancel()
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
	}
	return errors.Join(failures...)
}
