package routes

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/background"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store/storetest"
)

// countingTerminal can hold session discovery open. Builds are asserted by
// identity (shared *wsTick, local part pointers), not by counting ListSessions:
// other code, such as the session cache pruner, also lists sessions.
type countingTerminal struct {
	*mockSessionTerminal
	lists atomic.Int64
	hold  chan struct{}
}

func (c *countingTerminal) ListSessions(ctx context.Context) ([]ptymanager.PaneInfo, error) {
	c.lists.Add(1)
	if c.hold != nil {
		<-c.hold
	}
	return c.mockSessionTerminal.ListSessions(ctx)
}

// fakeRemoteHub is a RemoteAgentSource with a change generation.
type fakeRemoteHub struct {
	mu        sync.Mutex
	snap      background.RemoteSnapshot
	gen       atomic.Uint64
	snapshots atomic.Int64
}

func (f *fakeRemoteHub) Snapshot() background.RemoteSnapshot {
	f.snapshots.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap
}
func (f *fakeRemoteHub) Subscribe() (<-chan struct{}, func()) { return make(chan struct{}), func() {} }
func (f *fakeRemoteHub) Generation() uint64                   { return f.gen.Load() }
func (f *fakeRemoteHub) set(agents ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snap = background.RemoteSnapshot{Servers: []background.RemoteServerStatus{{ID: "r1", Label: "r1", Status: "online", Agents: len(agents)}}}
	for _, a := range agents {
		f.snap.Sessions = append(f.snap.Sessions, map[string]any{"server": "r1", "session_id": a, "name": a})
	}
}

func newSharedListHandler(t *testing.T) (*SessionsHandler, *countingTerminal) {
	t.Helper()
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	term := &countingTerminal{mockSessionTerminal: newMockTerminal()}
	term.addSession("claude-11111111-2222-3333-4444-555555555555", "/tmp/a")
	term.addSession("claude-66666666-2222-3333-4444-555555555555", "/tmp/b")
	return NewSessionsHandler(db, &config.Config{LogDir: t.TempDir()}, nil, term, nil), term
}

func TestSharedWSTick_ConcurrentClientsShareOneBuild(t *testing.T) {
	h, term := newSharedListHandler(t)
	term.hold = make(chan struct{})
	req := httptest.NewRequest("GET", "/ws/coral", nil)

	const clients = 20
	ticks := make([]*wsTick, clients)
	var wg sync.WaitGroup
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tick, err := h.sharedWSTick(req)
			assert.NoError(t, err)
			ticks[i] = tick
		}(i)
	}
	require.Eventually(t, func() bool { return term.lists.Load() >= 1 }, time.Second, time.Millisecond)
	time.Sleep(20 * time.Millisecond) // let the other clients queue behind the build
	close(term.hold)
	wg.Wait()
	for _, tick := range ticks { // one build: every client got the same tick
		require.Len(t, tick.sessions, 2)
		require.Len(t, tick.current, 2)
		assert.Same(t, ticks[0], tick, "every client reads the same serialized tick")
	}
	// The serialized form is exactly the entry it stands for.
	for key, s := range ticks[0].byKey {
		b, err := json.Marshal(s)
		require.NoError(t, err)
		assert.Equal(t, string(b), ticks[0].current[key])
	}
}

func TestSharedWSTick_RebuildsWhenStale(t *testing.T) {
	h, _ := newSharedListHandler(t)
	req := httptest.NewRequest("GET", "/ws/coral", nil)
	_, err := h.sharedWSTick(req)
	require.NoError(t, err)
	first := h.wsList.local
	_, err = h.sharedWSTick(req)
	require.NoError(t, err)
	assert.Same(t, first, h.wsList.local, "fresh build is reused")

	h.wsList.mu.Lock()
	h.wsList.local.builtAt = time.Now().Add(-2 * wsListMaxAge())
	h.wsList.mu.Unlock()
	_, err = h.sharedWSTick(req)
	require.NoError(t, err)
	assert.NotSame(t, first, h.wsList.local, "stale build is redone")
}

// A remote change (generation bump) is picked up at once, without rebuilding
// the local part; a change the hub does not signal still shows within the
// age bound.
func TestSharedWSTick_RemoteChanges(t *testing.T) {
	h, _ := newSharedListHandler(t)
	hub := &fakeRemoteHub{}
	hub.set("remote-a")
	h.SetRemoteAgents(hub)
	req := httptest.NewRequest("GET", "/ws/coral", nil)

	tick, err := h.sharedWSTick(req)
	require.NoError(t, err)
	require.True(t, tick.hasRemotes)
	require.Len(t, tick.sessions, 3)
	for _, s := range tick.sessions[:2] {
		assert.Equal(t, "local", s["server"], "local entries are tagged once remotes exist")
	}
	assert.Contains(t, tick.current, remoteKey("r1", "remote-a"))
	local := h.wsList.local

	hub.set("remote-a", "remote-b")
	hub.gen.Add(1)
	tick, err = h.sharedWSTick(req)
	require.NoError(t, err)
	assert.Contains(t, tick.current, remoteKey("r1", "remote-b"), "signalled change is visible immediately")
	assert.Same(t, local, h.wsList.local, "local part reused")

	hub.set("remote-c") // no generation bump
	tick, err = h.sharedWSTick(req)
	require.NoError(t, err)
	assert.NotContains(t, tick.current, remoteKey("r1", "remote-c"), "within the age bound the shared part is reused")
	h.wsList.mu.Lock()
	h.wsList.remote.builtAt = time.Now().Add(-2 * wsListMaxAge())
	h.wsList.mu.Unlock()
	tick, err = h.sharedWSTick(req)
	require.NoError(t, err)
	assert.Contains(t, tick.current, remoteKey("r1", "remote-c"), "unsignalled change shows once the part ages out")
}

// A client that goes away while waiting does not disturb the build or the
// clients still waiting for it.
func TestSharedWSTick_WaiterCanLeave(t *testing.T) {
	h, term := newSharedListHandler(t)
	term.hold = make(chan struct{})
	builder := make(chan error, 1)
	go func() {
		_, err := h.sharedWSTick(httptest.NewRequest("GET", "/ws/coral", nil))
		builder <- err
	}()
	require.Eventually(t, func() bool { return term.lists.Load() >= 1 }, time.Second, time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	waiter := make(chan error, 1)
	go func() {
		_, err := h.sharedWSTick(httptest.NewRequest("GET", "/ws/coral", nil).WithContext(ctx))
		waiter <- err
	}()
	cancel()
	assert.ErrorIs(t, <-waiter, context.Canceled)

	close(term.hold)
	require.NoError(t, <-builder)
}

// panickyRemoteHub panics in Snapshot while armed, optionally after a gate.
type panickyRemoteHub struct {
	fakeRemoteHub
	armed    atomic.Bool
	gate     chan struct{}
	entered  chan struct{} // closed when an armed Snapshot has started (nil: not signalled)
	enterOne sync.Once
	gens     atomic.Int64 // Generation calls: one per sharedWSTick loop, so a queued caller shows
}

func (p *panickyRemoteHub) Generation() uint64 {
	p.gens.Add(1)
	return p.fakeRemoteHub.Generation()
}

func (p *panickyRemoteHub) Snapshot() background.RemoteSnapshot {
	if p.armed.Load() {
		if p.entered != nil {
			p.enterOne.Do(func() { close(p.entered) })
		}
		if p.gate != nil {
			<-p.gate
		}
		panic("remote snapshot exploded")
	}
	return p.fakeRemoteHub.Snapshot()
}

// A panic during the shared build must not wedge /ws/coral: the panicking
// call returns an error, a connection waiting on that build wakes up, and the
// next tick builds normally.
func TestSharedWSTick_PanicDuringBuildDoesNotWedge(t *testing.T) {
	h, _ := newSharedListHandler(t)
	hub := &panickyRemoteHub{gate: make(chan struct{}), entered: make(chan struct{})}
	hub.set("remote-a")
	hub.armed.Store(true)
	h.SetRemoteAgents(hub)
	req := httptest.NewRequest("GET", "/ws/coral", nil)

	builder := make(chan error, 1)
	go func() {
		_, err := h.sharedWSTick(req)
		builder <- err
	}()
	// The build is inside Snapshot, held at the gate: it has read armed, so disarming now only
	// affects the retry (disarming earlier could let the first build succeed under load).
	select {
	case <-hub.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the build never reached the remote snapshot")
	}
	hub.armed.Store(false) // the retry after wake-up succeeds

	// A second connection queues behind the build that is about to panic.
	waiter := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := h.sharedWSTick(req.WithContext(ctx))
		waiter <- err
	}()
	// Its first loop's generation read comes right before it takes the lock and waits on the build.
	require.Eventually(t, func() bool { return hub.gens.Load() >= 2 }, 30*time.Second, time.Millisecond)
	close(hub.gate)

	require.ErrorContains(t, <-builder, "panicked")
	require.NoError(t, <-waiter, "a waiting connection is released and builds")
	tick, err := h.sharedWSTick(req)
	require.NoError(t, err)
	assert.Contains(t, tick.current, remoteKey("r1", "remote-a"))
}
