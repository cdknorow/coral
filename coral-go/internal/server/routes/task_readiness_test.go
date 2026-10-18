package routes

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4602 L5: updates of one task never run its readiness check more than once at a time; ones that arrive meanwhile are ONE more run
// (so an update that lands mid-check is still seen); another task's check is independent; and the bookkeeping empties.
func TestTaskReadinessRunsOncePerTaskAtATime(t *testing.T) {
	h := &SessionsHandler{}
	var active, peak, total atomic.Int32
	release := make(chan struct{})
	started := make(chan int64, 100)
	h.readiness.run = func(_ context.Context, id int64) {
		n := active.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		total.Add(1)
		started <- id
		<-release
		active.Add(-1)
	}
	for i := 0; i < 50; i++ {
		h.scheduleTaskReadiness(7) // 50 updates of one task
	}
	assert.EqualValues(t, 7, <-started)
	h.scheduleTaskReadiness(8) // another task is its own check
	assert.EqualValues(t, 8, <-started)
	assert.EqualValues(t, 2, active.Load(), "one running check for task 7 and one for task 8, not 51")
	close(release)

	// The 50 updates became one more run of task 7 (the second <-started below), not 49.
	require.EqualValues(t, 7, <-started, "an update that came during the check is run once more")
	require.Eventually(t, func() bool {
		h.readiness.mu.Lock()
		defer h.readiness.mu.Unlock()
		return len(h.readiness.running) == 0
	}, 2*time.Second, 5*time.Millisecond, "the bookkeeping empties when the checks end")
	assert.EqualValues(t, 3, total.Load(), "task 7 twice (first, and once more for all the rest), task 8 once")
	assert.EqualValues(t, 2, peak.Load())

	// An update after a check finished starts a new one.
	h.readiness.run = func(_ context.Context, id int64) { total.Add(1) }
	h.scheduleTaskReadiness(7)
	require.Eventually(t, func() bool { return total.Load() == 4 }, 2*time.Second, 5*time.Millisecond)
}

// A panic in the check is recovered and does not leave the task marked running for ever.
func TestTaskReadinessSurvivesAPanicingCheck(t *testing.T) {
	h := &SessionsHandler{}
	var calls atomic.Int32
	var mu sync.Mutex
	h.readiness.run = func(_ context.Context, id int64) {
		mu.Lock()
		calls.Add(1)
		mu.Unlock()
		panic("boom")
	}
	h.scheduleTaskReadiness(1)
	require.Eventually(t, func() bool { return calls.Load() == 1 }, 2*time.Second, 5*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	h.readiness.mu.Lock()
	stuck := len(h.readiness.running)
	h.readiness.mu.Unlock()
	require.Zero(t, stuck, "the task is not left marked as running")
	h.scheduleTaskReadiness(1)
	require.Eventually(t, func() bool { return calls.Load() == 2 }, 2*time.Second, 5*time.Millisecond, "the next update runs again")
}
