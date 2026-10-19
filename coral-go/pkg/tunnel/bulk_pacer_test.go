package tunnel

import (
	"context"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The bulk controller and pacer (#4427) with no network: samples, fake pings and a fake clock are fed in.

func TestBulkControllerSlowStartThenAIMD(t *testing.T) {
	c := newBulkController(50*time.Millisecond, 0)
	now := time.Unix(1000, 0)
	assert.EqualValues(t, bulkStartRate, c.rate)
	c.idle(60 * time.Millisecond)
	// A low queue (rtt within the base plus half the target): the rate grows by the growth factor each sample while slow-starting.
	c.sample(now, 70*time.Millisecond)
	assert.InDelta(t, bulkStartRate*bulkGrowth, c.rate, 1)
	c.sample(now, 60*time.Millisecond)
	assert.InDelta(t, bulkStartRate*bulkGrowth*bulkGrowth, c.rate, 1)
	assert.True(t, c.slowStart)
	// In between (queue 25 to 50 ms): no change in rate, but a queue is showing, so the fast growth is over.
	before := c.rate
	c.sample(now, 60*time.Millisecond+30*time.Millisecond)
	assert.Equal(t, before, c.rate)
	assert.False(t, c.slowStart, "slow start ends when a queue first shows")
	// Over the target: cut by 30 percent.
	c.sample(now, 60*time.Millisecond+80*time.Millisecond)
	assert.InDelta(t, before*bulkDecrease, c.rate, 1)
	// Then it grows by a few percent, not by half.
	c.rate = 1 << 20
	r := c.rate
	c.sample(now.Add(time.Second), 60*time.Millisecond)
	assert.InDelta(t, r+r*bulkIncrease, c.rate, 1)
	// A small rate still grows by at least the minimum step.
	c.rate = 200 << 10
	c.sample(now.Add(2*time.Second), 60*time.Millisecond)
	assert.InDelta(t, 200<<10+bulkIncMin, c.rate, 1)
}

func TestBulkControllerCutsOncePerRoundTripAndKeepsAFloor(t *testing.T) {
	c := newBulkController(50*time.Millisecond, 0)
	c.idle(100 * time.Millisecond)
	c.rate = 4 << 20
	now := time.Unix(1000, 0)
	over := 100*time.Millisecond + 200*time.Millisecond
	c.sample(now, over)
	cut := c.rate
	assert.InDelta(t, 4<<20*bulkDecrease, cut, 1)
	// Another over-target sample within the base round trip (100 ms) is the same queue: not cut again.
	c.sample(now.Add(50*time.Millisecond), over)
	assert.Equal(t, cut, c.rate)
	c.sample(now.Add(120*time.Millisecond), over)
	assert.InDelta(t, cut*bulkDecrease, c.rate, 1)
	// A slow peer whose queue never drains ends at the floor, and stays there.
	for i := 1; i < 60; i++ {
		c.sample(now.Add(time.Duration(i)*time.Second), over)
	}
	assert.EqualValues(t, bulkMinRate, c.rate)
	assert.True(t, c.atFloor())
}

// The floor lets an 8 MiB file finish inside half of the connector's artifact limit (coral-hub's fail-safe 3).
func TestBulkFloorFitsTheConnectorsLimit(t *testing.T) {
	seconds := float64(RelayDefaultArtifact) / bulkMinRate
	assert.LessOrEqual(t, seconds, artifactNow().totalFor.Seconds()/2, "an 8 MiB file at the floor takes %.0f s of the connector's %v", seconds, artifactNow().totalFor)
	assert.LessOrEqual(t, float64(bulkMinRate), float64(bulkStartRate), "the floor is not above where a push starts")
	assert.EqualValues(t, bulkMinRate, bulkSafeRate, "the fixed fail-safe rate is the floor")
}

// A peer that answers every ping at once (it lies, or the path is empty) cannot make the push unpaced: the rate stops at
// the ceiling, BulkMaxRate or the default.
func TestBulkControllerALyingPeerStopsAtTheCeiling(t *testing.T) {
	for name, tc := range map[string]struct {
		max  float64
		want float64
	}{"default ceiling": {0, bulkDefCeiling}, "BulkMaxRate": {1 << 20, 1 << 20}} {
		c := newBulkController(50*time.Millisecond, tc.max)
		c.idle(time.Millisecond)
		now := time.Unix(1000, 0)
		for i := 0; i < 500; i++ {
			c.sample(now.Add(time.Duration(i)*50*time.Millisecond), time.Millisecond) // every pong instant
		}
		assert.EqualValues(t, tc.want, c.rate, name)
	}
}

func TestBulkControllerLateAndMalformedAnswersAreCongestion(t *testing.T) {
	c := newBulkController(50*time.Millisecond, 0)
	c.idle(60 * time.Millisecond)
	c.rate = 2 << 20
	now := time.Unix(1000, 0)
	_, d0 := BulkStats()
	c.late(now)
	assert.InDelta(t, 2<<20*bulkDecrease, c.rate, 1, "a ping still out past the late bound is a decrease")
	assert.Equal(t, 60*time.Millisecond+50*time.Millisecond+bulkLateMargin, c.lateAfter(bulkLateMargin))
	// A round trip that is not a measurement (zero or negative) is as bad as a missing one, never ignored.
	c.rate = 2 << 20
	c.sample(now.Add(time.Second), 0)
	assert.InDelta(t, 2<<20*bulkDecrease, c.rate, 1)
	c.rate = 2 << 20
	c.sample(now.Add(2*time.Second), -time.Second)
	assert.InDelta(t, 2<<20*bulkDecrease, c.rate, 1)
	_, d1 := BulkStats()
	assert.Equal(t, uint64(3), d1-d0, "each decrease is counted")
}

func TestBulkControllerFailSafeIsFixed(t *testing.T) {
	c := newBulkController(50*time.Millisecond, 0)
	c.idle(50 * time.Millisecond)
	c.rate = 8 << 20
	_, d0 := BulkStats()
	c.failSafe()
	assert.EqualValues(t, bulkSafeRate, c.rate, "the safe rate, not unlimited")
	for i := 0; i < 100; i++ { // good answers afterwards change nothing
		c.sample(time.Unix(1000+int64(i), 0), 50*time.Millisecond)
		c.late(time.Unix(2000+int64(i), 0))
	}
	assert.EqualValues(t, bulkSafeRate, c.rate)
	_, d1 := BulkStats()
	assert.Equal(t, uint64(1), d1-d0, "counted once")
	c.restart()
	assert.EqualValues(t, bulkStartRate, c.rate, "a new push starts again")
	assert.False(t, c.fixed)
	// The cap wins over the safe rate and the start rate when it is lower.
	assert.EqualValues(t, 100<<10, newBulkController(0, 100<<10).rate)
	c = newBulkController(0, 100<<10)
	c.failSafe()
	assert.EqualValues(t, 100<<10, c.rate)
	assert.Equal(t, bulkDefTarget, newBulkController(0, 0).target, "the default target")
}

func TestBulkControllerRespectsTheCapAndTheBase(t *testing.T) {
	c := newBulkController(50*time.Millisecond, 1<<20)
	c.idle(50 * time.Millisecond)
	for i := 0; i < 20; i++ {
		c.sample(time.Unix(1000, 0), 50*time.Millisecond)
	}
	assert.EqualValues(t, 1<<20, c.rate, "capped at BulkMaxRate")
	c.idle(80 * time.Millisecond)
	assert.Equal(t, 50*time.Millisecond, c.base, "the base only goes down")
	c.idle(30 * time.Millisecond)
	assert.Equal(t, 30*time.Millisecond, c.base)
	c.idle(0)
	assert.Equal(t, 30*time.Millisecond, c.base)
}

// fakeClock lets a pacer run on virtual time: sleeping advances it.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeClock) now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.t }
func (f *fakeClock) sleep(d time.Duration, _ <-chan struct{}) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}

func fakePacer(rate float64) (*bulkPacer, *fakeClock) {
	fc := &fakeClock{t: time.Unix(1000, 0)}
	p := newBulkPacer(func() (time.Duration, error) { return 60 * time.Millisecond, nil }, 50*time.Millisecond, 0)
	p.now, p.sleep = fc.now, fc.sleep
	p.ctl.rate, p.last = rate, fc.now()
	return p, fc
}

func TestBulkPacerWritesAtTheRate(t *testing.T) {
	p, fc := fakePacer(1 << 20) // 1 MiB/s
	start := fc.now()
	for i := 0; i < 64; i++ { // 1 MiB in 16 KiB slices
		p.take(bulkSlice, nil)
	}
	assert.InDelta(t, 1.0, fc.now().Sub(start).Seconds(), 0.05, "1 MiB at 1 MiB/s takes about a second")
	// Halve the rate: the same bytes take twice as long.
	p.mu.Lock()
	p.ctl.rate = 512 << 10
	p.mu.Unlock()
	start = fc.now()
	for i := 0; i < 64; i++ {
		p.take(bulkSlice, nil)
	}
	assert.InDelta(t, 2.0, fc.now().Sub(start).Seconds(), 0.1)
}

func TestBulkPacerDoesNotSaveUpMoreThanABurst(t *testing.T) {
	p, fc := fakePacer(1 << 20)
	fc.sleep(10*time.Second, nil) // idle: ten seconds of rate would be 10 MiB of tokens
	start := fc.now()
	for i := 0; i < 8; i++ { // 128 KiB straight away
		p.take(bulkSlice, nil)
	}
	// Only a burst (32 KiB) was saved: the other 96 KiB are paced at 1 MiB/s, about 94 ms.
	assert.InDelta(t, 96.0/1024, fc.now().Sub(start).Seconds(), 0.02)
}

func TestBulkPacerTakeEndsWhenDoneCloses(t *testing.T) {
	p := newBulkPacer(func() (time.Duration, error) { return time.Millisecond, nil }, 0, 0)
	p.ctl.rate, p.last = bulkMinRate, time.Now()
	done := make(chan struct{})
	close(done)
	finished := make(chan struct{})
	go func() { p.take(bulkBurst, done); close(finished) }()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("take did not return after done closed")
	}
}

// tooSlow concludes only at the floor, and only when the bytes left cannot be written by the deadline.
func TestBulkPacerTooSlowOnlyAtTheFloor(t *testing.T) {
	p, fc := fakePacer(bulkMinRate)
	deadline := fc.now().Add(60 * time.Second)
	assert.False(t, p.tooSlow(8<<20, deadline), "8 MiB at the floor fits 60 s")
	assert.True(t, p.tooSlow(64<<20, deadline), "64 MiB at the floor does not")
	assert.False(t, p.tooSlow(0, deadline), "nothing left")
	p.mu.Lock()
	p.ctl.rate = 4 << 20 // above the floor the rate may still rise: nothing is concluded
	p.mu.Unlock()
	assert.False(t, p.tooSlow(1<<30, deadline))
}

// fastPacer is a pacer on real time with short intervals and a ping that counts.
func fastPacer(ping func() (time.Duration, error)) *bulkPacer {
	p := newBulkPacer(ping, 50*time.Millisecond, 0)
	p.every, p.silentAfter, p.probeWait, p.lateMargin = 2*time.Millisecond, 40*time.Millisecond, 30*time.Millisecond, 20*time.Millisecond
	return p
}

// The first writer probes idle round trips, then pings every interval while any writer is registered; the last release stops it.
func TestBulkPacerPingsWhileWritersAreRegistered(t *testing.T) {
	var pings atomic.Int32
	p := fastPacer(func() (time.Duration, error) { pings.Add(1); return 60 * time.Millisecond, nil })
	p.acquire(nil)
	assert.EqualValues(t, bulkProbes, pings.Load(), "the idle probes come before the first write")
	p.acquire(nil) // a second writer shares the loop: no new probes
	assert.EqualValues(t, bulkProbes, pings.Load())
	require.Eventually(t, func() bool { return pings.Load() > bulkProbes+3 }, 2*time.Second, 5*time.Millisecond, "the loop pings")
	p.release()
	n := pings.Load()
	time.Sleep(50 * time.Millisecond)
	assert.Greater(t, pings.Load(), n, "still pinging with one writer left")
	p.release()
	time.Sleep(30 * time.Millisecond) // a ping in flight may finish
	n = pings.Load()
	time.Sleep(80 * time.Millisecond)
	assert.Equal(t, n, pings.Load(), "no pings once the last writer left")
	p.release() // an extra release is harmless
	assert.Equal(t, 0, p.users)
	// A writer arriving later probes again and restarts the rate.
	p.ctl.rate = bulkMinRate
	p.acquire(nil)
	assert.EqualValues(t, bulkStartRate, p.ctl.rate)
	p.release()
}

// onSafeRate waits for the pacer to be on the fixed safe rate and the ping loop to be over.
func onSafeRate(t *testing.T, p *bulkPacer, pings *atomic.Int32) {
	t.Helper()
	require.Eventually(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.ctl.fixed }, 3*time.Second, 5*time.Millisecond, "the pacer went to the safe rate")
	p.mu.Lock()
	assert.EqualValues(t, bulkSafeRate, p.ctl.rate, "the safe rate, never unlimited")
	p.mu.Unlock()
	n := pings.Load()
	time.Sleep(60 * time.Millisecond)
	assert.Equal(t, n, pings.Load(), "no more pings once the rate is fixed")
}

// A peer that never answers (coral-hub's fail-safes 1 and 2): from the start, the probes time out and the push is on the safe
// rate (OpenStream is held for a probe, not 30 s); partway through, the unanswered ping ends the loop on the safe rate.
func TestBulkPacerASilentPeerEndsOnTheSafeRate(t *testing.T) {
	never := make(chan struct{})
	t.Cleanup(func() { close(never) })
	var pings atomic.Int32
	p := fastPacer(func() (time.Duration, error) { pings.Add(1); <-never; return 0, nil })
	start := time.Now()
	p.acquire(nil)
	assert.Less(t, time.Since(start), time.Second, "acquire is not held by a silent peer")
	onSafeRate(t, p, &pings)
	p.release()

	var calls atomic.Int32
	var mid atomic.Int32
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	p = fastPacer(func() (time.Duration, error) {
		mid.Add(1)
		if calls.Add(1) > bulkProbes+3 { // answers, then goes quiet
			<-hang
		}
		return 60 * time.Millisecond, nil
	})
	p.acquire(nil)
	onSafeRate(t, p, &mid)
	p.release()
}

// A pong that errors, or a ping that panics, partway through: the safe rate, and the push goes on.
func TestBulkPacerAFailingOrPanickingPingEndsOnTheSafeRate(t *testing.T) {
	for name, fail := range map[string]func(){"error": nil, "panic": func() { panic("boom") }} {
		var calls, pings atomic.Int32
		p := fastPacer(func() (time.Duration, error) {
			pings.Add(1)
			if calls.Add(1) > bulkProbes+3 {
				if fail != nil {
					fail()
				}
				return 0, assert.AnError
			}
			return 60 * time.Millisecond, nil
		})
		p.acquire(nil)
		onSafeRate(t, p, &pings)
		done := make(chan struct{})
		go func() { p.take(bulkSlice, nil); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: a write was held after the pings failed", name)
		}
		p.release()
	}
}

// A ping that is still out past the late bound is congestion at that moment, before any pong: the rate is cut while the
// pong has not come (and has not yet timed out as silent).
func TestBulkPacerALatePongCutsTheRate(t *testing.T) {
	var calls atomic.Int32
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	p := fastPacer(func() (time.Duration, error) {
		if calls.Add(1) > bulkProbes {
			<-hold // the pong does not come during this test
		}
		return 0, nil
	})
	p.silentAfter = 5 * time.Second     // far away: only the late bound can have cut the rate
	p.ctl.target = 5 * time.Millisecond // the late bound is base + 5 ms + 20 ms margin, about 25 ms
	p.acquire(nil)
	p.mu.Lock()
	start := p.ctl.rate
	p.mu.Unlock()
	require.Eventually(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.ctl.rate < start && !p.ctl.fixed }, time.Second, 5*time.Millisecond, "a ping out past the late bound cuts the rate before it is answered")
	p.release()
}

// Pacing state is bounded (coral-hub's fail-safe 4): 1000 tunnels with 8 bulk streams each cost one small struct and one ping
// loop per tunnel while they write, all of it gone after they end; the metrics count tunnels, not streams.
func TestBulkPacingStateIsBounded(t *testing.T) {
	assert.LessOrEqual(t, int(unsafe.Sizeof(bulkPacer{}))+int(unsafe.Sizeof(bulkController{})), 512, "a pacer is a small struct")
	baseline := runtime.NumGoroutine()
	g0, _ := BulkStats()
	const tunnels, streams = 1000, 8
	pacers := make([]*bulkPacer, tunnels)
	for i := range pacers {
		p := newBulkPacer(func() (time.Duration, error) { return 60 * time.Millisecond, nil }, 50*time.Millisecond, 0)
		p.every = 5 * time.Millisecond
		pacers[i] = p
		for j := 0; j < streams; j++ {
			p.acquire(nil)
		}
	}
	g1, _ := BulkStats()
	assert.EqualValues(t, tunnels, g1-g0, "the gauge counts tunnels in bulk, not streams")
	// One ping loop per tunnel, plus the one ping in flight per loop at a time: never per stream.
	assert.LessOrEqual(t, runtime.NumGoroutine()-baseline, 2*tunnels+50, "at most a loop and a ping per tunnel")
	for _, p := range pacers {
		for j := 0; j < streams; j++ {
			p.release()
		}
	}
	g2, _ := BulkStats()
	assert.EqualValues(t, 0, g2-g0, "the gauge returns to where it was")
	require.Eventually(t, func() bool { return runtime.NumGoroutine() <= baseline+20 }, 5*time.Second, 20*time.Millisecond, "no goroutine is left")
}

// Bulk is opt-in: an unmarked request never makes a pacer or sends a ping; a marked one does, and releases it when the body is written.
func TestBulkIsOptIn(t *testing.T) {
	var received atomic.Int64
	hub := newHub(t, SessionConfig{})
	s, _ := connect(t, hub, pushApp(&received), nil)

	req, _ := http.NewRequest(http.MethodPost, "http://ignored/api/board/team/external-artifacts", strings.NewReader(strings.Repeat("x", 100<<10)))
	resp, err := s.RoundTrip(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	assert.Nil(t, s.bulkPace, "an unmarked request does not make a pacer")
	assert.EqualValues(t, 100<<10, received.Load())

	req, _ = http.NewRequest(http.MethodPost, "http://ignored/api/board/team/external-artifacts", strings.NewReader(strings.Repeat("x", 100<<10)))
	resp, err = s.RoundTrip(req.WithContext(WithBulk(req.Context())))
	require.NoError(t, err)
	require.NotNil(t, s.bulkPace, "a marked request does")
	// The pings stop when the body is written, not when the response ends: the response body is still open here.
	require.Eventually(t, func() bool {
		s.bulkPace.mu.Lock()
		defer s.bulkPace.mu.Unlock()
		return s.bulkPace.users == 0
	}, 2*time.Second, 5*time.Millisecond, "the pacer is released when the body is written")
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	assert.EqualValues(t, 200<<10, received.Load(), "and the whole body arrives")
}

// BulkMaxRate caps a bulk request's speed whatever the link allows.
func TestBulkMaxRateCapsAPush(t *testing.T) {
	var received atomic.Int64
	hub := newHub(t, SessionConfig{BulkMaxRate: 256 << 10})
	s, _ := connect(t, hub, pushApp(&received), nil)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	pushFiles(ctx, s, 8<<20, &wg, true)
	time.Sleep(500 * time.Millisecond) // past the idle probes and the first burst
	before := received.Load()
	start := time.Now()
	time.Sleep(2 * time.Second)
	rate := float64(received.Load()-before) / time.Since(start).Seconds()
	cancel()
	wg.Wait()
	assert.Less(t, rate, 256.0*1024*1.2, "the push stays at the cap (256 KiB/s)")
	assert.Greater(t, rate, 256.0*1024*0.5, "and gets near it on a link that allows it")
}

// A push that cannot finish in its time at the lowest rate fails at once, with a fixed error, not at the deadline.
func TestBulkPushThatCannotFinishFailsFast(t *testing.T) {
	var received atomic.Int64
	// A cap below the floor stands for a link that holds the push there: 8 MiB at 64 KiB/s is 128 s, the deadline 300 ms.
	hub := newHub(t, SessionConfig{BulkMaxRate: 64 << 10, BulkMaxDuration: 300 * time.Millisecond})
	s, _ := connect(t, hub, pushApp(&received), nil)
	req, _ := http.NewRequest(http.MethodPost, "http://ignored/api/board/team/external-artifacts", &patternBody{left: 8 << 20})
	req.ContentLength = 8 << 20
	start := time.Now()
	_, err := s.RoundTrip(req.WithContext(WithBulk(req.Context())))
	require.ErrorIs(t, err, ErrBulkTooSlow)
	assert.Less(t, time.Since(start), 5*time.Second, "it failed at once, not at the deadline")
	assert.Less(t, received.Load(), int64(1<<20), "and moved almost nothing")
	require.Eventually(t, func() bool {
		s.bulkPace.mu.Lock()
		defer s.bulkPace.mu.Unlock()
		return s.bulkPace.users == 0
	}, 2*time.Second, 5*time.Millisecond, "the slot is released")
}
