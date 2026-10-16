//go:build linux

package peercred

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The real kernel tables: a connection this process opens is its own uid's, for
// IPv4 and IPv6 loopback, and through an HTTP server's request.
func TestPeerUIDRealLoopback(t *testing.T) {
	for _, network := range []string{"tcp4", "tcp6"} {
		addr := map[string]string{"tcp4": "127.0.0.1:0", "tcp6": "[::1]:0"}[network]
		ln, err := net.Listen(network, addr)
		if err != nil {
			t.Skipf("%s loopback unavailable: %v", network, err)
		}
		defer ln.Close()
		accepted := make(chan net.Conn, 1)
		go func() { c, _ := ln.Accept(); accepted <- c }()
		c, err := net.Dial(network, ln.Addr().String())
		require.NoError(t, err)
		defer c.Close()
		sc := <-accepted
		require.NotNil(t, sc)
		defer sc.Close()

		clientAP := netip.MustParseAddrPort(sc.RemoteAddr().String())
		serverAP := netip.MustParseAddrPort(sc.LocalAddr().String())
		uid, err := PeerUID(clientAP, serverAP)
		require.NoError(t, err, network)
		assert.Equal(t, os.Geteuid(), uid, network)
	}
}

// Another local user's request is not the same user (run as root with setpriv and
// curl available, e.g. in the golang Docker image; skipped otherwise).
func TestSameUserOtherUIDRequest(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to make a request as another uid")
	}
	setpriv, err1 := exec.LookPath("setpriv")
	curl, err2 := exec.LookPath("curl")
	if err1 != nil || err2 != nil {
		t.Skip("needs setpriv and curl")
	}
	results := make(chan [2]bool, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		same, supported := SameUser(r)
		results <- [2]bool{same, supported}
	}))
	defer srv.Close()

	out, err := exec.Command(setpriv, "--reuid=1000", "--regid=1000", "--clear-groups", curl, "-s", srv.URL).CombinedOutput()
	require.NoError(t, err, string(out))
	got := <-results
	assert.Equal(t, [2]bool{false, true}, got, "uid 1000 is not this process's user")

	out, err = exec.Command(curl, "-s", srv.URL).CombinedOutput() // and root's own request is
	require.NoError(t, err, string(out))
	assert.Equal(t, [2]bool{true, true}, <-results)
}
