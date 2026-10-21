package updatecheck

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/safego"
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

// entry is one cached answer.
type entry[T any] struct {
	val T
	err error
	at  time.Time
}

type call[T any] struct {
	done chan struct{}
	val  T
	err  error
}

// memo answers from memory while the answer is fresh, and lets concurrent callers share one request, one per key. Safe for concurrent use.
type memo[T any] struct {
	mu      sync.Mutex
	now     func() time.Time
	fetch   func(ctx context.Context, key string) (T, error)
	entries map[string]*entry[T]
	calls   map[string]*call[T]
}

func newMemo[T any](now func() time.Time, fetch func(ctx context.Context, key string) (T, error)) *memo[T] {
	return &memo[T]{now: now, fetch: fetch, entries: map[string]*entry[T]{}, calls: map[string]*call[T]{}}
}

// peek is the fresh cached answer for key, if there is one: it never asks.
func (m *memo[T]) peek(key string) (val T, err error, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.entries[key]; e != nil {
		ttl := CacheOK
		if e.err != nil {
			ttl = CacheFail
		}
		if m.now().Sub(e.at) < ttl {
			return e.val, e.err, true
		}
	}
	var zero T
	return zero, nil, false
}

func (m *memo[T]) get(ctx context.Context, key string, refresh bool) (T, error) {
	m.mu.Lock()
	if e := m.entries[key]; e != nil {
		age := m.now().Sub(e.at)
		ttl := CacheOK
		if e.err != nil {
			ttl = CacheFail
		}
		if age < ttl && !(refresh && age >= RefreshAfter) {
			m.mu.Unlock()
			return e.val, e.err
		}
	}
	c := m.calls[key]
	if c == nil {
		c = &call[T]{done: make(chan struct{})}
		m.calls[key] = c
		safego.Go("updatecheck/fetch", func() {
			// Its own context: a caller that gives up must not cancel the request the others wait for.
			fctx, cancel := context.WithTimeout(context.Background(), Timeout)
			defer cancel()
			c.val, c.err = m.fetch(fctx, key)
			m.mu.Lock()
			if len(m.entries) >= cacheMax {
				m.entries = map[string]*entry[T]{}
			}
			m.entries[key] = &entry[T]{val: c.val, err: c.err, at: m.now()}
			delete(m.calls, key)
			m.mu.Unlock()
			close(c.done)
		})
	}
	m.mu.Unlock()
	select {
	case <-c.done:
		return c.val, c.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

// Cache answers Latest and Feed from memory while the answer is fresh, and lets concurrent callers share one request. Safe for concurrent use.
type Cache struct {
	now    func() time.Time
	latest *memo[string]
	feeds  *memo[*Feed]
}

// NewCache returns an empty cache that asks GitHub with Latest and FetchFeed.
func NewCache() *Cache {
	c := &Cache{now: time.Now}
	clock := func() time.Time { return c.now() }
	c.latest = newMemo(clock, Latest)
	c.feeds = newMemo(clock, func(ctx context.Context, key string) (*Feed, error) {
		ch, url, _ := strings.Cut(key, "|")
		return FetchFeed(ctx, Channel(ch), url, c.now())
	})
	return c
}

// Default is the cache the server shares.
var Default = NewCache()

// Latest is the package's Latest, from the cache while it is fresh (CacheOK for a version,
// CacheFail for an error) and with at most one request in flight per URL: callers that arrive
// meanwhile wait for it, each up to its own ctx.
func (c *Cache) Latest(ctx context.Context, apiURL string) (string, error) {
	return c.latest.get(ctx, apiURL, false)
}

// Refresh is Latest for a person's "Check for Updates": it fetches again unless the cached
// answer is under RefreshAfter old, so pressing the button sees a release published minutes
// ago but cannot be used to hammer GitHub.
func (c *Cache) Refresh(ctx context.Context, apiURL string) (string, error) {
	return c.latest.get(ctx, apiURL, true)
}

// feedKey is one cache entry per channel and feed URL, so the two channels never share an answer and a change of channel needs no flush.
func feedKey(ch Channel, f Feeds) string { return string(ch) + "|" + f.url(ch) }

// Feed is the channel's feed, from the cache while it is fresh (an hour; a failure five minutes), one request in flight per feed. refresh asks again
// when the cached answer is over RefreshAfter old (the dashboard's "Check for Updates").
func (c *Cache) Feed(ctx context.Context, ch Channel, f Feeds, refresh bool) (*Feed, error) {
	return c.feeds.get(ctx, feedKey(ch, f), refresh)
}

// PeekFeed is the fresh cached feed, or ok false when there is none: it never asks. A status screen that must not wait for GitHub uses it with WarmFeed.
func (c *Cache) PeekFeed(ch Channel, f Feeds) (feed *Feed, err error, ok bool) {
	return c.feeds.peek(feedKey(ch, f))
}

// WarmFeed starts the request for the channel's feed in the background when the cached answer is not fresh, so the next PeekFeed has one.
func (c *Cache) WarmFeed(ch Channel, f Feeds) {
	if _, _, ok := c.PeekFeed(ch, f); ok {
		return
	}
	safego.Go("updatecheck/warm", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*Timeout)
		defer cancel()
		_, _ = c.Feed(ctx, ch, f, false)
	})
}
