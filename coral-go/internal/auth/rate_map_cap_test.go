package auth

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// #4602 L6: the failed-key limiter tracks at most maxRateIPs addresses; expired ones are swept only when the map is full (not on
// every call past 1,000 entries); if it is still full a new address is not counted; wrong keys are refused as ever.
func TestRateMapIsCappedAndSweptOnlyWhenFull(t *testing.T) {
	assert.Equal(t, 10000, maxRateIPs, "the hard cap the audit asked for, like the password throttle's")
	ks := &KeyStore{rateMap: make(map[string]*rateLimitEntry)}
	// 1,500 expired entries stay until the map is full: no sweep per call.
	old := time.Now().Add(-2 * rateLimitWindow)
	for i := 0; i < 1500; i++ {
		ks.rateMap[fmt.Sprintf("old-%d", i)] = &rateLimitEntry{count: 1, windowAt: old}
	}
	assert.True(t, ks.CheckRateLimit("fresh-1"))
	assert.Len(t, ks.rateMap, 1501, "expired entries are not swept on each call")

	// Fill to the cap with live entries: a new address is not tracked.
	ks = &KeyStore{rateMap: make(map[string]*rateLimitEntry)}
	for i := 0; i < maxRateIPs; i++ {
		ks.rateMap[fmt.Sprintf("live-%d", i)] = &rateLimitEntry{count: 1, windowAt: time.Now()}
	}
	assert.True(t, ks.CheckRateLimit("flood-1"), "a flood of addresses is not counted")
	assert.Len(t, ks.rateMap, maxRateIPs, "and the map did not grow")
	assert.False(t, ks.RateLimited("flood-1"))

	// Tracked addresses are still counted and limited at the cap.
	for i := 0; i < rateLimitMax-1; i++ { // it was seeded with one failure
		assert.True(t, ks.CheckRateLimit("live-5"), i)
	}
	assert.False(t, ks.CheckRateLimit("live-5"), "an address already tracked is limited as before")
	assert.True(t, ks.RateLimited("live-5"))

	// Full of expired entries: the sweep makes room for the new address.
	ks = &KeyStore{rateMap: make(map[string]*rateLimitEntry)}
	for i := 0; i < maxRateIPs; i++ {
		ks.rateMap[fmt.Sprintf("stale-%d", i)] = &rateLimitEntry{count: 1, windowAt: old}
	}
	assert.True(t, ks.CheckRateLimit("new-1"))
	assert.Len(t, ks.rateMap, 1, "the expired ones were swept and the new address is tracked")
}
