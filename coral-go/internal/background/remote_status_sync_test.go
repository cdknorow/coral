package background

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
	proxy "github.com/cdknorow/coral/pkg/hubproxy"
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

// A burst of proxied failures does not become a burst of probes.
func TestHub_ProxyFailureBurstIsCoalesced(t *testing.T) {
	a := newFakeRemote(t, sess("one", "a-1"))
	a.noFeed = true
	reg := newRowRegistry()
	reg.put("a", a.URL)
	h := syncHub(reg, 5*time.Second)
	h.cfg.MinBackoff = 200 * time.Millisecond
	startHub(t, h)
	require.Eventually(t, onlineEverywhere(h, reg, "a"), 3*time.Second, 10*time.Millisecond)
	before, _ := a.counts()
	for i := 0; i < 50; i++ {
		h.ReportFailure(context.Background(), "a", store.RemoteStatusUnreachable, "connect failed")
		time.Sleep(10 * time.Millisecond) // sleep-ok: spaces a burst of failures over 0.5 s
	}
	require.Eventually(t, onlineEverywhere(h, reg, "a"), 2*time.Second, 10*time.Millisecond)
	after, _ := a.counts()
	assert.LessOrEqual(t, after-before, 5, "probes stay MinBackoff apart (%d probes for 50 failures in 0.5 s)", after-before)
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
