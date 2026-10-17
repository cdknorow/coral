package tunnel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/pkg/redact"
)

// ── A throttled link ───────────────────────────────────────────────────

// shapedConn models the connector's uplink: Write fills a send queue of at most queue
// bytes (the kernel's unsent data: TCP_NOTSENT_LOWAT keeps it short, by default it is
// hundreds of KiB), which drains at rate bytes/s and arrives owd later. Reads arrive
// owd late too (the hub's requests are small, so the downlink isn't rate-limited).
type shapedConn struct {
	net.Conn
	rate  float64
	owd   time.Duration
	queue int

	mu     sync.Mutex
	cond   *sync.Cond
	q      []byte
	closed bool

	pr *io.PipeReader
}

type packet struct {
	data []byte
	at   time.Time
}

func newShapedConn(c net.Conn, rate float64, owd time.Duration, queue int) *shapedConn {
	s := &shapedConn{Conn: c, rate: rate, owd: owd, queue: queue}
	s.cond = sync.NewCond(&s.mu)
	up := make(chan packet, 1<<16)
	go s.pump(up)
	go func() { // deliver the uplink owd after each packet left
		for p := range up {
			time.Sleep(time.Until(p.at))
			if _, err := c.Write(p.data); err != nil {
				s.Close()
				for range up {
				}
				return
			}
		}
	}()
	pr, pw := io.Pipe()
	s.pr = pr
	down := make(chan packet, 1<<16)
	go func() {
		defer close(down)
		for {
			b := make([]byte, 32<<10)
			n, err := c.Read(b)
			if n > 0 {
				down <- packet{b[:n], time.Now().Add(owd)}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		for p := range down {
			time.Sleep(time.Until(p.at))
			if _, err := pw.Write(p.data); err != nil {
				break
			}
		}
		pw.Close()
		for range down {
		}
	}()
	return s
}

func (s *shapedConn) pump(up chan<- packet) {
	defer close(up)
	const mtu = 1460
	next := time.Now()
	for {
		s.mu.Lock()
		for len(s.q) == 0 && !s.closed {
			s.cond.Wait()
		}
		if s.closed {
			s.mu.Unlock()
			return
		}
		n := min(len(s.q), mtu)
		b := append([]byte(nil), s.q[:n]...)
		s.q = s.q[n:]
		s.cond.Broadcast()
		s.mu.Unlock()
		if now := time.Now(); next.Before(now) {
			next = now
		}
		next = next.Add(time.Duration(float64(n) / s.rate * float64(time.Second)))
		time.Sleep(time.Until(next))
		up <- packet{b, next.Add(s.owd)}
	}
}

func (s *shapedConn) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	written := 0
	for len(p) > 0 {
		for len(s.q) >= s.queue && !s.closed {
			s.cond.Wait()
		}
		if s.closed {
			return written, net.ErrClosed
		}
		n := min(len(p), s.queue-len(s.q))
		s.q = append(s.q, p[:n]...)
		p = p[n:]
		written += n
		s.cond.Broadcast()
	}
	return written, nil
}

func (s *shapedConn) Read(p []byte) (int, error) { return s.pr.Read(p) }

func (s *shapedConn) Close() error {
	s.mu.Lock()
	s.closed = true
	s.cond.Broadcast()
	s.mu.Unlock()
	s.pr.Close() // the delivery goroutine's write ends too (#4167: it blocked on the pipe for good)
	return s.Conn.Close()
}

type linkShape struct {
	mbit  float64
	rtt   time.Duration
	queue int // the uplink's unsent queue, bytes
}

// connectShaped dials h through a throttled uplink with the scheduler tuned (or off).
func connectShaped(t testing.TB, h *hubHarness, local http.Handler, shape linkShape, up uplinkConfig) *Session {
	ls := httptest.NewServer(local)
	t.Cleanup(ls.Close)
	tr := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return newShapedConn(c, shape.mbit*1e6/8, shape.rtt/2, shape.queue), nil
	}}
	cs, err := Dial(context.Background(), h.URL(), testToken, ClientOptions{
		SessionConfig: h.Cfg, Handler: &LocalHandler{BaseURL: ls.URL, AllowPath: allowAll, Redactor: redact.Rules},
		CoralVersion: "test", AllowInsecure: true, HTTPClient: &http.Client{Transport: tr}, uplink: up,
	})
	require.NoError(t, err)
	t.Cleanup(func() { cs.Close() })
	return h.next(t)
}

// localApp: /small answers 2 KB; a capture path streams size= bytes of a pattern.
func localApp() http.Handler {
	small := bytes.Repeat([]byte("s"), 2<<10)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/small" {
			w.Write(small)
			return
		}
		n, _ := strconv.Atoi(r.URL.Query().Get("size"))
		w.Header().Set("Content-Length", strconv.Itoa(n))
		buf := make([]byte, 32<<10)
		for i := 0; n > 0; i++ {
			k := min(n, len(buf))
			for j := range buf[:k] {
				buf[j] = byte(i + j)
			}
			if _, err := w.Write(buf[:k]); err != nil {
				return
			}
			n -= k
		}
	})
}

func get(s *Session, path, query string) (*http.Response, error) {
	req, _ := http.NewRequest("GET", "http://ignored"+path, nil)
	req.URL.RawQuery = query
	return s.RoundTrip(req)
}

// smallReplyP95 runs bulk downloads (read as fast as the link allows) and measures a
// 2 KB reply's latency meanwhile.
func smallReplyP95(t testing.TB, s *Session, bulk int) (p50, p95 time.Duration) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < bulk; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for ctx.Err() == nil {
				resp, err := get(s, fmt.Sprintf("/api/sessions/live/a%d/capture", i), "size="+strconv.Itoa(8<<20))
				if err != nil {
					return
				}
				buf := make([]byte, 64<<10)
				for ctx.Err() == nil {
					if _, err := resp.Body.Read(buf); err != nil {
						break
					}
				}
				resp.Body.Close()
			}
		}(i)
	}
	time.Sleep(time.Duration(300+100*bulk) * time.Millisecond) // the bulk streams fill the link
	var lat []time.Duration
	for i := 0; i < 20; i++ {
		start := time.Now()
		resp, err := get(s, "/api/small", "")
		require.NoError(t, err)
		_, err = io.Copy(io.Discard, resp.Body)
		require.NoError(t, err)
		resp.Body.Close()
		lat = append(lat, time.Since(start))
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	return lat[len(lat)/2], lat[len(lat)*95/100]
}

var hol = linkShape{mbit: 5, rtt: 60 * time.Millisecond}

// smallReplyBefore and smallReplyAfter measure a small reply's latency behind bulk streams on the
// throttled link: without the scheduler and with the OS's long send queue, and with both.
func smallReplyBefore(t testing.TB, bulk int) (p50, p95 time.Duration) {
	before := hol
	before.queue = 512 << 10 // an OS send queue without TCP_NOTSENT_LOWAT
	return smallReplyP95(t, connectShaped(t, newHub(t, SessionConfig{}), localApp(), before, uplinkConfig{Off: true}), bulk)
}

func smallReplyAfter(t testing.TB, bulk int) (p50, p95 time.Duration) {
	after := hol
	after.queue = sendLowWater
	return smallReplyP95(t, connectShaped(t, newHub(t, SessionConfig{}), localApp(), after, uplinkConfig{}), bulk)
}

// hubCriterion is coral-hub's: a small reply's p95 behind bulk streams, with the scheduler.
const hubCriterion = 200 * time.Millisecond

// loadavg is the host's load average where it can be read (Linux), for the log.
func loadavg() string {
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		return strings.Join(strings.Fields(string(b))[:3], " ")
	}
	return "n/a"
}

// coral-hub's reproduction (60 ms RTT, 5 Mbit uplink): with 1 and 4 bulk streams in
// flight, a small reply's p95 drops below 0.2 s with the scheduler and a short send
// queue; without them it is longer, several times longer with 4 streams.
//
// The scheduler's gain is measured against an unscheduled baseline in the same run, so it holds
// however busy the host is. The 0.2 s bound is absolute, and other CPU work on the host (a full
// go test ./..., which this test runs alongside) eats into its ~30 ms of headroom: it is
// re-measured once if missed, with the reason and the load logged (#3716). A real regression
// misses it twice; BenchmarkUplinkSmallReplyP95 reports the figure on its own.
func TestUplinkSmallRepliesOvertakeBulk(t *testing.T) {
	if testing.Short() {
		t.Skip("throttled-link measurement (~20 s)")
	}
	for _, bulk := range []int{1, 4} {
		t.Run(fmt.Sprintf("bulk=%d", bulk), func(t *testing.T) {
			b50, b95 := smallReplyBefore(t, bulk)
			a50, a95 := smallReplyAfter(t, bulk)
			t.Logf("bulk=%d small 2 KB reply: before p50 %v p95 %v; after p50 %v p95 %v", bulk, b50.Round(time.Millisecond), b95.Round(time.Millisecond), a50.Round(time.Millisecond), a95.Round(time.Millisecond))
			// One bulk stream has at most a yamux window (256 KiB) in flight, less than the
			// unscheduled send queue, so how far it fills the queue (the baseline's delay) depends
			// on the WebSocket library's writes: 280-570 ms on the same host (#3663's
			// coder/websocket); here it must only gain. Four streams fill the queue whatever the
			// library, so there it must at least halve.
			if bulk == 1 {
				// The typical reply, not the tail: with one stream the gain is modest, and each p95 is
				// the 19th of 20 replies, so one slow reply on a shared runner decides it (#4136:
				// windows-tests, before p50 229 ms p95 238 ms, after p50 137 ms p95 285 ms). The tail
				// is held to coral-hub's criterion below.
				assert.Less(t, a50, b50, "faster than before (median)")
			} else {
				assert.Less(t, a95*2, b95, "at least twice as fast as before")
			}
			if a95 >= hubCriterion {
				t.Logf("bulk=%d: p95 %v missed the %v criterion with load average %s: measuring once more (#3716)", bulk, a95.Round(time.Millisecond), hubCriterion, loadavg())
				_, a95 = smallReplyAfter(t, bulk)
			}
			assert.Less(t, a95, hubCriterion, "small-reply p95 with the scheduler (coral-hub's criterion)")
		})
	}
}

// One bulk stream alone keeps its throughput: on the throttled link, and on a fast one.
func TestUplinkSingleBulkThroughput(t *testing.T) {
	if testing.Short() {
		t.Skip("throughput measurement")
	}
	download := func(s *Session, size int) time.Duration {
		start := time.Now()
		resp, err := get(s, "/api/sessions/live/a/capture", "size="+strconv.Itoa(size))
		require.NoError(t, err)
		n, err := io.Copy(io.Discard, resp.Body)
		require.NoError(t, err)
		resp.Body.Close()
		require.EqualValues(t, size, n)
		return time.Since(start)
	}
	// Throttled: 1 MiB at 5 Mbit is ~1.7 s on the wire.
	off := hol
	off.queue = 512 << 10
	on := hol
	on.queue = sendLowWater
	tOff := download(connectShaped(t, newHub(t, SessionConfig{}), localApp(), off, uplinkConfig{Off: true}), 1<<20)
	tOn := download(connectShaped(t, newHub(t, SessionConfig{}), localApp(), on, uplinkConfig{}), 1<<20)
	t.Logf("throttled 1 MiB: before %v, after %v", tOff.Round(time.Millisecond), tOn.Round(time.Millisecond))
	assert.Less(t, float64(tOn), float64(tOff)*1.10, "within 10%")

	// Fast (loopback, no shaping): 64 MiB, best of 3 each.
	best := func(up uplinkConfig) time.Duration {
		h := newHub(t, SessionConfig{})
		ls := httptest.NewServer(localApp())
		t.Cleanup(ls.Close)
		cs, err := Dial(context.Background(), h.URL(), testToken, ClientOptions{SessionConfig: h.Cfg, Handler: &LocalHandler{BaseURL: ls.URL, AllowPath: allowAll, Redactor: redact.Rules}, CoralVersion: "test", AllowInsecure: true, uplink: up})
		require.NoError(t, err)
		t.Cleanup(func() { cs.Close() })
		s := h.next(t)
		d := time.Hour
		for i := 0; i < 3; i++ {
			d = min(d, download(s, 64<<20))
		}
		return d
	}
	fOff, fOn := best(uplinkConfig{Off: true}), best(uplinkConfig{})
	t.Logf("loopback 64 MiB: before %v (%.0f MB/s), after %v (%.0f MB/s)", fOff.Round(time.Millisecond), 64/fOff.Seconds(), fOn.Round(time.Millisecond), 64/fOn.Seconds())
	assert.Less(t, float64(fOn), float64(fOff)*1.5, "no large regression on a fast link")
}

// ── The scheduler's rules (coral-hub's acceptance tests 1-4) ───────────

// grants runs workers against u, each holding the link for hold per slice, and records
// the order of grants by lane.
type grantLog struct {
	mu   sync.Mutex
	bulk []time.Time
	n    [2]int
}

func worker(ctx context.Context, u *uplink, lane, n int, hold time.Duration, g *grantLog) {
	for ctx.Err() == nil {
		if err := u.acquire(lane, n, ctx.Done()); err != nil {
			return
		}
		g.mu.Lock()
		g.n[lane]++
		if lane == laneBulk {
			g.bulk = append(g.bulk, time.Now())
		}
		g.mu.Unlock()
		time.Sleep(hold)
		u.release()
	}
}

// (1) A bulk floor in time: 4 chatty streams in a tight loop, with credit to spare,
// can't stall a download: it is served at least every second (in practice every
// ~BulkFloor).
func TestUplinkBulkFloor(t *testing.T) {
	u := newUplink(uplinkConfig{CreditRate: 1 << 40, CreditBurst: 1 << 40}) // unlimited credit: only the floor helps bulk
	g := &grantLog{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); worker(ctx, u, laneInteractive, 2<<10, time.Millisecond, g) }()
	}
	time.Sleep(50 * time.Millisecond) // the chatty streams are busy first
	wg.Add(1)
	start := time.Now()
	go func() { defer wg.Done(); worker(ctx, u, laneBulk, 16<<10, time.Millisecond, g) }()
	wg.Wait()
	g.mu.Lock()
	defer g.mu.Unlock()
	require.NotEmpty(t, g.bulk)
	prev, worst := start, time.Duration(0)
	for _, at := range g.bulk {
		worst = max(worst, at.Sub(prev))
		prev = at
	}
	t.Logf("bulk grants %d, interactive %d, longest bulk wait %v", g.n[laneBulk], g.n[laneInteractive], worst)
	assert.Less(t, worst, time.Second, "bulk progresses every second")
	assert.Less(t, worst, 3*u.cfg.BulkFloor, "in practice about every BulkFloor")
	assert.Greater(t, g.n[laneInteractive], 4*g.n[laneBulk], "interactive still goes first")
}

// Many bulk streams don't take the interactive lane's turns: with 4 bulk streams
// keeping the link busy, an interactive write waits about one slice, not for the
// bulk queue (the floor counts from the bulk lane's last turn, not each waiter's).
func TestUplinkInteractiveOvertakesManyBulkStreams(t *testing.T) {
	u := newUplink(uplinkConfig{})
	g := &grantLog{}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); worker(ctx, u, laneBulk, 16<<10, 20*time.Millisecond, g) }()
	}
	time.Sleep(200 * time.Millisecond) // every bulk waiter has waited past the floor
	var worst time.Duration
	for i := 0; i < 20; i++ {
		start := time.Now()
		require.NoError(t, u.acquire(laneInteractive, 2<<10, nil))
		worst = max(worst, time.Since(start))
		u.release()
		time.Sleep(15 * time.Millisecond)
	}
	cancel()
	wg.Wait()
	t.Logf("longest interactive wait %v (a bulk slice holds the link 20 ms)", worst)
	assert.Less(t, worst, 60*time.Millisecond, "one slice (sometimes a floor turn), not the bulk queue")
}

// (2) Interactive credit is capped per connection: many streams can't farm priority.
// With 16 interactive streams sending continuously, once the bucket is spent the lanes
// take turns, so the one bulk stream still gets about half the slices.
func TestUplinkInteractiveCreditIsCapped(t *testing.T) {
	u := newUplink(uplinkConfig{BulkFloor: time.Hour, CreditRate: 64 << 10, CreditBurst: 64 << 10})
	g := &grantLog{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); worker(ctx, u, laneInteractive, 16<<10, 500*time.Microsecond, g) }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); worker(ctx, u, laneBulk, 16<<10, 500*time.Microsecond, g) }()
	wg.Wait()
	g.mu.Lock()
	defer g.mu.Unlock()
	share := float64(g.n[laneBulk]) / float64(g.n[laneBulk]+g.n[laneInteractive])
	t.Logf("bulk share %.2f (%d of %d)", share, g.n[laneBulk], g.n[laneBulk]+g.n[laneInteractive])
	assert.Greater(t, share, 0.35, "bulk isn't starved by many interactive streams")

	// With credit to spare, interactive goes first.
	u2 := newUplink(uplinkConfig{BulkFloor: time.Hour})
	u2.busy = true
	u2.q[laneBulk] = []*upWaiter{{lane: laneBulk, n: 1, since: time.Now(), ready: make(chan struct{})}}
	u2.q[laneInteractive] = []*upWaiter{{lane: laneInteractive, n: 1, since: time.Now(), ready: make(chan struct{})}}
	u2.busy = false
	u2.mu.Lock()
	lane, _ := u2.pickLocked()
	u2.mu.Unlock()
	assert.Equal(t, laneInteractive, lane)
}

// (3) A stalled hub reader back-pressures only its own stream: the connector stops
// reading that local response once the stream's window is full, the queue holds at most
// a slice for it, and other streams still answer promptly.
func TestUplinkStalledReaderBackPressures(t *testing.T) {
	var produced atomic.Int64
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/small" {
			w.Write([]byte("ok"))
			return
		}
		buf := make([]byte, 16<<10)
		for i := 0; i < 1024; i++ { // up to 16 MiB
			if _, err := w.Write(buf); err != nil {
				return
			}
			produced.Add(int64(len(buf)))
		}
	})
	h := newHub(t, SessionConfig{})
	s := connectShaped(t, h, app, linkShape{mbit: 100, rtt: 2 * time.Millisecond, queue: sendLowWater}, uplinkConfig{})
	resp, err := get(s, "/api/sessions/live/a/file-content", "") // never read: the hub's reader is stalled
	require.NoError(t, err)
	defer resp.Body.Close()
	time.Sleep(500 * time.Millisecond)
	stalled := produced.Load()
	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, stalled, produced.Load(), "the local response is no longer read")
	// The window (256 KiB) plus the socket buffers between the local server and the
	// connector (megabytes on Linux): bounded, well short of the 16 MiB response.
	assert.Less(t, stalled, int64(8<<20), "bounded, not the whole response")

	for i := 0; i < 5; i++ {
		start := time.Now()
		r, err := get(s, "/api/small", "")
		require.NoError(t, err)
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		assert.Equal(t, "ok", string(b))
		assert.Less(t, time.Since(start), 100*time.Millisecond, "another stream isn't held up by the stalled one")
	}
}

// (4) Frames are never reordered within a stream: concurrent bulk and interactive
// streams through the scheduler each arrive byte-exact.
func TestUplinkKeepsEachStreamInOrder(t *testing.T) {
	h := newHub(t, SessionConfig{})
	s := connectShaped(t, h, localApp(), linkShape{mbit: 200, rtt: 2 * time.Millisecond, queue: sendLowWater}, uplinkConfig{Slice: 4 << 10})
	want := func(size int) [32]byte {
		var b bytes.Buffer
		app := localApp()
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest("GET", "/x?size="+strconv.Itoa(size), nil))
		b.Write(rec.Body.Bytes())
		return sha256.Sum256(b.Bytes())
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := "/api/other"
			if i%2 == 0 {
				path = fmt.Sprintf("/api/sessions/live/a%d/capture", i)
			}
			size := 300<<10 + i*7919
			resp, err := get(s, path, "size="+strconv.Itoa(size))
			if !assert.NoError(t, err) {
				return
			}
			defer resp.Body.Close()
			got, err := io.ReadAll(resp.Body)
			assert.NoError(t, err)
			assert.Equal(t, want(size), sha256.Sum256(got), path)
		}(i)
	}
	wg.Wait()
}

func TestLaneFor(t *testing.T) {
	for path, lane := range map[string]int{
		"/api/sessions/live/claude-x/capture":       laneBulk,
		"/api/sessions/live/claude-x/diff":          laneBulk,
		"/api/sessions/live/claude-x/changes.diff":  laneBulk,
		"/api/sessions/live/claude-x/file-content":  laneBulk,
		"/api/sessions/live/claude-x/file-original": laneBulk,
		"/api/sessions/live/claude-x/chat":          laneInteractive,
		"/api/sessions/live/claude-x/poll":          laneInteractive,
		"/api/sessions/live/a/b/capture":            laneInteractive,
		"/api/board/p/messages":                     laneInteractive,
	} {
		assert.Equal(t, lane, laneFor(Header{Kind: KindHTTP, Method: "GET", Path: path}), path)
	}
	assert.Equal(t, laneInteractive, laneFor(Header{Kind: KindHTTP, Method: "POST", Path: "/api/sessions/live/x/capture"}))
	assert.Equal(t, laneInteractive, laneFor(Header{Kind: KindWS, Path: "/api/sessions/live/x/capture"}))
}

// An abandoned wait (the stream ended) leaves the queue, and a grant to an abandoned
// waiter passes the link on.
func TestUplinkAbandonedWait(t *testing.T) {
	u := newUplink(uplinkConfig{})
	require.NoError(t, u.acquire(laneBulk, 1, nil)) // holds the link
	done := make(chan struct{})
	errc := make(chan error, 1)
	go func() { errc <- u.acquire(laneInteractive, 1, done) }()
	require.Eventually(t, func() bool { u.mu.Lock(); defer u.mu.Unlock(); return len(u.q[laneInteractive]) == 1 }, time.Second, time.Millisecond)
	close(done)
	assert.ErrorIs(t, <-errc, errUplinkClosed)
	u.mu.Lock()
	assert.Empty(t, u.q[laneInteractive])
	u.mu.Unlock()
	u.release()
	require.NoError(t, u.acquire(laneBulk, 1, nil), "the link is free again")
	u.release()
}

// #3440 v3 (coral-hub saw a poll body trail its headers by ~90 ms): a non-bulk reply
// of unknown length (headers flushed, then a small body), and a bodyless 304, follow
// their headers at once through the scheduler, well within one RTT.
func TestUnknownLengthBodyFollowsItsHeaders(t *testing.T) {
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sessions/live/x/poll304" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush() // headers first: the body's length is unknown
		w.Write(bytes.Repeat([]byte("p"), 2<<10))
	})
	s := hol
	s.queue = sendLowWater
	sess := connectShaped(t, newHub(t, SessionConfig{}), app, s, uplinkConfig{})
	for _, path := range []string{"/api/sessions/live/x/poll", "/api/sessions/live/x/poll304"} {
		var worst time.Duration
		for i := 0; i < 10; i++ {
			resp, err := get(sess, path, "")
			require.NoError(t, err)
			afterHeaders := time.Now()
			_, err = io.Copy(io.Discard, resp.Body)
			require.NoError(t, err)
			resp.Body.Close()
			worst = max(worst, time.Since(afterHeaders))
		}
		t.Logf("%s: body after headers, worst %v", path, worst.Round(time.Millisecond))
		assert.Less(t, worst, hol.rtt, path)
	}
}

// CORAL_TUNNEL_SCHEDULER=off: the session's writes go straight to yamux.
func TestSchedulerSwitch(t *testing.T) {
	t.Setenv(schedulerEnv, "off")
	h := newHub(t, SessionConfig{})
	cs, err := Dial(context.Background(), h.URL(), testToken, ClientOptions{SessionConfig: h.Cfg, Handler: HandlerFunc(func(context.Context, *Stream) {}), AllowInsecure: true})
	require.NoError(t, err)
	defer cs.Close()
	h.next(t)
	assert.Nil(t, cs.up)
}

// BenchmarkUplinkSmallReplyP95 reports coral-hub's criterion on its own (#3716): a small reply's
// p95 behind 1 and 4 bulk streams with the scheduler, as p95-ms, failing above 0.2 s. Run it on
// a quiet host: go test -run '^$' -bench UplinkSmallReplyP95 -benchtime 1x ./pkg/tunnel
func BenchmarkUplinkSmallReplyP95(b *testing.B) {
	for _, bulk := range []int{1, 4} {
		b.Run(fmt.Sprintf("bulk=%d", bulk), func(b *testing.B) {
			var worst time.Duration
			for i := 0; i < b.N; i++ {
				_, p95 := smallReplyAfter(b, bulk)
				worst = max(worst, p95)
			}
			b.ReportMetric(float64(worst.Milliseconds()), "p95-ms")
			if worst >= hubCriterion {
				b.Errorf("p95 %v, criterion %v (load average %s)", worst, hubCriterion, loadavg())
			}
		})
	}
}
