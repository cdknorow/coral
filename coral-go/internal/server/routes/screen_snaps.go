package routes

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// screenSnaps lets read-only pollers share one capture of an agent's terminal for
// screenSnapTTL (#3887). The agent poll's 200-line capture was most of Coral's capture-pane
// processes under two dashboard tabs (122 of 175 in 80 s, QA's #3851 census): every tab
// polling an agent captured it separately. Only read-only pollers opt in. Prompt answers,
// pastes and auto-accept read the screen fresh, because they act on what it shows.
type screenSnaps struct {
	mu sync.Mutex
	m  map[string]*screenSnap
}

// screenSnapTTL is how long one capture answers the read-only pollers (a var for tests): the
// dashboard's poll interval, so a poll shows the screen at most one interval older than before.
var screenSnapTTL = 2 * time.Second

// screenSnapTimeout bounds a shared capture. It runs on its own context, not the first poller's
// request: a poller that gives up must not leave the others an empty screen for screenSnapTTL.
var screenSnapTimeout = 2 * time.Second

// screenSnapsMax bounds the map; past it, the map starts over.
const screenSnapsMax = 512

// screenSnapKeep: a capture older than this is dropped when a new one is stored, so a stopped
// session's last screen doesn't stay in memory (Security's note on #3887).
const screenSnapKeep = time.Minute

type screenSnap struct {
	at   time.Time
	done chan struct{}
	out  string
	err  error
}

// capture returns the last lines of the named terminal and when that capture started: a
// capture started in the last screenSnapTTL, or a new one from fetch. One capture at a time per
// terminal; a poller that comes while one is under way waits for it, up to its own ctx. A
// failed capture is not kept.
func (c *screenSnaps) capture(ctx context.Context, name string, lines int, now time.Time, fetch func(context.Context) (string, error)) (string, time.Time, error) {
	key := name + "\x00" + strconv.Itoa(lines)
	c.mu.Lock()
	e, ok := c.m[key]
	if !ok || now.Sub(e.at) >= screenSnapTTL {
		if c.m == nil || len(c.m) >= screenSnapsMax {
			c.m = map[string]*screenSnap{}
		}
		for k, old := range c.m {
			if now.Sub(old.at) >= screenSnapKeep {
				delete(c.m, k)
			}
		}
		e = &screenSnap{at: now, done: make(chan struct{})}
		c.m[key] = e
		go func() {
			fctx, cancel := context.WithTimeout(context.Background(), screenSnapTimeout)
			defer cancel()
			e.out, e.err = fetch(fctx)
			if e.err != nil {
				c.mu.Lock()
				if c.m[key] == e {
					delete(c.m, key)
				}
				c.mu.Unlock()
			}
			close(e.done)
		}()
	}
	c.mu.Unlock()
	select {
	case <-e.done:
		return e.out, e.at, e.err
	case <-ctx.Done():
		return "", time.Time{}, ctx.Err()
	}
}
