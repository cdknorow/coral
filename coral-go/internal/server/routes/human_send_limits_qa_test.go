package routes

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// QA #7037: each of a gate's own limits is pinned alone: the other one is set far above what the test uses, so it cannot be what refuses.
func TestHumanSendGateEachOwnLimitAlone(t *testing.T) {
	ctx := context.Background()
	short := func() (context.Context, context.CancelFunc) { return context.WithTimeout(ctx, 500*time.Millisecond) }

	t.Run("MaxWaiting", func(t *testing.T) {
		var g humanSendGate
		g.set(func(tm *humanSendTimings) { tm.MaxWaiting, tm.MaxInFlight = 1, 1000 })
		leave, err := g.enter(ctx, "a")
		require.NoError(t, err)
		done := make(chan error, 1)
		go func() {
			l, err := g.enter(ctx, "a")
			done <- err
			if l != nil {
				defer l()
			}
		}()
		require.Eventually(t, func() bool {
			g.mu.Lock()
			defer g.mu.Unlock()
			return g.agents["a"] != nil && g.agents["a"].count == 2
		}, time.Second, time.Millisecond)
		c, cancel := short()
		defer cancel()
		_, err = g.enter(c, "a")
		assert.ErrorIs(t, err, errSendBusy, "this gate's MaxWaiting is 1 and MaxInFlight is far above: only MaxWaiting can refuse")
		leave()
		require.NoError(t, <-done)
	})

	t.Run("MaxInFlight", func(t *testing.T) {
		var g humanSendGate
		g.set(func(tm *humanSendTimings) { tm.MaxWaiting, tm.MaxInFlight = 1000, 2 })
		leaveA, err := g.enter(ctx, "a")
		require.NoError(t, err)
		leaveB, err := g.enter(ctx, "b")
		require.NoError(t, err)
		c, cancel := short()
		defer cancel()
		_, err = g.enter(c, "c")
		assert.ErrorIs(t, err, errSendBusy, "this gate's MaxInFlight is 2 and MaxWaiting is far above: only MaxInFlight can refuse")
		leaveA()
		leaveB()
	})
}
