package auth

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #4964: failed-key attempts are counted per IPv4 address and per IPv6 /64, so a client that owns a /64 does not get a budget (and a place in the
// limiter's map) for each of its 2^64 addresses.
func TestFailedKeyLimiterCountsAnIPv6NetworkOnce(t *testing.T) {
	assert.Equal(t, "203.0.113.7", rateKey("203.0.113.7"))
	assert.Equal(t, "2001:db8:1:2::/64", rateKey("2001:db8:1:2:aaaa:bbbb:cccc:dddd"))
	assert.Equal(t, rateKey("2001:db8:1:2::1"), rateKey("2001:db8:1:2:ffff::9"), "one /64")
	assert.NotEqual(t, rateKey("2001:db8:1:2::1"), rateKey("2001:db8:1:3::1"), "another /64")
	assert.Equal(t, "fe80::/64", rateKey("fe80::1%en0"), "a zone is not part of the address")
	assert.Equal(t, "::ffff:203.0.113.7", rateKey("::ffff:203.0.113.7"), "an IPv4-mapped address is left as it is")
	assert.Equal(t, "not-an-ip", rateKey("not-an-ip"))

	ks := &KeyStore{rateMap: make(map[string]*rateLimitEntry)}
	for i := 0; i < rateLimitMax; i++ {
		assert.True(t, ks.CheckRateLimit(fmt.Sprintf("2001:db8:9:9::%x", i+1)), i)
	}
	assert.False(t, ks.CheckRateLimit("2001:db8:9:9::ff"), "the 11th failure from the same /64 is limited, whatever the address")
	assert.True(t, ks.RateLimited("2001:db8:9:9:1:2:3:4"))
	assert.Len(t, ks.rateMap, 1, "one place in the map for the whole network")
}
