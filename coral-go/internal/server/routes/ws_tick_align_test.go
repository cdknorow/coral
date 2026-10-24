package routes

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUntilTickIsTheNextBoundary(t *testing.T) {
	iv := 5 * time.Second
	base := time.Unix(1_700_000_000, 0) // a multiple of 5 s
	assert.Equal(t, 4990*time.Millisecond, untilTick(base.Add(10*time.Millisecond), iv))
	assert.Equal(t, 2*time.Second, untilTick(base.Add(3*time.Second), iv))
	assert.Equal(t, iv+20*time.Millisecond, untilTick(base.Add(iv-20*time.Millisecond), iv), "too close to tick twice in a row")
}

// #3846 (#3833 item 6): connections tick on the same boundaries, so tabs that connected at
// different moments share one build of the session list per interval instead of one each.
func TestWSCoral_ConnectionsShareTicks(t *testing.T) {
	var builds atomic.Int64
	// A short shared-build age and an interval over it: the same sharing is checked in 2 s instead of 9 (#4914). The server's own age is
	// 750 ms; the property (one build per tick for any number of connections, ticks on the wall clock) does not depend on its value.
	defaultAge := wsListMaxAgeNs.Swap(int64(150 * time.Millisecond))
	t.Cleanup(func() { wsListMaxAgeNs.Store(defaultAge) })
	server, handler := setupTestServer(t)
	handler.wsBuildHook = func() { builds.Add(1) }
	const interval = 400 * time.Millisecond // over wsListMaxAge, so each tick builds once
	handler.cfg.WSPollInterval = interval
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dial := func() {
		conn, _, err := websocket.Dial(ctx, "ws"+server.URL[4:]+"/ws/coral", nil)
		require.NoError(t, err)
		t.Cleanup(func() { conn.CloseNow() })
		go func() { // drain
			for {
				if _, _, err := conn.Read(ctx); err != nil {
					return
				}
			}
		}()
	}
	dial()
	time.Sleep(interval / 2) // the second tab connects out of phase, past wsListMaxAge
	dial()
	time.Sleep(interval) // both first messages built
	before := builds.Load()
	const intervals = 3
	time.Sleep(intervals * interval)
	got := builds.Load() - before
	assert.LessOrEqual(t, got, int64(intervals+1), "one build per interval for both connections, not one each")
	assert.GreaterOrEqual(t, got, int64(intervals-1), "the list is still built every interval")
}

// clearOfTick returns once the next wall-clock tick (#3846) is at least need away, waiting
// until just after it when it is closer. Ticks fall on multiples of the poll interval, not
// a whole interval after a test connects, so a test that counts on no tick for a while
// (a remote burst coalescing into one diff, a kick's build count) must place itself (#3932).
func clearOfTick(interval, need time.Duration) {
	if d := untilTick(time.Now(), interval); d < need {
		time.Sleep(d + 50*time.Millisecond)
	}
}
