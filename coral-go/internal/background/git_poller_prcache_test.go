package background

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// #3999 (finding 5): the PR cache sweeps expired entries once it is large, and keeps the live ones.
func TestPRCacheSweepsExpiredEntries(t *testing.T) {
	now := time.Now()
	p := &GitPoller{prCache: map[string]prCacheEntry{}}
	for i := 0; i < prCacheKeysMax+20; i++ {
		p.prCache[fmt.Sprintf("remote::old-%d", i)] = prCacheEntry{expiresAt: now.Add(-time.Hour)}
	}
	p.prCache["remote::fresh"] = prCacheEntry{prNumber: 7, expiresAt: now.Add(time.Hour)}
	p.sweepPRCache(now)
	assert.Len(t, p.prCache, 1)
	assert.Contains(t, p.prCache, "remote::fresh")

	small := &GitPoller{prCache: map[string]prCacheEntry{"remote::x": {expiresAt: now.Add(-time.Hour)}}}
	small.sweepPRCache(now)
	assert.Len(t, small.prCache, 1, "a small cache is left alone")
}
