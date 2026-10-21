package background

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/subgentic/coral/internal/store"
)

// countingRegistry records every SetStatus write.
type countingRegistry struct {
	*fakeRegistry
	mu     sync.Mutex
	writes []string // "status|msg"
}

func (c *countingRegistry) SetStatus(ctx context.Context, id, status, msg string) error {
	c.mu.Lock()
	c.writes = append(c.writes, status+"|"+msg)
	c.mu.Unlock()
	return c.fakeRegistry.SetStatus(ctx, id, status, msg)
}

func (c *countingRegistry) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	w := c.writes
	c.writes = nil
	return w
}

func newWriteTestHub(t *testing.T, every time.Duration) (*RemoteAgentHub, *countingRegistry) {
	t.Helper()
	old := remoteSeenWriteEvery
	remoteSeenWriteEvery = every
	t.Cleanup(func() { remoteSeenWriteEvery = old })
	reg := &countingRegistry{fakeRegistry: newFakeRegistry()}
	reg.put("a", "http://127.0.0.1:1")
	h := NewRemoteAgentHub(reg, regResolver{reg.fakeRegistry}, RemoteAgentHubConfig{OfflineGrace: -1}) // each failure at once (#3649)
	h.remotes["a"] = &remoteState{srv: store.RemoteServer{ID: "a"}, status: "unknown"}
	return h, reg
}

// An online remote's row is written when it comes online and then refreshed
// (last_seen) at most once per remoteSeenWriteEvery, however often it is
// heard from; session changes alone do not write (#3142).
func TestRemoteStatusWritesAreCoalesced(t *testing.T) {
	h, reg := newWriteTestHub(t, 300*time.Millisecond)
	ctx := context.Background()

	h.recordAlive(ctx, "a")
	assert.Equal(t, []string{"online|"}, reg.take(), "coming online is written at once")
	for i := 0; i < 20; i++ {
		h.recordAlive(ctx, "a")
		h.recordOnline(ctx, "a", []map[string]any{{"session_id": "s", "n": i}}) // a new session list each time
	}
	assert.Empty(t, reg.take(), "heartbeats and session changes within the interval write nothing")

	time.Sleep(350 * time.Millisecond)
	h.recordAlive(ctx, "a")
	h.recordAlive(ctx, "a")
	assert.Equal(t, []string{"online|"}, reg.take(), "last_seen is refreshed once per interval")
}

// Failures: a status change or a different error is written at once; the same
// error repeating is written at most once per interval; recovery is written
// at once.
func TestRemoteFailureWritesAreCoalesced(t *testing.T) {
	h, reg := newWriteTestHub(t, 300*time.Millisecond)
	ctx := context.Background()
	h.recordAlive(ctx, "a")
	reg.take()

	down := errors.New("dial tcp: connection refused")
	h.recordFailure(ctx, "a", down)
	assert.Equal(t, []string{"unreachable|dial tcp: connection refused"}, reg.take(), "going unreachable is written at once")
	for i := 0; i < 10; i++ {
		h.recordFailure(ctx, "a", down)
	}
	assert.Empty(t, reg.take(), "the same error repeating writes nothing within the interval")

	h.recordFailure(ctx, "a", errors.New("context deadline exceeded"))
	assert.Equal(t, []string{"unreachable|context deadline exceeded"}, reg.take(), "a different error is written at once")

	time.Sleep(350 * time.Millisecond)
	h.recordFailure(ctx, "a", errors.New("context deadline exceeded"))
	assert.Len(t, reg.take(), 1, "a repeated error is refreshed once per interval")

	h.recordAlive(ctx, "a")
	assert.Equal(t, []string{"online|"}, reg.take(), "recovery is written at once")
	assert.Equal(t, store.RemoteStatusOnline, reg.statusOf("a"))
}

// The production interval keeps last_seen accurate to about a minute.
func TestRemoteSeenWriteInterval(t *testing.T) {
	assert.Equal(t, time.Minute, remoteSeenWriteEvery)
}

// hookRegistry runs before, once, just before a write of status lands: what
// another goroutine does between ReportFailure's unlock and its row write.
type hookRegistry struct {
	*countingRegistry
	status string
	before func()
}

func (h *hookRegistry) SetStatus(ctx context.Context, id, status, msg string) error {
	if f := h.before; f != nil && status == h.status {
		h.before = nil
		f()
	}
	return h.countingRegistry.SetStatus(ctx, id, status, msg)
}

func (h *hookRegistry) row(id string) string {
	h.fakeRegistry.mu.Lock()
	defer h.fakeRegistry.mu.Unlock()
	return h.fakeRegistry.status[id]
}

// A probe that finishes while a proxied failure is being written (its online
// write lands first, the failure write last) leaves the row saying unreachable.
// The worker must notice and correct the row on its next probe, not on the
// once-a-minute write (#3202 review).
func TestReportFailureRaceWithAProbeIsCorrected(t *testing.T) {
	h, counting := newWriteTestHub(t, time.Hour) // no periodic write to hide behind
	ctx := context.Background()
	reg := &hookRegistry{countingRegistry: counting, status: store.RemoteStatusUnreachable}
	h.reg = reg
	h.remotes["a"] = newRemoteState()
	h.remotes["a"].srv = store.RemoteServer{ID: "a"}
	h.remotes["a"].cancel = func() {} // a worker runs
	h.recordAlive(ctx, "a")
	reg.take()

	// The probe finishes between ReportFailure's unlock and its write.
	reg.before = func() { h.recordAlive(ctx, "a") }
	assert.True(t, h.ReportFailure(ctx, "a", store.RemoteStatusUnreachable, "connect failed"))
	assert.Equal(t, store.RemoteStatusUnreachable, reg.row("a"), "the late write left the row failed")

	h.recordAlive(ctx, "a") // the next probe
	assert.Equal(t, store.RemoteStatusOnline, reg.row("a"), "corrected at once")
}
