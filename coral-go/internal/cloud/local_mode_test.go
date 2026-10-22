package cloud

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/pkg/tunnel"
)

// #5918 (coral-hub's "follow the machine"): this Coral tells the hub when its own maximum mode changes, by a local_mode/1 message to a hub that advertised it, and by a reconnect (as before)
// otherwise, on any answer but 204, and on a timeout. At most one message a second, the latest value only, an identical one not sent.

type modeLog struct {
	mu sync.Mutex
	in []tunnel.Mode
}

func (l *modeLog) add(m tunnel.Mode) { l.mu.Lock(); l.in = append(l.in, m); l.mu.Unlock() }
func (l *modeLog) all() []tunnel.Mode {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]tunnel.Mode(nil), l.in...)
}

func modeEnv(t *testing.T, gap time.Duration) (*env, *Manager, *modeLog) {
	t.Helper()
	e := newEnv(t)
	log := &modeLog{}
	e.hub.onMode.Store(func(m tunnel.Mode) { log.add(m) })
	m := e.manager(e.hub.srv.URL)
	m.modeGap = gap
	start(t, m)
	require.NoError(t, m.Connect(context.Background()))
	waitState(t, m, StateConnected)
	first := e.hub.session(t)
	require.Equal(t, tunnel.TierView, first.LocalMode())
	require.Eventually(t, func() bool { cs := m.session.Load(); return cs != nil && cs.CanSendLocalMode() }, 5*time.Second, 10*time.Millisecond, "the hub advertised the capability")
	return e, m, log
}

func TestAChangeOfTheLocalModeIsAMessageNotAReconnectForAHubThatAdvertisedIt(t *testing.T) {
	e, m, log := modeEnv(t, 0)
	tunnels := e.hub.tunnels.Load()
	e.mode.Store(int32(tunnel.TierInteract))
	m.ModeChanged(context.Background())
	require.Eventually(t, func() bool { return len(log.all()) == 1 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []tunnel.Mode{tunnel.TierInteract}, log.all())
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, tunnels, e.hub.tunnels.Load(), "no reconnect")
	assert.Equal(t, StateConnected, m.Status().State)
}

// The hub turns the capability on with its own go: until it does, a change is the reconnect it always was.
func TestAHubThatDidNotAdvertiseItStillGetsTheReconnect(t *testing.T) {
	e := newEnv(t)
	m := e.manager(e.hub.srv.URL)
	start(t, m)
	require.NoError(t, m.Connect(context.Background()))
	waitState(t, m, StateConnected)
	first := e.hub.session(t)
	require.Equal(t, tunnel.TierView, first.LocalMode())
	assert.False(t, m.session.Load().CanSendLocalMode())
	e.mode.Store(int32(tunnel.TierInteract))
	m.ModeChanged(context.Background())
	second := e.hub.session(t)
	require.Equal(t, tunnel.TierInteract, second.LocalMode(), "the next Hello carries it")
}

// Any answer but 204 falls back to the reconnect: here the hub's 429 for a second message inside its second.
func TestAnAnswerButNoContentFallsBackToTheReconnect(t *testing.T) {
	e, m, log := modeEnv(t, 0) // this Coral does not hold back, so the hub's one-a-second limit answers 429
	e.mode.Store(int32(tunnel.TierInteract))
	m.ModeChanged(context.Background())
	require.Eventually(t, func() bool { return len(log.all()) == 1 }, 5*time.Second, 10*time.Millisecond)
	e.mode.Store(int32(tunnel.TierLaunch))
	m.ModeChanged(context.Background()) // inside the hub's second: 429
	second := e.hub.session(t)
	require.Equal(t, tunnel.TierLaunch, second.LocalMode(), "reconnected, and the new Hello carries the mode")
	assert.Equal(t, []tunnel.Mode{tunnel.TierInteract}, log.all(), "the refused message was not taken")
	waitState(t, m, StateConnected)
}

// One message a second, the latest value only, an identical one not sent.
func TestMessagesAreHeldToOneASecondWithOnlyTheLatestAndNoRepeats(t *testing.T) {
	e, m, log := modeEnv(t, 300*time.Millisecond)
	tunnels := e.hub.tunnels.Load()
	set := func(md tunnel.Mode) { e.mode.Store(int32(md)); m.ModeChanged(context.Background()) }
	set(tunnel.TierInteract)
	require.Eventually(t, func() bool { return len(log.all()) == 1 }, 5*time.Second, 5*time.Millisecond)
	// two more inside the gap: one is held, the latest wins, and it is back to what the hub has: nothing is sent
	set(tunnel.TierLaunch)
	set(tunnel.TierInteract)
	time.Sleep(700 * time.Millisecond)
	assert.Equal(t, []tunnel.Mode{tunnel.TierInteract}, log.all(), "held, then coalesced away: the hub already had it")
	// one that is not within the gap of the last is sent at once
	set(tunnel.TierLaunch)
	require.Eventually(t, func() bool { return len(log.all()) == 2 }, 5*time.Second, 5*time.Millisecond)
	// three inside the gap: only where they end up is sent, once, after the gap
	set(tunnel.TierView)
	set(tunnel.TierInteract)
	set(tunnel.TierView)
	require.Eventually(t, func() bool { return len(log.all()) == 3 }, 5*time.Second, 5*time.Millisecond)
	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, []tunnel.Mode{tunnel.TierInteract, tunnel.TierLaunch, tunnel.TierView}, log.all(), "the latest value, once")
	assert.Equal(t, tunnels, e.hub.tunnels.Load(), "never a reconnect")
}

// A hub that takes the message but then goes away mid-send: the connector's own timeout is a fall back too (the hook never answers).
func TestATimeoutFallsBackToTheReconnect(t *testing.T) {
	e := newEnv(t)
	release := make(chan struct{})
	e.hub.onMode.Store(func(tunnel.Mode) { <-release })
	t.Cleanup(func() { close(release) })
	m := e.manager(e.hub.srv.URL)
	m.modeGap = 0
	start(t, m)
	require.NoError(t, m.Connect(context.Background()))
	waitState(t, m, StateConnected)
	e.hub.session(t)
	require.Eventually(t, func() bool { cs := m.session.Load(); return cs != nil && cs.CanSendLocalMode() }, 5*time.Second, 10*time.Millisecond)
	e.mode.Store(int32(tunnel.TierInteract))
	m.ModeChanged(context.Background())
	second := e.hub.session(t) // after the 5 s the message gets: reconnected
	require.Equal(t, tunnel.TierInteract, second.LocalMode())
}

// On a lowering this Coral ends its own streams that were allowed more than the new mode BEFORE it tells the hub; a raise ends none.
func TestALoweringEndsTheStreamsAboveTheNewModeBeforeTheHubIsTold(t *testing.T) {
	e, m, log := modeEnv(t, 0)
	var mu sync.Mutex
	var events []string
	m.closeAbove = func(md tunnel.Mode) int {
		mu.Lock()
		events = append(events, "close:"+md.String())
		mu.Unlock()
		return 1
	}
	e.hub.onMode.Store(func(md tunnel.Mode) {
		mu.Lock()
		events = append(events, "told:"+md.String())
		mu.Unlock()
		log.add(md)
	})
	e.mode.Store(int32(tunnel.TierLaunch))
	m.ModeChanged(context.Background()) // a raise (view to launch): nothing to end
	require.Eventually(t, func() bool { return len(log.all()) == 1 }, 5*time.Second, 5*time.Millisecond)
	time.Sleep(150 * time.Millisecond) // the hub's gap
	e.mode.Store(int32(tunnel.TierInteract))
	m.ModeChanged(context.Background()) // a lowering (launch to interact)
	require.Eventually(t, func() bool { return len(log.all()) == 2 }, 5*time.Second, 5*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"told:launch", "close:interact", "told:interact"}, events, "no close on a raise; on a lowering the close comes first")
}

// One message is in flight at a time (Security's note on #5918): a change made while one is being answered waits for it, and only the latest goes out, so the hub never sees two at once and
// never in the wrong order.
func TestAChangeWhileOneIsInFlightWaitsForItAndOnlyTheLatestFollows(t *testing.T) {
	e, m, log := modeEnv(t, 150*time.Millisecond)
	var mu sync.Mutex
	inflight, maxInflight := 0, 0
	release := make(chan struct{})
	first := true
	e.hub.onMode.Store(func(md tunnel.Mode) {
		mu.Lock()
		inflight++
		maxInflight = max(maxInflight, inflight)
		wait := first
		first = false
		mu.Unlock()
		if wait {
			<-release // the first message is not answered yet
		}
		log.add(md)
		mu.Lock()
		inflight--
		mu.Unlock()
	})
	tunnels := e.hub.tunnels.Load()
	set := func(md tunnel.Mode) { e.mode.Store(int32(md)); m.ModeChanged(context.Background()) }
	set(tunnel.TierInteract)
	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return inflight == 1 }, 5*time.Second, 5*time.Millisecond, "the first message is in flight")
	set(tunnel.TierView)   // asked for and then changed again: never sent
	set(tunnel.TierLaunch) // the latest, which is not what the hub has
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	assert.Equal(t, 1, inflight, "no second message while the first is unanswered")
	mu.Unlock()
	assert.Empty(t, log.all())
	close(release)
	require.Eventually(t, func() bool { return len(log.all()) == 2 }, 10*time.Second, 5*time.Millisecond)
	time.Sleep(400 * time.Millisecond)
	assert.Equal(t, []tunnel.Mode{tunnel.TierInteract, tunnel.TierLaunch}, log.all(), "in order, and only the latest of the changes made meanwhile")
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, maxInflight, "never two at once")
	assert.Equal(t, tunnels, e.hub.tunnels.Load(), "no second send reached the hub inside its gap (a 429 would have reconnected)")
}
