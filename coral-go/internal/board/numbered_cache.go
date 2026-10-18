package board

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// numberedCache remembers each board's numbered agent names (numberedNames) between reads. Every
// ReadMessages, unread count and wait check asked the database for the board's whole subscriber
// list to find them, so the cost grew with agents times reads (#4598: 3% of the server's CPU at
// 100 mock agents on a busy board). The names change only when the board's active subscribers do:
// Subscribe, Unsubscribe and deleting a board bump subGen, and an entry is used only while it
// carries the current generation (read before the query, so a write that lands meanwhile makes the
// entry stale at once). numberedMaxAge bounds the staleness a writer that missed the bump would cause.
type numberedCache struct {
	gen     atomic.Uint64
	mu      sync.Mutex
	entries map[string]numberedEntry
}

type numberedEntry struct {
	gen   uint64
	at    time.Time
	names map[string]bool // read-only: containsExplicitTag only looks names up
}

const (
	numberedMaxAge     = 30 * time.Second
	numberedCacheLimit = 1024 // boards; the map is dropped when full
)

// subscribersChanged marks every board's cached numbered names stale.
func (s *Store) subscribersChanged() { s.numbered.gen.Add(1) }

// numberedNamesCached is numberedNames on the reader pool through the cache. The returned map is
// shared: callers only read it.
func (s *Store) numberedNamesCached(ctx context.Context, project string) map[string]bool {
	c := &s.numbered
	gen := c.gen.Load()
	now := time.Now()
	c.mu.Lock()
	e, ok := c.entries[project]
	c.mu.Unlock()
	if ok && e.gen == gen && now.Sub(e.at) < numberedMaxAge {
		return e.names
	}
	names, err := s.numberedNamesErr(ctx, s.reader(), project)
	if err != nil {
		return nil // as numberedNames: no names; nothing is kept
	}
	c.mu.Lock()
	if c.entries == nil || len(c.entries) >= numberedCacheLimit {
		c.entries = make(map[string]numberedEntry)
	}
	c.entries[project] = numberedEntry{gen: gen, at: now, names: names}
	c.mu.Unlock()
	return names
}
