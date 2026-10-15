package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3594 (d): a request from this computer may name this server only: localhost, a loopback
// address, or an address Coral listens on. Any other IP is refused like an external domain,
// unless Coral listens on every interface.
func TestAcceptedLocalHost(t *testing.T) {
	t.Cleanup(func() { acceptedHosts.Store(nil) })

	acceptedHosts.Store(nil) // before startup sets it: as before, any IP
	for _, h := range []string{"localhost", "127.0.0.1:8420", "[::1]:8420", "10.9.9.9", "100.64.0.1:8420"} {
		assert.True(t, AcceptedLocalHost(h), h)
	}
	assert.False(t, AcceptedLocalHost("evil.example:8420"))

	SetListenHosts([]string{"127.0.0.1", "100.64.0.1", "myhost.local", "[fd7a:115c::1]"})
	for _, h := range []string{"localhost", "LOCALHOST:8420", "127.0.0.1:8420", "127.0.0.2", "[::1]:8420", "100.64.0.1", "100.64.0.1:8420", "myhost.local:8420", "[fd7a:115c::1]:8420", "[FD7A:115C:0::1]:8420"} {
		assert.True(t, AcceptedLocalHost(h), h)
	}
	for _, h := range []string{"10.9.9.9", "10.9.9.9:8420", "100.64.0.2:8420", "evil.example", "myhost.local.evil.example", "", ":8420"} {
		assert.False(t, AcceptedLocalHost(h), h)
	}
	assert.True(t, isValidLocalhostHost(""), "an empty Host (HTTP/1.0) still passes the middleware's check")

	SetListenHosts([]string{"0.0.0.0"}) // every interface: any IP may be this server's
	assert.True(t, AcceptedLocalHost("10.9.9.9:8420"))
	assert.False(t, AcceptedLocalHost("evil.example"))
	SetListenHosts([]string{""})
	assert.True(t, AcceptedLocalHost("10.9.9.9"))
}
