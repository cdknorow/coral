// Package diskguard says whether the volume Coral stores its data on is nearly full, so the routes that can fill it (agent artifact
// uploads, board posts, a cross-team file) refuse with 507 before a write fails halfway: a full disk makes SQLite fail for every agent
// at once (events and board posts lost) (#4602). The same condition shows to the Operator as the machine-health "busy: disk" status,
// which warns earlier (2 GiB or 5% free); this refuses later (under 1 GiB or 1% free).
package diskguard

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
)

const (
	// MinFree is the free space below which writes that grow without a bound are refused.
	MinFree uint64 = 1 << 30
	// MinFraction is the same threshold as a share of the volume (the larger of the two applies).
	MinFraction = 0.01
	// ttl is how long a reading is reused: the check runs on every upload and post.
	ttl = 10 * time.Second
)

// Guard reads the free space of one folder's volume, at most once per ttl.
type Guard struct {
	dir   string
	usage func(dir string) (free, total uint64, err error)
	now   func() time.Time

	mu    sync.Mutex
	at    time.Time
	low   bool
	free  uint64
	known bool
}

func realUsage(dir string) (uint64, uint64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	u, err := disk.UsageWithContext(ctx, dir)
	if err != nil {
		return 0, 0, err
	}
	return u.Free, u.Total, nil
}

// New is a guard for dir's volume.
func New(dir string) *Guard { return &Guard{dir: dir, usage: realUsage, now: time.Now} }

// Low reports whether the volume is under the threshold, with the free bytes. A volume that can't be read is not low: a failed
// reading must not turn writes off.
func (g *Guard) Low() (bool, uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.known && g.now().Sub(g.at) < ttl {
		return g.low, g.free
	}
	free, total, err := g.usage(g.dir)
	if err != nil || total == 0 {
		g.at, g.known, g.low, g.free = g.now(), true, false, 0
		return false, 0
	}
	need := max(MinFree, uint64(MinFraction*float64(total)))
	g.at, g.known, g.low, g.free = g.now(), true, free < need, free
	return g.low, g.free
}

var current atomic.Pointer[Guard]

// Use makes dir's volume the one Low reports on (called once at startup). Until then nothing is low.
func Use(dir string) {
	if dir == "" {
		current.Store(nil)
		return
	}
	current.Store(New(dir))
}

// Low is the current guard's answer, or false when none is set.
func Low() (bool, uint64) {
	if g := current.Load(); g != nil {
		return g.Low()
	}
	return false, 0
}

// UseForTest installs a guard with a fake reading and returns a function that removes it.
func UseForTest(usage func(dir string) (free, total uint64, err error), now func() time.Time) func() {
	g := &Guard{dir: "test", usage: usage, now: now}
	current.Store(g)
	return func() { current.Store(nil) }
}
