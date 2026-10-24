package board

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWaitTimeoutPendingAfterPassiveExpiryAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "board.db")
	s, err := NewStoreWithKey(path, "")
	require.NoError(t, err)
	w, err := s.RegisterWait(ctx, "team", "Worker", "session", "message", "Lead", "", time.Minute)
	require.NoError(t, err)
	_, err = s.db.Exec("UPDATE board_waits SET expires_at=? WHERE id=?", time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), w.ID)
	require.NoError(t, err)
	active, err := s.GetActiveWait(ctx, "team", "Worker")
	require.NoError(t, err)
	require.Nil(t, active)
	require.NoError(t, s.Close())
	s, err = NewStoreWithKey(path, "")
	require.NoError(t, err)
	defer s.Close()
	attempts := 0
	require.Error(t, s.DispatchExpiredWaitNotices(ctx, func(RegisteredWait) error { attempts++; return errors.New("transport unavailable") }))
	var delivered []int64
	require.NoError(t, s.DispatchExpiredWaitNotices(ctx, func(w RegisteredWait) error { attempts++; delivered = append(delivered, w.ID); return nil }))
	require.NoError(t, s.DispatchExpiredWaitNotices(ctx, func(w RegisteredWait) error { delivered = append(delivered, w.ID); return nil }))
	require.Equal(t, 2, attempts)
	require.Equal(t, []int64{w.ID}, delivered)
	require.NoError(t, s.Close())
	s, err = NewStoreWithKey(path, "")
	require.NoError(t, err)
	defer s.Close()
	require.NoError(t, s.DispatchExpiredWaitNotices(ctx, func(w RegisteredWait) error { t.Error("already delivered timeout replayed after restart"); return nil }))
}

func TestWaitTimeoutCancelledReplacedAndResolvedStayQuiet(t *testing.T) {
	for _, action := range []string{"cancel", "replace", "resolve"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			s, err := NewStoreWithKey(filepath.Join(t.TempDir(), "board.db"), "")
			require.NoError(t, err)
			defer s.Close()
			w, err := s.RegisterWait(ctx, "team", "Worker", "session", "message", "Lead", "", time.Minute)
			require.NoError(t, err)
			if action == "resolve" {
				_, err = s.ResolveMatchingMessageWaits(ctx, "team", "Lead", "reply")
				require.NoError(t, err)
			}
			_, err = s.db.Exec("UPDATE board_waits SET expires_at=? WHERE id=?", time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), w.ID)
			require.NoError(t, err)
			_, err = s.ListActiveWaits(ctx, "team")
			require.NoError(t, err)
			switch action {
			case "cancel":
				require.NoError(t, s.CancelActiveWait(ctx, "team", "Worker"))
			case "replace":
				_, err = s.RegisterWait(ctx, "team", "Worker", "session", "task", "77", "", time.Hour)
				require.NoError(t, err)
			}
			require.NoError(t, s.DispatchExpiredWaitNotices(ctx, func(RegisteredWait) error {
				t.Error("cancelled/replaced/resolved wait delivered a timeout")
				return nil
			}))
		})
	}
}

func TestWaitTimeoutConcurrentPassesDeliverOnce(t *testing.T) {
	ctx := context.Background()
	s, err := NewStoreWithKey(filepath.Join(t.TempDir(), "board.db"), "")
	require.NoError(t, err)
	defer s.Close()
	w, err := s.RegisterWait(ctx, "team", "Worker", "session", "task", "42", "", time.Minute)
	require.NoError(t, err)
	_, err = s.db.Exec("UPDATE board_waits SET expires_at=? WHERE id=?", time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), w.ID)
	require.NoError(t, err)
	var n atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.DispatchExpiredWaitNotices(ctx, func(RegisteredWait) error { n.Add(1); return nil }); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), n.Load())
}

func TestWaitTimeoutLegacyMigrationKeepsActiveWaits(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "board.db")
	s, err := NewStoreWithKey(path, "")
	require.NoError(t, err)
	for _, name := range []string{"historical", "active"} {
		w, err := s.RegisterWait(ctx, "team", name, "session", "message", "Lead", "", time.Minute)
		require.NoError(t, err)
		_, err = s.db.Exec("UPDATE board_waits SET expires_at=? WHERE id=?", time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), w.ID)
		require.NoError(t, err)
	}
	_, err = s.GetActiveWait(ctx, "team", "historical")
	require.NoError(t, err)
	_, err = s.db.Exec("DROP INDEX idx_board_waits_timeout_pending; ALTER TABLE board_waits DROP COLUMN timeout_notified_at")
	require.NoError(t, err)
	require.NoError(t, s.Close())
	s, err = NewStoreWithKey(path, "")
	require.NoError(t, err)
	defer s.Close()
	var names []string
	require.NoError(t, s.DispatchExpiredWaitNotices(ctx, func(w RegisteredWait) error { names = append(names, w.SubscriberID); return nil }))
	require.Equal(t, []string{"active"}, names, "migration retires historical expired rows but keeps the active wait's future wake")
}

func TestWaitTimeoutAndResolutionCannotBothWin(t *testing.T) {
	ctx := context.Background()
	s, err := NewStoreWithKey(filepath.Join(t.TempDir(), "board.db"), "")
	require.NoError(t, err)
	defer s.Close()
	w, err := s.RegisterWait(ctx, "team", "Worker", "session", "task", "42", "", time.Minute)
	require.NoError(t, err)
	_, err = s.db.Exec("UPDATE board_waits SET expires_at=? WHERE id=?", time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), w.ID)
	require.NoError(t, err)
	require.NoError(t, s.DispatchExpiredWaitNotices(ctx, func(RegisteredWait) error { return nil }))
	require.False(t, s.resolveWait(ctx, w, nowUTC()), "a resolver with a stale active snapshot must not deliver a second wake")
}

func TestWaitTimeoutFailedBatchDoesNotStarveLaterWait(t *testing.T) {
	ctx := context.Background()
	s, err := NewStoreWithKey(filepath.Join(t.TempDir(), "board.db"), "")
	require.NoError(t, err)
	defer s.Close()
	var last int64
	for i := 0; i < 101; i++ {
		w, err := s.RegisterWait(ctx, "team", fmt.Sprintf("worker-%d", i), "session", "message", "Lead", "", time.Nanosecond)
		require.NoError(t, err)
		last = w.ID
	}
	delivered := 0
	deliver := func(w RegisteredWait) error {
		if w.ID != last {
			return errors.New("unavailable old terminal")
		}
		delivered++
		return nil
	}
	require.Error(t, s.DispatchExpiredWaitNotices(ctx, deliver))
	require.Zero(t, delivered)
	require.NoError(t, s.DispatchExpiredWaitNotices(ctx, deliver))
	require.Equal(t, 1, delivered)
	require.Error(t, s.DispatchExpiredWaitNotices(ctx, deliver), "the next pass wraps and retries the earlier failures")
	require.Equal(t, 1, delivered)
}
