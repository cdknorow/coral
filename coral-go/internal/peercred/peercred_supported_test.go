//go:build linux || darwin || windows

package peercred

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The real kernel tables: a connection this process opens is its own user's, on
// IPv4 and IPv6 loopback, and when an IPv4 client reaches a dual-stack listener.
func TestOwnerIsMeRealLoopback(t *testing.T) {
	for _, tc := range []struct{ name, listenNet, listenAddr, dialNet, dialHost string }{
		{"IPv4", "tcp4", "127.0.0.1:0", "tcp4", "127.0.0.1"},
		{"IPv6", "tcp6", "[::1]:0", "tcp6", "::1"},
		{"IPv4 to a dual-stack listener", "tcp", ":0", "tcp4", "127.0.0.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ln, err := net.Listen(tc.listenNet, tc.listenAddr)
			if err != nil {
				t.Skipf("%s unavailable: %v", tc.listenNet, err)
			}
			defer ln.Close()
			accepted := make(chan net.Conn, 1)
			go func() { c, _ := ln.Accept(); accepted <- c }()
			port := ln.Addr().(*net.TCPAddr).Port
			c, err := net.Dial(tc.dialNet, net.JoinHostPort(tc.dialHost, fmt.Sprint(port)))
			require.NoError(t, err)
			defer c.Close()
			sc := <-accepted
			require.NotNil(t, sc)
			defer sc.Close()

			// What a server sees: the client is the remote end of its accepted socket.
			client := unmap(netip.MustParseAddrPort(sc.RemoteAddr().String()))
			server := unmap(netip.MustParseAddrPort(sc.LocalAddr().String()))
			me, err := ownerIsMe(client, server)
			require.NoError(t, err)
			assert.True(t, me)

			// The server's own socket is the other way round: not the client's row.
			_, err = ownerIsMe(server, netip.AddrPortFrom(server.Addr(), 1))
			assert.True(t, errors.Is(err, ErrNotFound), "%v", err)
		})
	}
}

func TestSameUserRealRequest(t *testing.T) {
	var same, supported bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		same, supported = SameUser(r)
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	assert.True(t, supported)
	assert.True(t, same, "a request from this process is from the same user")
}

// A loopback request whose socket cannot be found is not the same user.
func TestSameUserFailsClosed(t *testing.T) {
	r := httptestRequest() // 127.0.0.1:51234, no such connection
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, ln.Addr()))
	same, supported := SameUser(r)
	assert.False(t, same)
	assert.True(t, supported)

	// No local address: cannot tell, so not the same user.
	same, supported = SameUser(httptestRequest())
	assert.False(t, same)
	assert.True(t, supported)
}
