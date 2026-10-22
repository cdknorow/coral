package auth

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// #6103: the listener proof answers only this computer: loopback, or one of its own interface addresses; never a host on the network, and never a request a proxy forwarded.
func TestIsThisComputerPeer(t *testing.T) {
	old := ownAddrs
	t.Cleanup(func() { ownAddrs = old })
	ownAddrs = &addrSet{ttl: time.Hour, list: func() []net.Addr {
		_, v4, _ := net.ParseCIDR("192.0.2.10/24")
		v4.IP = net.ParseIP("192.0.2.10")
		_, v6, _ := net.ParseCIDR("fe80::10/64")
		v6.IP = net.ParseIP("fe80::10")
		return []net.Addr{v4, v6}
	}}
	peer := func(remote string, hdr ...string) bool {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		return IsThisComputerPeer(r)
	}
	assert.True(t, peer("127.0.0.1:5000"))
	assert.True(t, peer("[::1]:5000"))
	assert.True(t, peer("192.0.2.10:5000"), "its own interface address")
	assert.True(t, peer("[::ffff:192.0.2.10]:5000"), "the same address, IPv4-mapped")
	assert.True(t, peer("[fe80::10%en0]:5000"), "its own link-local address, with its zone")
	assert.False(t, peer("192.0.2.11:5000"), "another host on the same network")
	assert.False(t, peer("203.0.113.5:5000"))
	assert.False(t, peer("not an address"))
	assert.False(t, peer("127.0.0.1:5000", "X-Forwarded-For", "203.0.113.5"), "a proxy's request is not a local tool's")
	assert.False(t, peer("192.0.2.10:5000", "X-Forwarded-For", "203.0.113.5"))
}
