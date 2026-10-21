package tunnel

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/subgentic/coral/internal/safego"
)

// Hub-side bulk pacing (#4427, coral-hub's push of a file to a machine).
//
// A request the hub marks as bulk (WithBulk) has its body written at a paced rate that keeps what it queues ahead of
// an interactive frame small. A scheduler that only orders the hub's writes cannot do that: the pushed stream's window
// (yamux's initial 256 KiB, which cannot be made smaller) is already written ahead of any later frame, and a keystroke's
// request waits for it to drain at the machine's downlink speed (420 ms at 5 Mbit). And the hub cannot see that link: its
// socket ends at a proxy or the internet, so its writes never block on it.
//
// So the bulk writer is rate-limited, and the rate follows the queue it makes. While a bulk request is writing, the
// hub pings the connector (yamux's own ping, no new frame) every bulkPingEvery; a ping's round trip minus the lowest
// seen is the queueing delay on the path, and the rate rises (fast at first, then by a few percent) while that delay is
// under half the target, and is cut by 30 percent when it is over the target: AIMD on delay, as LEDBAT does. It adapts to
// any slow link or buffer, a fast link gets close to its speed, and a keystroke sees about the target added at most.
//
// Fail-safes (coral-hub's): a ping that is late counts as congestion (a decrease); a ping that fails, panics or gets no
// answer, or a controller that panics, puts the push on a fixed conservative rate (bulkSafeRate) for the rest of it, never
// "unlimited"; the rate never rises above a ceiling (BulkMaxRate, by default bulkDefCeiling), so a peer that answers every
// ping at once cannot make the push unpaced; and the rate never falls below bulkMinRate, which lets an 8 MiB file finish
// inside the connector's limit; a push that cannot finish in its time at that floor fails fast (ErrBulkTooSlow) instead of
// holding a slot to the deadline. Only bulk writes wait: interactive requests are never paced and never wait for a pacer.
// Nothing changes for a request that is not marked: no pacer, no pings. The pacer is one small struct per session, made on
// first use and holding no goroutine unless a bulk stream is writing; its pings stop when the body is written.

type bulkCtxKey struct{}

// WithBulk marks the requests and streams opened with the returned context as bulk: their bodies are written paced, so
// they yield to interactive traffic on the same session. The default (an unmarked context) is interactive.
func WithBulk(ctx context.Context) context.Context { return context.WithValue(ctx, bulkCtxKey{}, true) }

func isBulk(ctx context.Context) bool { v, _ := ctx.Value(bulkCtxKey{}).(bool); return v }

// ErrBulkTooSlow: a bulk push whose body cannot finish in its time at the lowest rate the pacer allows, so it was ended at
// once and not at the deadline.
var ErrBulkTooSlow = errors.New("tunnel: the link is too slow to finish this transfer in time")

const (
	bulkSlice        = 16 << 10 // the most written at once
	bulkBurst        = 2 * bulkSlice
	bulkMinRate      = 160 << 10 // bytes/s, the floor: 8 MiB in about 51 s, inside half of the connector's 120 s artifact limit (a test pins this)
	bulkSafeRate     = bulkMinRate
	bulkStartRate    = 256 << 10
	bulkGrowth       = 1.3
	bulkIncrease     = 0.05
	bulkIncMin       = 16 << 10
	bulkDecrease     = 0.7
	bulkDefCeiling   = 16 << 20 // bytes/s: the most a push goes when BulkMaxRate is not set
	bulkPingEvery    = 50 * time.Millisecond
	bulkProbes       = 3                      // idle pings taken before a push, for the lowest round trip
	bulkProbeTimeout = 500 * time.Millisecond // each probe's, so a silent peer holds OpenStream for at most about 1.5 s
	bulkLateMargin   = 100 * time.Millisecond // a ping outstanding longer than base + target + this is late: a decrease
	bulkSilentAfter  = 2 * time.Second        // a ping with no answer by then: the safe rate for the rest of the push
	bulkMinDelay     = 20 * time.Millisecond  // a rate is cut at most once per base round trip, and no oftener than this
	bulkDefTarget    = 50 * time.Millisecond
	bulkDefDuration  = 120 * time.Second // how long a bulk push may take: the connector's artifact limit
)

// Metrics, with no tunnel, account or server label by design: how many tunnels have a bulk push in progress, and how many
// times a push's rate was cut (a late ping and a failed ping count too).
var (
	bulkTunnels   atomic.Int64
	bulkDecreases atomic.Uint64
)

// BulkStats reports how many sessions have a bulk stream open now, and the total number of rate cuts so far.
func BulkStats() (tunnelsInBulk int64, decreases uint64) {
	return bulkTunnels.Load(), bulkDecreases.Load()
}

var (
	errPingPanic  = errors.New("tunnel: ping panicked")
	errPingSilent = errors.New("tunnel: ping not answered")
	errPingStop   = errors.New("tunnel: ping stopped")
)

// bulkController is the rate decision, with no clock or network of its own (tests feed it samples).
type bulkController struct {
	target     time.Duration
	maxRate    float64 // the ceiling, always set
	rate       float64 // bytes/s
	base       time.Duration
	slowStart  bool
	fixed      bool // after a failure: the safe rate, and the samples are ignored
	lastChange time.Time
}

func newBulkController(target time.Duration, maxRate float64) *bulkController {
	if target <= 0 {
		target = bulkDefTarget
	}
	if maxRate <= 0 {
		maxRate = bulkDefCeiling
	}
	c := &bulkController{target: target, maxRate: maxRate}
	c.restart()
	return c
}

// restart begins a new push: the rate starts low and grows fast until a queue shows. The lowest round trip seen is kept.
func (c *bulkController) restart() {
	c.rate = min(bulkStartRate, c.maxRate)
	c.slowStart, c.fixed = true, false
	c.lastChange = time.Time{}
}

// idle notes a round trip taken with nothing queued: it can only lower the base.
func (c *bulkController) idle(rtt time.Duration) {
	if rtt > 0 && (c.base == 0 || rtt < c.base) {
		c.base = rtt
	}
}

// cut lowers the rate (once per base round trip) and counts it.
func (c *bulkController) cut(now time.Time) {
	if now.Sub(c.lastChange) < max(c.base, bulkMinDelay) { // the last cut has had a round trip to show
		return
	}
	c.rate = max(bulkMinRate, c.rate*bulkDecrease)
	c.slowStart = false
	c.lastChange = now
	bulkDecreases.Add(1)
}

// sample takes a ping's round trip, at time now, and moves the rate.
func (c *bulkController) sample(now time.Time, rtt time.Duration) {
	if c.fixed {
		return
	}
	if rtt <= 0 { // not a measurement: as bad as a missing one
		c.cut(now)
		return
	}
	c.idle(rtt)
	queue := rtt - c.base
	switch {
	case queue > c.target:
		c.cut(now)
	case queue < c.target/2:
		if c.slowStart {
			c.rate *= bulkGrowth
		} else {
			c.rate += max(bulkIncMin, c.rate*bulkIncrease)
		}
	case c.slowStart:
		c.slowStart = false // a queue is showing: the fast growth is over, with no cut
	}
	c.rate = min(c.rate, c.maxRate)
}

// late takes a ping still unanswered after lateAfter(): congestion, as a ping with a long round trip is.
func (c *bulkController) late(now time.Time) {
	if !c.fixed {
		c.cut(now)
	}
}

// lateAfter is how long a ping may be out before it counts as late.
func (c *bulkController) lateAfter(margin time.Duration) time.Duration {
	return c.base + c.target + margin
}

// failSafe puts the push on the fixed conservative rate for the rest of it: the controller can no longer be trusted
// (a ping failed, was never answered or panicked).
func (c *bulkController) failSafe() {
	if !c.fixed {
		bulkDecreases.Add(1)
	}
	c.rate = min(bulkSafeRate, c.maxRate)
	c.fixed, c.slowStart = true, false
}

// atFloor reports whether the rate is at the lowest the pacer allows (or the ceiling is below it).
func (c *bulkController) atFloor() bool { return c.rate <= bulkMinRate }

// bulkPacer paces one session's bulk writers (they share the rate) and runs the pings while any is writing.
type bulkPacer struct {
	ping        func() (time.Duration, error) // a round trip to the connector
	every       time.Duration                 // between a pong and the next ping
	lateMargin  time.Duration
	silentAfter time.Duration
	probeWait   time.Duration
	now         func() time.Time
	sleep       func(d time.Duration, done <-chan struct{}) // returns early when done closes

	mu     sync.Mutex
	ctl    *bulkController
	tokens float64
	last   time.Time
	users  int
	stop   chan struct{} // the ping loop's, closed when the last writer leaves
}

func newBulkPacer(ping func() (time.Duration, error), target time.Duration, maxRate float64) *bulkPacer {
	return &bulkPacer{
		ping: ping, every: bulkPingEvery, lateMargin: bulkLateMargin, silentAfter: bulkSilentAfter, probeWait: bulkProbeTimeout,
		now: time.Now, ctl: newBulkController(target, maxRate),
		sleep: func(d time.Duration, done <-chan struct{}) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-t.C:
			case <-done:
			}
		},
	}
}

// timedPing takes one round trip, with the fail-safes: a ping out longer than the late bound counts as congestion (once)
// while the wait goes on; one that errors, panics or is not answered within silent returns an error; stop ends the wait.
func (p *bulkPacer) timedPing(silent time.Duration, stop <-chan struct{}) (time.Duration, error) {
	type result struct {
		rtt time.Duration
		err error
	}
	res := make(chan result, 1)
	start := p.now()
	go func() {
		defer safego.Recover("tunnel/bulk_pacer:ping") // the repo's rule; the fail-safe recover below runs first
		defer func() {
			if recover() != nil {
				res <- result{err: errPingPanic}
			}
		}()
		_, err := p.ping()
		res <- result{p.now().Sub(start), err}
	}()
	p.mu.Lock()
	lateAfter := p.ctl.lateAfter(p.lateMargin)
	p.mu.Unlock()
	lateTimer, silentTimer := time.NewTimer(lateAfter), time.NewTimer(silent)
	defer lateTimer.Stop()
	defer silentTimer.Stop()
	for {
		select {
		case r := <-res:
			return r.rtt, r.err
		case <-lateTimer.C:
			p.mu.Lock()
			p.ctl.late(p.now())
			p.mu.Unlock()
		case <-silentTimer.C:
			return 0, errPingSilent
		case <-stop:
			return 0, errPingStop
		}
	}
}

// acquire registers a bulk writer. The first one takes a few idle pings (before it writes anything; each bounded, and
// by done) for the lowest round trip, restarts the rate, and starts the ping loop. If a probe fails, the push is on the
// safe rate and there is no loop.
func (p *bulkPacer) acquire(done <-chan struct{}) {
	p.mu.Lock()
	p.users++
	first := p.users == 1
	if first {
		bulkTunnels.Add(1)
	}
	p.mu.Unlock()
	if !first {
		return
	}
	probesOK := true
	for i := 0; i < bulkProbes; i++ {
		rtt, err := p.timedPing(p.probeWait, done)
		if err != nil {
			probesOK = false
			break
		}
		p.mu.Lock()
		p.ctl.idle(rtt)
		p.mu.Unlock()
	}
	p.mu.Lock()
	p.ctl.restart()
	if !probesOK {
		p.ctl.failSafe()
	}
	p.tokens, p.last = bulkSlice, p.now()
	var stop chan struct{}
	if probesOK {
		stop = make(chan struct{})
		p.stop = stop
	}
	p.mu.Unlock()
	if stop == nil {
		return
	}
	go func() {
		defer safego.Recover("tunnel/bulk_pacer:loop") // the repo's rule; loop's own fail-safe recover runs first
		p.loop(stop)
	}()
}

// loop pings while a bulk writer is registered and moves the rate by the answers.
func (p *bulkPacer) loop(stop chan struct{}) {
	defer func() {
		if recover() != nil { // the controller itself failed
			p.mu.Lock()
			p.ctl.failSafe()
			p.mu.Unlock()
		}
	}()
	for {
		p.sleep(p.every, stop)
		select {
		case <-stop:
			return
		default:
		}
		start := p.now()
		_, err := p.timedPing(p.silentAfter, stop)
		if errors.Is(err, errPingStop) {
			return
		}
		p.mu.Lock()
		if err != nil {
			p.ctl.failSafe()
			p.mu.Unlock()
			return // the rate is fixed: nothing more to measure
		}
		p.ctl.sample(p.now(), p.now().Sub(start))
		p.mu.Unlock()
	}
}

// release unregisters a bulk writer; the last one stops the pings.
func (p *bulkPacer) release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.users == 0 {
		return
	}
	p.users--
	if p.users == 0 {
		bulkTunnels.Add(-1)
		if p.stop != nil {
			close(p.stop)
			p.stop = nil
		}
	}
}

// take waits until n bytes may be written at the current rate (or done closes), and spends them.
func (p *bulkPacer) take(n int, done <-chan struct{}) {
	for {
		p.mu.Lock()
		now := p.now()
		p.tokens = min(bulkBurst, p.tokens+p.ctl.rate*now.Sub(p.last).Seconds())
		p.last = now
		if p.tokens >= float64(n) {
			p.tokens -= float64(n)
			p.mu.Unlock()
			return
		}
		wait := time.Duration((float64(n) - p.tokens) / p.ctl.rate * float64(time.Second))
		p.mu.Unlock()
		p.sleep(max(wait, time.Millisecond), done)
		select {
		case <-done:
			return
		default:
		}
	}
}

// tooSlow reports whether left bytes cannot be written before deadline at the current rate, with the rate at the floor
// (above the floor the rate may still rise, so nothing is concluded).
func (p *bulkPacer) tooSlow(left int64, deadline time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if left <= 0 || !p.ctl.atFloor() {
		return false
	}
	return time.Duration(float64(left)/p.ctl.rate*float64(time.Second)) > deadline.Sub(p.now())
}
