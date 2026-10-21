package startup

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/auth"
	"github.com/subgentic/coral/internal/peercred"
)

// loopbackConn is a net.Conn with loopback addresses and nothing else.
type loopbackConn struct{ net.Conn }

func (loopbackConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 50000}
}
func (loopbackConn) LocalAddr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8420} }

// #95: the main server attributes each loopback connection to its OS user (auth.ConnContext). Without
// it every connection would keep the loopback pass, whoever's it is.
func TestMainServerAttributesLoopbackConnections(t *testing.T) {
	t.Cleanup(auth.SetPeerAttribution(func(netip.AddrPort, netip.AddrPort) (peercred.Verdict, error) {
		return peercred.OtherUser, nil
	}))
	srv := newHTTPServer(http.NotFoundHandler())
	require.NotNil(t, srv.ConnContext, "the main server must attribute loopback connections")
	ctx := srv.ConnContext(context.Background(), loopbackConn{})
	r := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	assert.Equal(t, auth.PeerOtherUser, auth.PeerReason(r))
}

// #95: the profiler has no credential, so it answers only Coral's own OS user.
func TestProfilerIsOnlyForCoralsOwnUser(t *testing.T) {
	verdict := peercred.OtherUser
	t.Cleanup(auth.SetPeerAttribution(func(netip.AddrPort, netip.AddrPort) (peercred.Verdict, error) { return verdict, nil }))
	h := ownUserOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	for _, tc := range []struct {
		v    peercred.Verdict
		want int
	}{{peercred.OtherUser, http.StatusForbidden}, {peercred.Unattributed, http.StatusForbidden}, {peercred.Self, http.StatusOK}, {peercred.Unsupported, http.StatusOK}} {
		verdict = tc.v
		ctx := auth.ConnContext(context.Background(), loopbackConn{})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/debug/pprof/heap", nil).WithContext(ctx))
		assert.Equal(t, tc.want, rec.Code, "verdict %d", tc.v)
	}
}
