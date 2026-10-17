package routes

import (
	"sync"
	"time"
)

// chatMeta is what a chat request needs from a live session's record: its type, its
// working directory, its id and the session it was resumed from.
type chatMeta struct {
	agentType, workingDir, sessionID, resumeFromID, agentName string
	at                                                        time.Time
}

// chatMetaCache keeps it in memory (#3438). A chat through Coral Hub leaves out the
// working directory (the hub refuses absolute paths), so without this every poll (about
// one a second per open chat) asked the sessions database, whose single connection it
// shares with every background writer: under load the polls queued behind them. The
// fields never change for a session id, so a short lifetime only bounds staleness for a
// name that moves to another session.
type chatMetaCache struct {
	mu      sync.Mutex
	entries map[string]chatMeta
	ttl     time.Duration
	max     int
	now     func() time.Time
}

func newChatMetaCache() *chatMetaCache {
	return &chatMetaCache{entries: map[string]chatMeta{}, ttl: time.Minute, max: 1024, now: time.Now}
}

func (c *chatMetaCache) get(key string) (chatMeta, bool) {
	if c == nil || key == "" {
		return chatMeta{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	m, ok := c.entries[key]
	if !ok || c.now().Sub(m.at) > c.ttl {
		return chatMeta{}, false
	}
	return m, true
}

func (c *chatMetaCache) put(m chatMeta, keys ...string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	m.at = c.now()
	for _, k := range keys {
		if k == "" {
			continue
		}
		if _, ok := c.entries[k]; !ok && len(c.entries) >= c.max {
			c.evictLocked()
		}
		c.entries[k] = m
	}
}

// evictLocked drops the expired entries, or else the oldest one.
func (c *chatMetaCache) evictLocked() {
	now := c.now()
	oldestKey, oldest := "", now
	for k, m := range c.entries {
		if now.Sub(m.at) > c.ttl {
			delete(c.entries, k)
			continue
		}
		if m.at.Before(oldest) {
			oldestKey, oldest = k, m.at
		}
	}
	if len(c.entries) >= c.max && oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

// forget drops every entry for the given session ids or names: the entry under that key,
// and any entry (under a name) that points at that session. Called when a session is
// unregistered, replaced (restart, resume) or registered under a name another session had.
func (c *chatMetaCache) forget(keys ...string) {
	if c == nil {
		return
	}
	drop := map[string]bool{}
	for _, k := range keys {
		if k != "" {
			drop[k] = true
		}
	}
	if len(drop) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, m := range c.entries {
		if drop[k] || drop[m.sessionID] {
			delete(c.entries, k)
		}
	}
}
