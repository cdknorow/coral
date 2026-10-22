package background

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
	proxy "github.com/subgentic/coral/pkg/hubproxy"
)

// rowRegistry behaves like the remote_servers table: List returns the status
// last written by anyone, the worker or another writer (Test, the proxy).
type rowRegistry struct {
	mu     sync.Mutex
	rows   map[string]store.RemoteServer
	keyBad map[string]bool
}

func newRowRegistry() *rowRegistry {
	return &rowRegistry{rows: map[string]store.RemoteServer{}, keyBad: map[string]bool{}}
}

func (r *rowRegistry) put(id, url string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows[id] = store.RemoteServer{ID: id, Label: id, URL: url, AllowPrivate: true, Status: store.RemoteStatusUnknown}
}

func (r *rowRegistry) List(context.Context) ([]store.RemoteServer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []store.RemoteServer
	for _, s := range r.rows {
		out = append(out, s)
	}
	return out, nil
}

func (r *rowRegistry) SetStatus(_ context.Context, id, status, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.rows[id]; ok {
		s.Status = status
		r.rows[id] = s
	}
	return nil
}

func (r *rowRegistry) row(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rows[id].Status
}

func (r *rowRegistry) setKeyBad(id string, bad bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keyBad[id] = bad
}

type rowResolver struct{ r *rowRegistry }

func (rr rowResolver) Resolve(_ context.Context, id string) (proxy.Target, error) {
	rr.r.mu.Lock()
	defer rr.r.mu.Unlock()
	s, ok := rr.r.rows[id]
	if !ok {
		return proxy.HTTPTarget{}, proxy.ErrUnknownServer
	}
	if rr.r.keyBad[id] {
		return proxy.HTTPTarget{}, proxy.ErrKeyUnreadable
	}
	return proxy.HTTPTarget{URL: s.URL, APIKey: testKey, AllowPrivate: true}, nil
}

// syncHub polls rarely (pollEvery), so a recovery faster than that can only
// come from the immediate re-probe or the row check, never from the schedule.
func syncHub(reg *rowRegistry, pollEvery time.Duration) *RemoteAgentHub {
	return NewRemoteAgentHub(reg, rowResolver{reg}, RemoteAgentHubConfig{
		PollInterval: pollEvery, RequestTimeout: 300 * time.Millisecond,
		RegistryRefresh: 50 * time.Millisecond, MinBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond,
		OfflineGrace: -1, // each failure's status at once (#3649 tests the grace on its own)
	})
}

func onlineEverywhere(h *RemoteAgentHub, reg *rowRegistry, id string) func() bool {
	return func() bool {
		return statusMap(h.Snapshot())[id] == store.RemoteStatusOnline && reg.row(id) == store.RemoteStatusOnline
	}
}

// QA's repro (#3202): one proxied request fails during a short blip while the
// worker's own probes succeed. The row the Servers page shows must not stay
// unreachable until the once-a-minute write or a Test click: the failure goes
// through the worker, which probes again at once.
func TestHub_ProxyFailureReprobesAtOnce(t *testing.T) {
	require.Equal(t, time.Minute, remoteSeenWriteEvery, "the periodic write this test must not rely on")
	a := newFakeRemote(t, sess("one", "a-1"))
	a.noFeed = true
	reg := newRowRegistry()
	reg.put("a", a.URL)
	const pollEvery = 5 * time.Second
	h := syncHub(reg, pollEvery)
	startHub(t, h)
	require.Eventually(t, onlineEverywhere(h, reg, "a"), 3*time.Second, 10*time.Millisecond)
	before, _ := a.counts()

	start := time.Now()
	require.True(t, h.ReportFailure(context.Background(), "a", store.RemoteStatusUnreachable, "connect failed"))
	// The worker and the row agree on the failure at once.
	assert.Equal(t, store.RemoteStatusUnreachable, statusMap(h.Snapshot())["a"])
	assert.Equal(t, store.RemoteStatusUnreachable, reg.row("a"))

	require.Eventually(t, onlineEverywhere(h, reg, "a"), 2*time.Second, 10*time.Millisecond,
		"a remote that answers is back online within one probe")
	assert.Less(t, time.Since(start), pollEvery, "recovered by the immediate re-probe, not the poll schedule")
	after, _ := a.counts()
	assert.Greater(t, after, before, "the worker probed the remote again")
}

// manualClock is the hub's clock for a test: time moves only when the test advances it, so a burst is spaced
// by what the test says, not by how fast the machine runs.
type manualClock struct {
	mu      sync.Mutex
	t       time.Time
	waiters []*clockWaiter
}

type clockWaiter struct {
	at time.Time
	ch chan struct{}
}

func newManualClock() *manualClock { return &manualClock{t: time.Unix(1_700_000_000, 0)} }

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Sleep blocks until the clock reaches now+d (or ctx ends); a duration that has already passed returns at once.
func (c *manualClock) Sleep(ctx context.Context, d time.Duration) bool {
	c.mu.Lock()
	if d <= 0 {
		c.mu.Unlock()
		return ctx.Err() == nil
	}
	w := &clockWaiter{at: c.t.Add(d), ch: make(chan struct{})}
	c.waiters = append(c.waiters, w)
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return false
	case <-w.ch:
		return true
	}
}

// Sleepers is how many callers are blocked in Sleep.
func (c *manualClock) Sleepers() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}

// Advance moves the clock and releases every sleeper whose time has come.
func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
	kept := c.waiters[:0]
	for _, w := range c.waiters {
		if !c.t.Before(w.at) {
			close(w.ch)
		} else {
			kept = append(kept, w)
		}
	}
	c.waiters = kept
}

// A burst of proxied failures does not become a burst of probes: with the clock held still, the worker
// waits out MinBackoff after its last probe whatever the number of failures, and one probe follows when the
// clock reaches it (a manual clock, so the burst's length and the machine's speed cannot change the count).
func TestHub_ProxyFailureBurstIsCoalesced(t *testing.T) {
	a := newFakeRemote(t, sess("one", "a-1"))
	a.noFeed = true
	reg := newRowRegistry()
	reg.put("a", a.URL)
	h := syncHub(reg, 5*time.Second)
	const minBackoff = 200 * time.Millisecond
	h.cfg.MinBackoff = minBackoff
	clock := newManualClock()
	h.now, h.sleep = clock.Now, clock.Sleep
	startHub(t, h)
	require.Eventually(t, onlineEverywhere(h, reg, "a"), 3*time.Second, 10*time.Millisecond)
	before, _ := a.counts()

	// One failure wakes the worker; it takes that wake and holds off until MinBackoff has passed on the clock. Waiting for that event (the worker asleep on the clock) is what makes the rest of the
	// burst arrive while it holds off: sent all at once, on a slow machine the worker could take its first wake only after the whole burst, and there would be no second wake to coalesce (#6076).
	h.ReportFailure(context.Background(), "a", store.RemoteStatusUnreachable, "connect failed")
	require.Eventually(t, func() bool { return clock.Sleepers() == 1 }, 3*time.Second, time.Millisecond, "the worker waits out MinBackoff")
	for i := 0; i < 49; i++ {
		h.ReportFailure(context.Background(), "a", store.RemoteStatusUnreachable, "connect failed")
	}
	afterBurst, _ := a.counts()
	assert.Equal(t, before, afterBurst, "no probe while MinBackoff has not passed, however many failures")

	// MinBackoff passes: exactly one probe for the whole burst (the 49 other wakes were coalesced into one).
	clock.Advance(minBackoff)
	require.Eventually(t, func() bool { n, _ := a.counts(); return n == before+1 }, 3*time.Second, time.Millisecond, "one probe once MinBackoff passed")
	require.Eventually(t, onlineEverywhere(h, reg, "a"), 3*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return clock.Sleepers() == 1 }, 3*time.Second, time.Millisecond, "the coalesced wake holds off again, it does not probe at once")
	after, _ := a.counts()
	assert.Equal(t, before+1, after, "50 failures made one probe, then waited MinBackoff again (%d probes)", after-before)
}

// Another writer (Test, or a proxy without the hub) leaves a different status
// in the row while the worker sees the remote online: the worker's next
// successful probe rewrites the row, without waiting for the periodic write.
func TestHub_RowWrittenByAnotherWriterIsCorrected(t *testing.T) {
	a := newFakeRemote(t, sess("one", "a-1"))
	a.noFeed = true
	reg := newRowRegistry()
	reg.put("a", a.URL)
	h := syncHub(reg, 150*time.Millisecond)
	startHub(t, h)
	require.Eventually(t, onlineEverywhere(h, reg, "a"), 3*time.Second, 10*time.Millisecond)

	require.NoError(t, reg.SetStatus(context.Background(), "a", store.RemoteStatusUnreachable, "connect failed"))
	require.Eventually(t, func() bool { return reg.row("a") == store.RemoteStatusOnline }, 2*time.Second, 10*time.Millisecond,
		"the row agrees with the worker again within a poll or two, not after remoteSeenWriteEvery")
}

func workerRunning(h *RemoteAgentHub, id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	st, ok := h.current(id)
	return ok && st.cancel != nil
}

// A key_unreadable status in the row that the worker did not write (the proxy
// saw a transient decrypt failure) must not stop the worker: reconcile
// re-checks the key, and a readable key keeps the worker running, so its next
// probe rewrites the row. Polls are 1 s apart, so several reconciles (every
// 50 ms) see the row before the worker's next probe can fix it.
func TestHub_ProxyKeyUnreadableDoesNotStopWorker(t *testing.T) {
	a := newFakeRemote(t, sess("one", "a-1"))
	a.noFeed = true
	reg := newRowRegistry()
	reg.put("a", a.URL)
	h := syncHub(reg, time.Second)
	startHub(t, h)
	require.Eventually(t, onlineEverywhere(h, reg, "a"), 3*time.Second, 10*time.Millisecond)

	require.NoError(t, reg.SetStatus(context.Background(), "a", store.RemoteStatusKeyUnreadable, "stored key cannot be decrypted"))
	time.Sleep(300 * time.Millisecond) // sleep-ok: proves a negative, six reconciles see the row
	assert.True(t, workerRunning(h, "a"), "a readable key keeps the worker running")
	require.Eventually(t, onlineEverywhere(h, reg, "a"), 3*time.Second, 10*time.Millisecond)
}

// A worker stopped because the key really was unreadable starts again once the
// key reads, with no Test click.
func TestHub_UnreadableKeyRecoversWithoutTest(t *testing.T) {
	a := newFakeRemote(t, sess("one", "a-1"))
	a.noFeed = true
	reg := newRowRegistry()
	reg.put("a", a.URL)
	reg.setKeyBad("a", true)
	h := syncHub(reg, 100*time.Millisecond)
	startHub(t, h)
	require.Eventually(t, func() bool {
		return reg.row("a") == store.RemoteStatusKeyUnreadable && !workerRunning(h, "a")
	}, 3*time.Second, 10*time.Millisecond, "an unreadable key stops the worker")
	assert.Equal(t, store.RemoteStatusKeyUnreadable, statusMap(h.Snapshot())["a"])

	reg.setKeyBad("a", false)
	require.Eventually(t, onlineEverywhere(h, reg, "a"), 3*time.Second, 10*time.Millisecond,
		"the worker restarts once the key can be read")
}

// The proxy writes the row itself when no worker runs for the server.
func TestHub_ReportFailureWithoutWorker(t *testing.T) {
	reg := newRowRegistry()
	h := syncHub(reg, time.Second)
	assert.False(t, h.ReportFailure(context.Background(), "nope", store.RemoteStatusUnreachable, "connect failed"))
}
