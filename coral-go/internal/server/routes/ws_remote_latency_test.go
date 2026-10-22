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

// A burst of remote changes still costs at most one build per debounce window: the first goes out at once, the rest arrive together in one later diff carrying the latest state.
//
// The window is fired by the test, not waited for (#5942). The test used to run the burst against a real 500 ms window and count the windows it crossed from three measured durations
// (the first change's lag, the burst's span, the read), and it still failed under load (#83, #4136, #4736, and "2 is not <= 1" in whole-package runs): the window is anchored at the
// server's own build, a loaded host delays the sends and the reads by amounts the test can only estimate, and a window that fires mid-burst rightly sends an intermediate state. Here the
// window is an hour and its timer is the test's (Handler.remoteAfter): no change can fire it, so nothing is sent for the burst until the test says the window is over, and then exactly one
// diff, with the latest state, comes. A rebuild per change (no coalescing) puts diffs on the wire before the window is fired and fails the first check.
func TestWSCoral_RemoteBurstIsStillCoalesced(t *testing.T) {
	server, h := setupTestServer(t)
	h.cfg.WSPollInterval = time.Hour   // no tick in the test
	h.cfg.WSRemoteDebounce = time.Hour // the window never ends by itself
	windows := make(chan chan time.Time, 4)
	h.remoteAfter = func(time.Duration) <-chan time.Time {
		w := make(chan time.Time, 1)
		windows <- w
		return w
	}
	src := &fakeSource{}
	h.SetRemoteAgents(src)
	src.set(remoteSnap("b-0"))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn := dialFeed(t, ctx, server.URL)
	// ONE reader for the whole test: a read whose context ends closes the connection, so a quiet period is watched on a channel, never with a read timeout.
	seen := make(chan string, 64)
	go func() {
		for {
			var msg struct {
				Changed []map[string]any `json:"changed"`
			}
			if wsjson.Read(ctx, conn, &msg) != nil {
				return
			}
			for _, e := range msg.Changed {
				if e["session_id"] == "ra-1" {
					seen <- fmt.Sprint(e["status"])
				}
			}
		}
	}()
	next := func(within time.Duration) (string, bool) {
		select {
		case st := <-seen:
			return st, true
		case <-time.After(within):
			return "", false
		}
	}

	// The first change of a burst is sent at once, with no window to wait for: it arrives while the window is closed to the rest.
	src.set(remoteSnap("b-1"))
	st, ok := next(30 * time.Second)
	require.True(t, ok, "the first change of a burst arrives")
	require.Equal(t, "b-1", st)

	// The other nine, in a burst: held for the window, however long the host takes to send them.
	for i := 2; i <= 10; i++ {
		src.set(remoteSnap(fmt.Sprintf("b-%d", i)))
	}
	var window chan time.Time
	select {
	case window = <-windows:
	case <-time.After(30 * time.Second):
		t.Fatal("the burst opened no debounce window")
	}
	// Nothing goes out while the window is open: a build per change would have put b-2 on the wire already.
	if st, ok := next(300 * time.Millisecond); ok {
		t.Fatalf("%q was sent while the debounce window was open (no coalescing)", st)
	}

	// The window ends: the nine changes arrive as ONE diff carrying the latest state, and no further diff follows.
	window <- time.Now()
	st, ok = next(30 * time.Second)
	require.True(t, ok, "the held changes arrive when the window ends")
	assert.Equal(t, "b-10", st, "one coalesced diff carrying the latest remote state")
	if more, ok := next(500 * time.Millisecond); ok {
		t.Fatalf("a further diff %q followed the coalesced one", more)
	}
	select {
	case <-windows:
		t.Fatal("a second window was opened for the burst")
	default:
	}
}
