package tunnel

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Cross-team artifacts, the hub-to-machine leg (coral-hub's Security asked): the hub opens a stream and POSTs a file
// (8 MiB) to this machine's /api/board/{p}/external-artifacts. The push's BODY travels hub to machine, written by the
// hub's own writer; this side only reads it and answers with a few bytes, and yamux's window updates are not scheduled
// by the uplink. So a keystroke or a UI request to this machine, which the shaped uplink (60 ms RTT, 5 Mbit) carries,
// should not wait behind the push. This measures that, connector side: the shaped link of the uplink tests, a fake hub
// pushing 8 MiB files back to back, and a keystroke round trip and a small UI request timed meanwhile.

// pushApp answers like the machine: the external-artifacts route reads its body (counted) and answers a few bytes; a
// GET /api/small, and the keystroke POST, answer 2 KB.
func pushApp(received *atomic.Int64) http.Handler {
	small := bytes.Repeat([]byte("s"), 2<<10)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/external-artifacts") {
			_, _ = io.Copy(countWriter{received}, r.Body) // counted as it arrives: a push in flight shows
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write(small)
	})
}

// countWriter counts what it is given and keeps none of it.
type countWriter struct{ n *atomic.Int64 }

func (c countWriter) Write(b []byte) (int, error) { c.n.Add(int64(len(b))); return len(b), nil }

// patternBody is n bytes of a pattern, read without holding them (an 8 MiB file the hub streams).
type patternBody struct{ left int }

func (p *patternBody) Read(b []byte) (int, error) {
	if p.left <= 0 {
		return 0, io.EOF
	}
	n := min(len(b), p.left)
	for i := range b[:n] {
		b[i] = byte(i)
	}
	p.left -= n
	return n, nil
}

// pushFiles has the hub push n-byte files to the external-artifacts route, one after another, until ctx ends.
func pushFiles(ctx context.Context, s *Session, n int, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			req, _ := http.NewRequest(http.MethodPost, "http://ignored/api/board/team/external-artifacts", &patternBody{left: n})
			req.ContentLength = int64(n)
			resp, err := s.RoundTrip(req)
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
}

type sample struct{ p50, p95 time.Duration }

func percentiles(lat []time.Duration) sample {
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	return sample{lat[len(lat)/2], lat[len(lat)*95/100]}
}

// interactiveLatency times 20 UI requests (a GET) and 20 keystrokes (a small POST), interleaved, and returns each's
// median and p95. With push, a fake hub pushes files meanwhile and the measured window must overlap real transfers.
func interactiveLatency(t testing.TB, s *Session, received *atomic.Int64, push bool) (ui, key sample) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	if push {
		pushFiles(ctx, s, 8<<20, &wg)
		require.Eventually(t, func() bool { return received.Load() > 256<<10 }, 20*time.Second, 10*time.Millisecond, "the push is flowing")
	}
	before := received.Load()
	var uiLat, keyLat []time.Duration
	for i := 0; i < 20; i++ {
		start := time.Now()
		resp, err := get(s, "/api/small", "")
		require.NoError(t, err)
		_, err = io.Copy(io.Discard, resp.Body)
		require.NoError(t, err)
		resp.Body.Close()
		uiLat = append(uiLat, time.Since(start))
		time.Sleep(10 * time.Millisecond)
		start = time.Now()
		req, _ := http.NewRequest(http.MethodPost, "http://ignored/api/sessions/live/a/send", strings.NewReader(`{"text":"x"}`))
		resp, err = s.RoundTrip(req)
		require.NoError(t, err)
		_, err = io.Copy(io.Discard, resp.Body)
		require.NoError(t, err)
		resp.Body.Close()
		keyLat = append(keyLat, time.Since(start))
		time.Sleep(10 * time.Millisecond)
	}
	if push {
		// The push is bounded by the stream's window per round trip (256 KiB per 60 ms, a file takes about 2 s): over the
		// ~2.5 s the requests take, a whole file's worth of bytes must have arrived.
		assert.Greater(t, received.Load()-before, int64(4<<20), "a push was in flight while the requests were timed")
	}
	cancel()
	wg.Wait()
	return percentiles(uiLat), percentiles(keyLat)
}

func shapedPushSession(t testing.TB, received *atomic.Int64) *Session {
	shape := hol
	shape.queue = sendLowWater
	return connectShaped(t, newHub(t, SessionConfig{}), pushApp(received), shape, uplinkConfig{})
}

// A keystroke and a UI request stay within the interactive bound (coral-hub's 200 ms p95, hubCriterion) while the hub
// pushes 8 MiB files to this machine, and the push adds no more than 50 ms to their median over the same link idle.
func TestInteractiveRequestsDuringAHubToMachinePush(t *testing.T) {
	if testing.Short() {
		t.Skip("throttled-link measurement")
	}
	var received atomic.Int64
	s := shapedPushSession(t, &received)
	idleUI, idleKey := interactiveLatency(t, s, &received, false)
	measure := func() (ui, key sample) { return interactiveLatency(t, s, &received, true) }
	ui, key := measure()
	t.Logf("idle: UI p50 %v p95 %v, keystroke p50 %v p95 %v; during an 8 MiB push: UI p50 %v p95 %v, keystroke p50 %v p95 %v",
		idleUI.p50.Round(time.Millisecond), idleUI.p95.Round(time.Millisecond), idleKey.p50.Round(time.Millisecond), idleKey.p95.Round(time.Millisecond),
		ui.p50.Round(time.Millisecond), ui.p95.Round(time.Millisecond), key.p50.Round(time.Millisecond), key.p95.Round(time.Millisecond))
	if ui.p95 >= hubCriterion || key.p95 >= hubCriterion {
		// As the other latency tests: a busy host can miss the absolute bound once; a real delay misses it twice.
		t.Logf("p95 missed the %v bound with load average %s: measuring once more", hubCriterion, loadavg())
		ui, key = measure()
	}
	assert.Less(t, ui.p95, hubCriterion, "a UI request's p95 during the push")
	assert.Less(t, key.p95, hubCriterion, "a keystroke's p95 during the push")
	assert.LessOrEqual(t, ui.p50, idleUI.p50+50*time.Millisecond, fmt.Sprintf("a UI request's median is within 50 ms of idle (idle %v)", idleUI.p50))
	assert.LessOrEqual(t, key.p50, idleKey.p50+50*time.Millisecond, fmt.Sprintf("a keystroke's median is within 50 ms of idle (idle %v)", idleKey.p50))
}

// pushStalledApp is pushApp whose external-artifacts handler does not read its body until release is closed: the file
// the hub pushes stops at the stream's window while the rest of the machine carries on.
func pushStalledApp(release <-chan struct{}) http.Handler {
	small := bytes.Repeat([]byte("s"), 2<<10)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/external-artifacts") {
			<-release
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write(small)
	})
}

// A push whose receiving route stalls (the handler is not reading) holds only its own stream at the window: a keystroke
// and a UI request to the same machine are not delayed behind it.
func TestInteractiveRequestsWhileAPushIsStalled(t *testing.T) {
	if testing.Short() {
		t.Skip("throttled-link measurement")
	}
	release := make(chan struct{})
	shape := hol
	shape.queue = sendLowWater
	s := connectShaped(t, newHub(t, SessionConfig{}), pushStalledApp(release), shape, uplinkConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); close(release); wg.Wait() })
	pushFiles(ctx, s, 8<<20, &wg)
	time.Sleep(500 * time.Millisecond) // the push has filled the stream's window and stopped
	var uiLat, keyLat []time.Duration
	for i := 0; i < 20; i++ {
		start := time.Now()
		resp, err := get(s, "/api/small", "")
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		uiLat = append(uiLat, time.Since(start))
		start = time.Now()
		req, _ := http.NewRequest(http.MethodPost, "http://ignored/api/sessions/live/a/send", strings.NewReader(`{"text":"x"}`))
		resp, err = s.RoundTrip(req)
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		keyLat = append(keyLat, time.Since(start))
		time.Sleep(10 * time.Millisecond)
	}
	ui, key := percentiles(uiLat), percentiles(keyLat)
	t.Logf("with a stalled push: UI p50 %v p95 %v, keystroke p50 %v p95 %v", ui.p50.Round(time.Millisecond), ui.p95.Round(time.Millisecond), key.p50.Round(time.Millisecond), key.p95.Round(time.Millisecond))
	assert.Less(t, ui.p95, hubCriterion)
	assert.Less(t, key.p95, hubCriterion)
}
