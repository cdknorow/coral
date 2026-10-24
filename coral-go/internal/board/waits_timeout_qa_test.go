package board

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// QA #7118: while a timeout notice is being delivered (a terminal held at a prompt can hold it for the whole 5 s pass), registering or cancelling another agent's wait must not wait for it.
func TestRegisteringAnotherWaitDoesNotWaitForASlowTimeoutDelivery(t *testing.T) {
	ctx := context.Background()
	s, err := NewStoreWithKey(filepath.Join(t.TempDir(), "board.db"), "")
	require.NoError(t, err)
	defer s.Close()
	w, err := s.RegisterWait(ctx, "team", "Slow", "session-slow", "message", "Lead", "", time.Minute)
	require.NoError(t, err)
	_, err = s.db.Exec("UPDATE board_waits SET expires_at=? WHERE id=?", time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), w.ID)
	require.NoError(t, err)

	started := make(chan struct{})
	done := make(chan struct{})
	passCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond) // the real pass allows 5 s
	defer cancel()
	go func() {
		defer close(done)
		_ = s.DispatchExpiredWaitNotices(passCtx, func(RegisteredWait) error {
			close(started)
			<-passCtx.Done() // a delivery the gate holds until its context ends
			return passCtx.Err()
		})
	}()
	<-started
	begin := time.Now()
	_, err = s.RegisterWait(ctx, "team", "Other", "session-other", "message", "Lead", "", time.Minute)
	took := time.Since(begin)
	require.NoError(t, err)
	assert.Less(t, took, 500*time.Millisecond, "another agent's RegisterWait waited %s for a slow delivery", took)
	<-done
}
