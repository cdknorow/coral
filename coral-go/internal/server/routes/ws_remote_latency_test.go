package routes

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/background"
)

// remoteSnap is one remote with one agent whose status carries a marker.
func remoteSnap(marker string) background.RemoteSnapshot {
	return background.RemoteSnapshot{
		Servers:  []background.RemoteServerStatus{{ID: "a", Label: "A", Status: "online"}},
		Sessions: []map[string]any{{"name": "agent", "session_id": "ra-1", "server": "a", "status": marker}},
	}
}

// dialFeed opens /ws/coral and reads the first coral_update.
func dialFeed(t *testing.T, ctx context.Context, url string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, "ws"+url[4:]+"/ws/coral", nil)
	require.NoError(t, err)
	t.Cleanup(func() { conn.CloseNow() })
	var first struct {
		Type string `json:"type"`
	}
	require.NoError(t, wsjson.Read(ctx, conn, &first))
	require.Equal(t, "coral_update", first.Type)
	return conn
}

// awaitStatus reads conn until a coral_diff carries the remote agent with
// status want, and returns how long that took.
func awaitStatus(t *testing.T, ctx context.Context, conn *websocket.Conn, want string, within time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	rctx, stop := context.WithTimeout(ctx, within)
	defer stop()
	for {
		var msg struct {
			Changed []map[string]any `json:"changed"`
		}
		require.NoError(t, wsjson.Read(rctx, conn, &msg), "no coral_diff with status %q within %s", want, within)
		for _, e := range msg.Changed {
			if e["session_id"] == "ra-1" && e["status"] == want {
				return time.Since(start)
			}
		}
	}
}

// A remote change reaches a dashboard at once even while local state changes
// (kicks) keep rebuilding the feed: kick builds do not count against the
// one-a-second limit on remote-triggered builds (#3142). Before, a remote
// change landing within a second of any build waited for the next one.
func TestWSCoral_RemoteChangesDoNotWaitBehindKicks(t *testing.T) {
	server, h := setupTestServer(t)
	h.cfg.WSPollInterval = 10 * time.Second // the ticker cannot deliver it
	// The remote limit, shortened from a second so each sample doesn't wait a second (#3566 timing).
	h.cfg.WSRemoteDebounce = 250 * time.Millisecond
	src := &fakeSource{}
	h.SetRemoteAgents(src)
	src.set(remoteSnap("m-0"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := dialFeed(t, ctx, server.URL)

	// A local state change every 200 ms, the whole time.
	stopKicks := make(chan struct{})
	defer close(stopKicks)
	go func() {
		tk := time.NewTicker(200 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stopKicks:
				return
			case <-tk.C:
				h.kickWS()
			}
		}
	}()
	time.Sleep(300 * time.Millisecond) // kicks are flowing

	// Remote changes slower than the remote limit, at offsets
	// that drift against the kicks.
	var lat []time.Duration
	for i := 1; i <= 8; i++ {
		time.Sleep(h.cfg.WSRemoteDebounce + 100*time.Millisecond + time.Duration(i*37)*time.Millisecond)
		marker := fmt.Sprintf("m-%d", i)
		src.set(remoteSnap(marker))
		lat = append(lat, awaitStatus(t, ctx, conn, marker, 2*time.Second))
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	t.Logf("remote change -> dashboard latencies: %v", lat)
	// Most go out at once; one that lands while a kick is coalescing rides that
	// kick's build, so none waits longer than the kick window. Before #3142 a
	// change waited for the next kick build (median ~80-110 ms, max ~190 ms).
	// The bounds are for a quiet machine; a loaded one delays every wake-up by the scheduler's jitter, so they grow by it.
	slack := 2 * schedulerJitter()
	assert.Less(t, lat[len(lat)/2], 10*time.Millisecond+slack, "median remote change -> dashboard with kicks every 200ms (slack %v): %v", slack, lat)
	assert.Less(t, lat[len(lat)-1], wsKickCoalesce+30*time.Millisecond+slack, "no remote change waits beyond the kick window (slack %v): %v", slack, lat)
}

// schedulerJitter is the worst lateness of 20 one-millisecond sleeps: ~0 on a quiet machine, tens of ms when loaded.
func schedulerJitter() time.Duration {
	var worst time.Duration
	for range 20 {
		start := time.Now()
		time.Sleep(time.Millisecond)
		if late := time.Since(start) - time.Millisecond; late > worst {
			worst = late
		}
	}
	return worst
}

// A burst of remote changes still costs at most one build a second: the
// first goes out at once, the rest arrive together in one later diff.
func TestWSCoral_RemoteBurstIsStillCoalesced(t *testing.T) {
	server, h := setupTestServer(t)
	h.cfg.WSPollInterval = 10 * time.Second
	h.cfg.WSRemoteDebounce = 500 * time.Millisecond // shortened from a second; still well over the 180 ms burst
	src := &fakeSource{}
	h.SetRemoteAgents(src)
	src.set(remoteSnap("b-0"))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn := dialFeed(t, ctx, server.URL)
	time.Sleep(h.cfg.WSRemoteDebounce + 100*time.Millisecond) // past the remote limit since the first build
	// A tick inside the burst would carry the change pending then (b-2, b-8) as its own diff,
	// as it should: keep the next one out of the burst and the read window (#3932).
	clearOfTick(h.cfg.WSPollInterval, 3*time.Second)

	setAt := time.Now()
	src.set(remoteSnap("b-1"))
	first := awaitStatus(t, ctx, conn, "b-1", time.Second)
	// awaitStatus times from its own call: a test goroutine starved between the set and that call (a loaded host: the failure showed
	// first = 0s) understates how far into the window b-1's build sits, and the burst then seems to fit one window when it
	// does not. From the set it is the whole lag (#83).
	seen := first
	first = max(first, time.Since(setAt))
	// Not held back means sent before the debounce would have let it go; under a loaded gate the
	// send itself can take more than a fixed 100 ms (#4136: this failed twice in full gates).
	assert.Less(t, seen, h.cfg.WSRemoteDebounce, "the first change of a burst is not held back")
	burstStart := time.Now()
	for i := 2; i <= 10; i++ {
		src.set(remoteSnap(fmt.Sprintf("b-%d", i)))
		time.Sleep(20 * time.Millisecond)
	}
	burst := time.Since(burstStart)
	// The debounce window is anchored at b-1's build on the server, not at burstStart: burstStart is
	// set only once awaitStatus has delivered b-1 to the client, which lags the build by at most
	// `first`. So the burst sits up to `first` into the window and spans at most `first+burst` of it,
	// and that span -- not burst alone -- decides how many windows it crosses. Under load `first` grows
	// (a starved client reads b-1 late) while the sends stay ~180 ms, so the window can fire mid-burst
	// and rightly send an intermediate state; counting from burst alone then wrongly demanded a single
	// diff (the #4736 flake: 2 diffs where it expected 1, with burst still under the window).
	span := first + burst
	// The other nine changes come as one diff with the latest state, within the
	// limit, and no further diff follows. Read until the latest state has come (a loaded machine
	// can send it later than a fixed window: the test failed that way under load after #4136),
	// then one more window for a diff that should not come.
	var diffs []string
	// read takes the diffs that come within d; it stops at the one carrying the latest state when
	// untilLatest is set.
	read := func(d time.Duration, untilLatest bool) {
		rctx, stop := context.WithTimeout(ctx, d)
		defer stop()
		for {
			var msg struct {
				Type    string           `json:"type"`
				Changed []map[string]any `json:"changed"`
			}
			if err := wsjson.Read(rctx, conn, &msg); err != nil {
				return
			}
			for _, e := range msg.Changed {
				if e["session_id"] == "ra-1" {
					diffs = append(diffs, fmt.Sprint(e["status"]))
					if untilLatest && e["status"] == "b-10" {
						return
					}
				}
			}
		}
	}
	read(10*h.cfg.WSRemoteDebounce, true)
	read(2*h.cfg.WSRemoteDebounce, false) // nothing more should come
	// One coalesced diff per debounce window the burst crossed, counted from the window's anchor
	// (span = first+burst): one when the whole burst landed inside a single window (the ~180 ms it
	// takes unloaded, delivered promptly), more only when the span reached into a later window, which
	// then rightly sends what was pending by then. Never a diff per change, and the last one carries
	// the latest state.
	allowed := 1 + int(span/h.cfg.WSRemoteDebounce)
	require.NotEmpty(t, diffs, "the burst's changes arrive")
	assert.LessOrEqual(t, len(diffs), allowed, "coalesced: %v over a %s span (first %s + burst %s)", diffs, span, first, burst)
	assert.Equal(t, "b-10", diffs[len(diffs)-1], "the last diff carries the latest remote state")
	if span < h.cfg.WSRemoteDebounce {
		assert.Equal(t, []string{"b-10"}, diffs, "one coalesced diff carrying the latest remote state (span %s)", span)
	}
}
