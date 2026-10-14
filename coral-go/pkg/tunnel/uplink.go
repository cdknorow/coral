package tunnel

import (
	"errors"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

// Uplink scheduling (#3440). Every stream's bytes share one yamux session over one
// WebSocket/TCP connection, and yamux sends frames in the order writers reach it. A
// small reply therefore waited behind every bulk frame already queued (a 2 KB chat
// reply took 0.44 s with one terminal capture in flight on a 5 Mbit uplink, 1.1 s with
// four). The connector now passes each stream's writes through one link scheduler:
//
//   - writes go out in slices of at most 16 KiB, one slice in the send path at a time,
//     so another stream's reply never waits behind more than one slice;
//   - two lanes: BULK is a GET of a terminal capture, a diff or a file's content (by
//     path, the same list as the hub's bulk lane), everything else is INTERACTIVE;
//   - interactive goes first while it has credit, a token bucket per connection, so
//     opening many streams can't starve bulk; out of credit, the lanes take turns;
//   - the bulk lane gets a turn at least every BulkFloor whatever is waiting (a download
//     always makes progress);
//   - a stream whose window is closed (the hub isn't reading it) gives its turn back
//     after windowWait instead of holding the link.
//
// It only orders bytes: nothing is refused, no frame of a stream is reordered (a
// stream's slices go in order), and nothing changes on the wire. yamux's own frames
// (pings, window updates) don't pass through it. The kernel's send queue is kept short
// by TCP_NOTSENT_LOWAT on the tunnel's socket (LowLatencyControl), so priority isn't
// lost again in the OS.

// uplinkConfig tunes the scheduler; zero fields take the defaults.
type uplinkConfig struct {
	Off         bool          // no scheduling (tests compare against it)
	Slice       int           // bytes per slice, default 16 KiB
	BulkFloor   time.Duration // a bulk slice waiting this long goes next, default 100 ms
	CreditRate  float64       // interactive priority, bytes/s, default 256 KiB/s
	CreditBurst float64       // default 512 KiB
	WindowWait  time.Duration // a slot holder may wait this long for its stream's window, default 10 ms
}

func (c uplinkConfig) withDefaults() uplinkConfig {
	if c.Slice <= 0 {
		c.Slice = 16 << 10
	}
	if c.BulkFloor <= 0 {
		c.BulkFloor = 100 * time.Millisecond
	}
	if c.CreditRate <= 0 {
		c.CreditRate = 256 << 10
	}
	if c.CreditBurst <= 0 {
		c.CreditBurst = 512 << 10
	}
	if c.WindowWait <= 0 {
		c.WindowWait = 10 * time.Millisecond
	}
	return c
}

// sendLowWater is the most unsent data the kernel may hold for the tunnel's socket
// (TCP_NOTSENT_LOWAT, where available): about 26 ms on a 5 Mbit uplink, so a reply the
// scheduler puts first isn't queued behind megabytes the OS already took. In-flight
// (sent, unacknowledged) data isn't limited, so throughput on a fast link is unchanged.
const sendLowWater = 16 << 10

// wholeReplyMax: a local reply of known length up to this size is sent in one write.
const wholeReplyMax = 16 << 10

const (
	laneInteractive = 0
	laneBulk        = 1
)

// bulkPaths: GETs whose answers are large and not waited on by a person typing (the
// hub's bulk lane uses the same list).
var bulkPaths = regexp.MustCompile(`^/api/sessions/live/[^/]+/(capture|diff|changes\.diff|file-content|file-original)$`)

// laneFor classifies a stream from its request.
func laneFor(h Header) int {
	if h.Kind == KindHTTP && (h.Method == http.MethodGet || h.Method == "") && bulkPaths.MatchString(h.Path) {
		return laneBulk
	}
	return laneInteractive
}

var errUplinkClosed = errors.New("tunnel: stream closed")

type upWaiter struct {
	lane    int
	n       int
	since   time.Time
	ready   chan struct{}
	granted bool
}

// uplink is one connection's scheduler. One slice holds the link at a time.
type uplink struct {
	cfg uplinkConfig
	now func() time.Time

	mu       sync.Mutex
	busy     bool
	q        [2][]*upWaiter
	credit   float64
	refilled time.Time
	lastLane int
	lastBulk time.Time // when the bulk lane last had a turn
	perByte  float64   // seconds per byte the send path has been taking, a moving average (0: not known yet)
}

// bigSliceTime: on an idle link a write may go as one larger frame only if the link
// sends it within this long (a fast link, where smaller frames would cost throughput).
const bigSliceTime = 5 * time.Millisecond

// observe records that n bytes took d to go into the send path.
func (u *uplink) observe(n int, d time.Duration) {
	if d <= 0 {
		return
	}
	// Time per byte, not bytes per second: a write that lands in an empty send queue
	// returns at once, and an average of rates would be dominated by those.
	if n <= 0 {
		return
	}
	x := d.Seconds() / float64(n)
	u.mu.Lock()
	if u.perByte == 0 {
		u.perByte = x
	} else {
		u.perByte += 0.2 * (x - u.perByte)
	}
	u.mu.Unlock()
}

// idleSlice is how much a write may send at once on an idle link.
func (u *uplink) idleSlice() int {
	u.mu.Lock()
	perByte := u.perByte
	u.mu.Unlock()
	if perByte == 0 {
		return u.cfg.Slice
	}
	n := int(bigSliceTime.Seconds() / perByte)
	return max(u.cfg.Slice, min(n, 2*u.cfg.Slice+u.cfg.Slice/4))
}

func newUplink(cfg uplinkConfig) *uplink {
	cfg = cfg.withDefaults()
	u := &uplink{cfg: cfg, now: time.Now}
	u.credit, u.refilled = cfg.CreditBurst, u.now()
	return u
}

// acquire waits for the link. done aborts the wait (the stream or session ended).
func (u *uplink) acquire(lane, n int, done <-chan struct{}) error {
	_, err := u.acquireIdle(lane, n, done)
	return err
}

// acquireIdle is acquire, also reporting whether the link was idle with nobody waiting.
func (u *uplink) acquireIdle(lane, n int, done <-chan struct{}) (idle bool, err error) {
	u.mu.Lock()
	w := &upWaiter{lane: lane, n: n, since: u.now(), ready: make(chan struct{})}
	if !u.busy && len(u.q[0]) == 0 && len(u.q[1]) == 0 {
		u.grantLocked(w)
		u.mu.Unlock()
		return true, nil
	}
	u.q[lane] = append(u.q[lane], w)
	u.mu.Unlock()
	select {
	case <-w.ready:
		return false, nil
	case <-done:
	}
	u.mu.Lock()
	if w.granted { // granted as it was abandoned: pass the turn on
		u.busy = false
		u.dispatchLocked()
		u.mu.Unlock()
		return false, errUplinkClosed
	}
	q := u.q[lane]
	for i, x := range q {
		if x == w {
			u.q[lane] = append(q[:i:i], q[i+1:]...)
			break
		}
	}
	u.mu.Unlock()
	return false, errUplinkClosed
}

// release gives the link to the next waiter.
func (u *uplink) release() {
	u.mu.Lock()
	u.busy = false
	u.dispatchLocked()
	u.mu.Unlock()
}

func (u *uplink) grantLocked(w *upWaiter) {
	u.busy = true
	w.granted = true
	if w.lane == laneInteractive {
		u.refillLocked()
		u.credit -= float64(w.n)
		if u.credit < -u.cfg.CreditBurst {
			u.credit = -u.cfg.CreditBurst
		}
	}
	u.lastLane = w.lane
	if w.lane == laneBulk {
		u.lastBulk = u.now()
	}
	close(w.ready)
}

func (u *uplink) refillLocked() {
	now := u.now()
	u.credit += now.Sub(u.refilled).Seconds() * u.cfg.CreditRate
	if u.credit > u.cfg.CreditBurst {
		u.credit = u.cfg.CreditBurst
	}
	u.refilled = now
}

func (u *uplink) dispatchLocked() {
	if u.busy {
		return
	}
	lane, ok := u.pickLocked()
	if !ok {
		return
	}
	w := u.q[lane][0]
	u.q[lane][0] = nil
	u.q[lane] = u.q[lane][1:]
	u.grantLocked(w)
}

// pickLocked chooses the lane that goes next.
func (u *uplink) pickLocked() (int, bool) {
	hasI, hasB := len(u.q[laneInteractive]) > 0, len(u.q[laneBulk]) > 0
	switch {
	case !hasI && !hasB:
		return 0, false
	case !hasB:
		return laneInteractive, true
	case !hasI:
		return laneBulk, true
	}
	// The bulk floor: the bulk LANE gets a turn at least every BulkFloor (counted from
	// its last turn, or from when its first waiter arrived), so a download always
	// progresses; between those turns interactive goes first, however many bulk
	// streams are waiting.
	since := u.q[laneBulk][0].since
	if u.lastBulk.After(since) {
		since = u.lastBulk
	}
	if u.now().Sub(since) >= u.cfg.BulkFloor {
		return laneBulk, true
	}
	u.refillLocked()
	if u.credit >= float64(u.q[laneInteractive][0].n) {
		return laneInteractive, true
	}
	if u.lastLane == laneInteractive { // out of credit: take turns
		return laneBulk, true
	}
	return laneInteractive, true
}

// upWriter is a stream's writer through the scheduler.
type upWriter struct {
	st   *yamux.Stream
	u    *uplink
	lane int
	done <-chan struct{}
}

func (w *upWriter) Write(p []byte) (int, error) {
	if w.u == nil {
		return w.st.Write(p)
	}
	written := 0
	for len(p) > 0 {
		// A slice, without a tiny tail (a chunk's 4-byte prefix). On an idle, fast link
		// with nobody waiting, up to two: one stream alone keeps yamux's larger frames
		// (each frame is a WebSocket message) where a frame takes at most bigSliceTime,
		// so a reply arriving meanwhile waits no longer than that. A slow link always
		// gets single slices.
		n := min(len(p), w.u.cfg.Slice)
		if len(p) <= w.u.cfg.Slice+w.u.cfg.Slice/4 {
			n = len(p)
		}
		idle, err := w.u.acquireIdle(w.lane, n, w.done)
		if err != nil {
			return written, err
		}
		if idle {
			n = min(len(p), max(n, w.u.idleSlice()))
		}
		_ = w.st.SetWriteDeadline(time.Now().Add(w.u.cfg.WindowWait))
		start := time.Now()
		m, err := w.st.Write(p[:n])
		_ = w.st.SetWriteDeadline(time.Time{})
		if err == nil {
			w.u.observe(m, time.Since(start))
		}
		w.u.release()
		written += m
		if errors.Is(err, yamux.ErrTimeout) {
			// The stream's window is closed: the hub isn't reading it. The turn is given
			// back; the rest of this slice (at most one) goes when the window opens.
			m, err = w.st.Write(p[m:n])
			written += m
		}
		if err != nil {
			return written, err
		}
		p = p[n:]
	}
	return written, nil
}
