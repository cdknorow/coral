package stallwatch

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3444: Stats reports the slowest probe's last latency (a probe still running counts with its
// elapsed time), the p95 of recent latencies, and the last time a probe took SlowThreshold or
// more, for the hub's machine health.
func TestStatsReportsResponsiveness(t *testing.T) {
	c := newClock()
	release := make(chan struct{})
	block := false
	w := New(Config{Dir: t.TempDir(), now: c.now, Probes: []Probe{
		{Name: "fast", Check: func(context.Context) error { c.add(5 * time.Millisecond); return nil }},
		{Name: "gated", Check: func(context.Context) error {
			if block {
				<-release
			}
			return nil
		}},
	}})
	ctx := context.Background()

	st := w.Stats()
	assert.Zero(t, st.Last, "nothing measured yet")
	assert.Zero(t, st.P95)
	assert.True(t, st.LastSlow.IsZero())

	w.Tick(ctx)
	require.Eventually(t, func() bool { return len(w.Stats().snapshotLatencies(w)) == 2 }, 2*time.Second, 5*time.Millisecond)
	st = w.Stats()
	assert.Less(t, st.Last, SlowThreshold)
	assert.True(t, st.LastSlow.IsZero(), "nothing slow yet")

	// The gated probe hangs: once it has run SlowThreshold, Stats shows it at once.
	block = true
	w.Tick(ctx)
	c.add(SlowThreshold + 500*time.Millisecond)
	st = w.Stats()
	assert.GreaterOrEqual(t, st.Last, SlowThreshold, "a probe still running counts with its elapsed time")
	assert.False(t, st.LastSlow.IsZero(), "and is slow now")
	slowAt := st.LastSlow
	close(release)
	require.Eventually(t, func() bool { w.mu.Lock(); defer w.mu.Unlock(); return !w.state["gated"].running }, 2*time.Second, 5*time.Millisecond)
	st = w.Stats()
	assert.GreaterOrEqual(t, st.P95, SlowThreshold, "the slow run is in the p95")
	assert.False(t, st.LastSlow.Before(slowAt), "the last slow time stays")
}

// snapshotLatencies reads the latency window (tests only).
func (Stats) snapshotLatencies(w *Watchdog) []time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Duration(nil), w.latencies...)
}
