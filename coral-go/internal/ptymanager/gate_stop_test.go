package ptymanager

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var stopGateRuns atomic.Int64

// The gate's retry loop sleeps between tries; StopGate must wake it and wait for it to end, well
// inside the sleep, and leave what is held held (#4167: a short test run ended before the 2 s sleep
// did, and the leak check named the loop).
func TestStopGateEndsTheSleepingLoopAndKeepsWhatIsHeld(t *testing.T) {
	// Its own session name each run: DropHeld (the cleanup) raises the session's generation, and a
	// later run's sends under the same name would be stale and never held (-count=N).
	name := fmt.Sprintf("stopgate-test-session-%d", stopGateRuns.Add(1))
	gate.mu.Lock()
	oldEvery := gate.timings.every
	gate.timings.every = 2 * time.Second // long enough that a retry can't happen during the test
	gate.mu.Unlock()
	t.Cleanup(func() {
		DropHeld(name, "test over")
		StopGate(5 * time.Second)
		gate.mu.Lock()
		gate.timings.every = oldEvery
		gate.mu.Unlock()
	})
	running := func() bool {
		gate.mu.Lock()
		defer gate.mu.Unlock()
		return gate.running
	}

	// Nothing running: it returns at once.
	start := time.Now()
	require.True(t, StopGate(time.Second))
	assert.Less(t, time.Since(start), 500*time.Millisecond)

	// A held send starts the loop, which then sleeps (the stop must find it asleep, or it proves nothing).
	asleepBefore := gate.sleeps.Load()
	gate.hold(AutomatedInput{Name: name, What: "stop test", Key: "first", Text: "first"}, "a busy footer", "")
	require.True(t, running(), "a hold starts the retry loop")
	require.Eventually(t, func() bool { return gate.sleeps.Load() > asleepBefore }, 2*time.Second, 5*time.Millisecond, "the loop went to sleep")
	start = time.Now()
	require.True(t, StopGate(5*time.Second), "the loop ended")
	assert.Less(t, time.Since(start), time.Second, "it woke the sleeping loop instead of waiting out its 2 s sleep")
	assert.False(t, running(), "the loop is not running")
	assert.Equal(t, 1, HeldCount(name), "what was held stays held")

	// A later hold starts the loop again, and a second stop ends that one too.
	asleepBefore = gate.sleeps.Load()
	gate.hold(AutomatedInput{Name: name, What: "stop test", Key: "second", Text: "second"}, "a busy footer", "")
	require.True(t, running(), "a hold after a stop starts a new loop")
	require.Eventually(t, func() bool { return gate.sleeps.Load() > asleepBefore }, 2*time.Second, 5*time.Millisecond, "the new loop went to sleep")
	require.True(t, StopGate(5*time.Second))
	assert.False(t, running())
	assert.Equal(t, 2, HeldCount(name))
}
