package updatecheck

import (
	"context"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/safego"
)

// The server asks GitHub through a cache (#4133, from the #4132 audit): every call to
// GET /api/system/update-check used to make one request to GitHub and hold a handler for up to
// Timeout, so any program that could call the route (an agent's token included) could use up the
// owner's unauthenticated rate limit (60 an hour) and make the real checks fail.
const (
	// CacheOK is how long a good answer is kept.
	CacheOK = time.Hour
	// CacheFail is how long a failure is kept (a rate limit, no network, a reply that isn't a
	// version): short, so a recovered network is noticed soon, but never one request per call.
	CacheFail = 5 * time.Minute
	// RefreshAfter is how old a cached answer must be before a manual "Check for Updates" fetches
	// again; a refresh sooner than this gets the cached answer.
	RefreshAfter = time.Minute
	// cacheMax bounds the entries (one per feed URL: production has one, tests a few).
	cacheMax = 16
)

type cacheEntry struct {
	latest string
	err    error
	at     time.Time
}

type cacheCall struct {
	done   chan struct{}
	latest string
	err    error
}

// Cache answers Latest from memory while the answer is fresh, and lets concurrent callers share
// one request. Safe for concurrent use.
type Cache struct {
	mu      sync.Mutex
	now     func() time.Time
	fetch   func(ctx context.Context, apiURL string) (string, error)
	entries map[string]*cacheEntry
	calls   map[string]*cacheCall
}

// NewCache returns an empty cache that asks GitHub with Latest.
func NewCache() *Cache {
	return &Cache{now: time.Now, fetch: Latest, entries: map[string]*cacheEntry{}, calls: map[string]*cacheCall{}}
}

// Default is the cache the server shares.
var Default = NewCache()

// Latest is the package's Latest, from the cache while it is fresh (CacheOK for a version,
// CacheFail for an error) and with at most one request in flight per URL: callers that arrive
// meanwhile wait for it, each up to its own ctx.
func (c *Cache) Latest(ctx context.Context, apiURL string) (string, error) {
	return c.get(ctx, apiURL, false)
}

// Refresh is Latest for a person's "Check for Updates": it fetches again unless the cached
// answer is under RefreshAfter old, so pressing the button sees a release published minutes
// ago but cannot be used to hammer GitHub.
func (c *Cache) Refresh(ctx context.Context, apiURL string) (string, error) {
	return c.get(ctx, apiURL, true)
}

func (c *Cache) get(ctx context.Context, apiURL string, refresh bool) (string, error) {
	c.mu.Lock()
	if e := c.entries[apiURL]; e != nil {
		age := c.now().Sub(e.at)
		ttl := CacheOK
		if e.err != nil {
			ttl = CacheFail
		}
		if age < ttl && !(refresh && age >= RefreshAfter) {
			c.mu.Unlock()
			return e.latest, e.err
		}
	}
	call := c.calls[apiURL]
	if call == nil {
		call = &cacheCall{done: make(chan struct{})}
		c.calls[apiURL] = call
		safego.Go("updatecheck/fetch", func() {
			// Its own context: a caller that gives up must not cancel the request the others wait for.
			fctx, cancel := context.WithTimeout(context.Background(), Timeout)
			defer cancel()
			call.latest, call.err = c.fetch(fctx, apiURL)
			c.mu.Lock()
			if len(c.entries) >= cacheMax {
				c.entries = map[string]*cacheEntry{}
			}
			c.entries[apiURL] = &cacheEntry{latest: call.latest, err: call.err, at: c.now()}
			delete(c.calls, apiURL)
			c.mu.Unlock()
			close(call.done)
		})
	}
	c.mu.Unlock()
	select {
	case <-call.done:
		return call.latest, call.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
