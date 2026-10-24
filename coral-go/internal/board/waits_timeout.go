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
// pass (also after restart). Claims serialize concurrent passes on this Store;
// delivery runs without the mutation lock so unrelated waits remain responsive.
// A queued delivery must recheck WaitTimeoutCurrent immediately before sending.
func (s *Store) DispatchExpiredWaitNotices(ctx context.Context, deliver func(RegisteredWait) error) error {
	waits, err := s.claimExpiredWaitNotices(ctx)
	if err != nil {
		return err
	}
	defer func() {
		s.waitDispatchMu.Lock()
		defer s.waitDispatchMu.Unlock()
		for _, w := range waits {
			delete(s.waitTimeoutClaims, w.ID)
		}
	}()
	var failures []error
	for _, w := range waits {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		current, err := s.WaitTimeoutCurrent(ctx, w.ID)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if !current {
			continue
		}
		if err := deliver(w); err != nil {
			failures = append(failures, fmt.Errorf("timeout notice %d: %w", w.ID, err))
			continue
		}
		// A server stop immediately after acceptance must still persist its receipt.
		ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		_, err = s.db.ExecContext(ackCtx, "UPDATE board_waits SET timeout_notified_at=? WHERE id=? AND status='expired' AND timeout_notified_at IS NULL", nowUTC(), w.ID)
		cancel()
		if err != nil {
			return errors.Join(append(failures, err)...)
		}
	}
	return errors.Join(failures...)
}

// WaitTimeoutCurrent also checks an already accepted, held notice: its receipt
// prevents re-enqueueing, but cancellation/replacement must still suppress typing.
func (s *Store) WaitTimeoutCurrent(ctx context.Context, id int64) (bool, error) {
	var current bool
	err := s.db.GetContext(ctx, &current, `SELECT EXISTS(SELECT 1 FROM board_waits w
 WHERE w.id=? AND w.status='expired' AND NOT EXISTS(SELECT 1 FROM board_waits newer
 WHERE newer.project=w.project AND newer.subscriber_id=w.subscriber_id AND newer.id>w.id))`, id)
	return current, err
}

func (s *Store) claimExpiredWaitNotices(ctx context.Context) ([]RegisteredWait, error) {
	s.waitDispatchMu.Lock()
	defer s.waitDispatchMu.Unlock()
	now := nowUTC()
	result, err := s.db.ExecContext(ctx, "UPDATE board_waits SET status='expired', resolved_at=? WHERE status='active' AND expires_at<=?", now, now)
	if err != nil {
		return nil, err
	}
	if n, _ := result.RowsAffected(); n > 0 {
		s.waitChanges.signal()
	}
	var waits []RegisteredWait
	const pending = `SELECT id,project,subscriber_id,session_name,wait_type,target_id,reason,status,created_at,resolved_at,expires_at
 FROM board_waits WHERE status='expired' AND timeout_notified_at IS NULL AND id>? ORDER BY id LIMIT 100`
	err = s.db.SelectContext(ctx, &waits, pending, s.waitTimeoutCursor)
	if err != nil {
		return nil, err
	}
	if len(waits) == 0 && s.waitTimeoutCursor != 0 {
		s.waitTimeoutCursor = 0
		err = s.db.SelectContext(ctx, &waits, pending, s.waitTimeoutCursor)
		if err != nil {
			return nil, err
		}
	}
	if s.waitTimeoutClaims == nil {
		s.waitTimeoutClaims = make(map[int64]bool)
	}
	claimed := make([]RegisteredWait, 0, len(waits))
	for _, w := range waits {
		s.waitTimeoutCursor = w.ID
		if !s.waitTimeoutClaims[w.ID] {
			s.waitTimeoutClaims[w.ID] = true
			claimed = append(claimed, w)
		}
	}
	return claimed, nil
}
