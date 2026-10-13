package routes

import (
	"sync"
	"time"
)

// wsKickCoalesce is how long a /ws/coral connection waits after a kick before
// rebuilding, so a burst of state changes (Sleep All over many agents, a team
// wake) costs one build instead of one per change. Connections that are kicked
// together still share that build through wsTickCache.
var wsKickCoalesce = 50 * time.Millisecond

// wsKicker wakes every /ws/coral connection after a state change, so the feed
// does not lag the session list by up to a poll interval. Each subscriber has a
// one-slot channel: kicks that arrive while one is pending collapse into it.
type wsKicker struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func (k *wsKicker) subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	k.mu.Lock()
	if k.subs == nil {
		k.subs = make(map[chan struct{}]struct{})
	}
	k.subs[ch] = struct{}{}
	k.mu.Unlock()
	return ch, func() {
		k.mu.Lock()
		delete(k.subs, ch)
		k.mu.Unlock()
	}
}

func (k *wsKicker) kick() {
	k.mu.Lock()
	defer k.mu.Unlock()
	for ch := range k.subs {
		select {
		case ch <- struct{}{}:
		default: // a kick is already pending for this connection
		}
	}
}

// kickWS asks every /ws/coral connection (dashboards and hub feeds) for a
// prompt rebuild and coral_diff after a state change: sleep, wake, archive,
// unarchive, restart, kill, launch. The shared local part is marked stale first,
// so the rebuild cannot reuse a list taken before the change. It never blocks.
func (h *SessionsHandler) kickWS() {
	h.wsList.mu.Lock()
	h.wsList.kickGen++
	h.wsList.mu.Unlock()
	h.wsKick.kick()
}
