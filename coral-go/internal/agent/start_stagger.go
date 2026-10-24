package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	at "github.com/subgentic/coral/internal/agenttypes"
)

// SharedHomeStartGap is how far apart two starts of an agent that keeps its state in one shared home (Codex's SQLite state database) are put. Two Codex runs started at once raced the first one's state
// initialisation and migration: one exited with a "duplicate" error (code 1) before it did anything (Codex Test's #6828). A variable so tests need not wait.
var SharedHomeStartGap = 4 * time.Second

// staggerOn is set by the server at startup (SetStartStagger): off, as in tests and any other caller, nothing waits.
var staggerOn atomic.Bool

// SetStartStagger turns the start spacing on or off. The server turns it on at startup; a test that needs it turns it on and off again.
func SetStartStagger(on bool) { staggerOn.Store(on) }

var startSlots = struct {
	mu   sync.Mutex
	last map[string]time.Time
}{last: map[string]time.Time{}}

// reserve takes agentType's next start slot: the time it may start at, and what to put back if the start is cancelled. ok is false when nothing is staggered (off, another agent, no gap).
func reserve(agentType string) (slot, prev time.Time, had, ok bool) {
	if !staggerOn.Load() || agentType != at.Codex || SharedHomeStartGap <= 0 {
		return time.Time{}, time.Time{}, false, false
	}
	startSlots.mu.Lock()
	defer startSlots.mu.Unlock()
	slot = time.Now()
	prev, had = startSlots.last[agentType]
	if had {
		if next := prev.Add(SharedHomeStartGap); next.After(slot) {
			slot = next
		}
	}
	startSlots.last[agentType] = slot
	return slot, prev, had, true
}

// ReserveStart is the non-blocking form: it takes the next slot and returns how long the start must wait for it (zero: now), without sleeping. A launch request uses it to type the start later and return at
// once, so a Hub viewer's response deadline cannot cut a team launch in half (Security's MUST on #6848). Nothing is given back: the start it reserved for is typed.
func ReserveStart(agentType string) time.Duration {
	slot, _, _, ok := reserve(agentType)
	if !ok {
		return 0
	}
	return max(time.Until(slot), 0)
}

// StaggerStart waits for agentType's next free start slot (at least SharedHomeStartGap after the previous one) and returns when it is reached, or with the context's error when ctx ends first, in which case
// the slot is given back if nobody has reserved a later one. The scheduler's runs use it (a run's own goroutine may wait); a launch request uses ReserveStart. Only agents whose CLI initialises a shared state
// database at start are staggered (Codex); the others return at once (#6848).
func StaggerStart(ctx context.Context, agentType string) error {
	slot, prev, had, ok := reserve(agentType)
	if !ok {
		return nil
	}
	wait := time.Until(slot)
	if wait <= 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		startSlots.mu.Lock()
		if startSlots.last[agentType].Equal(slot) { // nobody reserved after it: the next start need not wait for one that never happens
			if had {
				startSlots.last[agentType] = prev
			} else {
				delete(startSlots.last, agentType)
			}
		}
		startSlots.mu.Unlock()
		return ctx.Err()
	}
}
