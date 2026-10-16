package peercred

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kernelAddr prints ap the way /proc/net/tcp{,6} does on a little-endian machine.
func kernelAddr(ap netip.AddrPort) string {
	var raw []byte
	if ap.Addr().Is4() {
		b := ap.Addr().As4()
		raw = b[:]
	} else {
		b := ap.Addr().As16()
		raw = b[:]
	}
	out := make([]byte, len(raw))
	for i := 0; i < len(raw); i += 4 {
		out[i], out[i+1], out[i+2], out[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}
	return strings.ToUpper(hex.EncodeToString(out)) + fmt.Sprintf(":%04X", ap.Port())
}

type row struct {
	local, remote netip.AddrPort
	state         int
	uid           int
}

const header = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

func writeTable(t *testing.T, dir, name string, rows ...row) {
	t.Helper()
	var b strings.Builder
	b.WriteString(header)
	for i, r := range rows {
		fmt.Fprintf(&b, "%4d: %s %s %02X 00000000:00000000 00:00000000 00000000 %5d        0 %d 1 0000000000000000 20 4 30 10 -1\n",
			i, kernelAddr(r.local), kernelAddr(r.remote), r.state, r.uid, 1000+i)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o644))
}

func useTables(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old, oldLE := procNet, hostLittleEndian
	procNet, hostLittleEndian = dir, true
	t.Cleanup(func() { procNet, hostLittleEndian = old, oldLE })
	return dir
}

var (
	server   = netip.MustParseAddrPort("127.0.0.1:8420")
	client   = netip.MustParseAddrPort("127.0.0.1:51234")
	server6  = netip.MustParseAddrPort("[::1]:8420")
	client6  = netip.MustParseAddrPort("[::1]:51234")
	mappedSv = netip.MustParseAddrPort("[::ffff:127.0.0.1]:8420")
	mappedCl = netip.MustParseAddrPort("[::ffff:127.0.0.1]:51234")
)

const (
	established = 0x01
	coralUID    = 501
	otherUID    = 1002
)

func TestParseAddrRoundTrip(t *testing.T) {
	for _, ap := range []netip.AddrPort{server, client, server6, netip.MustParseAddrPort("[fe80::1:2:3:4]:443")} {
		got, err := parseAddr(kernelAddr(ap))
		require.NoError(t, err)
		assert.Equal(t, ap, got)
	}
	// The kernel's own spelling of 127.0.0.1:8080 on x86.
	got, err := parseAddr("0100007F:1F90")
	require.NoError(t, err)
	assert.Equal(t, netip.MustParseAddrPort("127.0.0.1:8080"), got)
	for _, bad := range []string{"", "0100007F", "0100007F:1F9", "ZZ00007F:1F90", "0100007F0:1F90"} {
		_, err := parseAddr(bad)
		assert.Error(t, err, bad)
	}
}

// The client's row (local = client, remote = server) names the peer; Coral's own
// accepted socket (local = server, remote = client) must not be taken for it.
func TestPeerUIDIsTheClientSocketsOwner(t *testing.T) {
	dir := useTables(t)
	writeTable(t, dir, "tcp",
		row{server, netip.MustParseAddrPort("0.0.0.0:0"), stateListen, coralUID},
		row{server, client, established, coralUID}, // Coral's side
		row{client, server, established, otherUID}, // the caller's side
	)
	uid, err := PeerUID(client, server)
	require.NoError(t, err)
	assert.Equal(t, otherUID, uid)
}

func TestPeerUIDIPv6AndMapped(t *testing.T) {
	t.Run("ipv6 loopback", func(t *testing.T) {
		dir := useTables(t)
		writeTable(t, dir, "tcp", row{netip.MustParseAddrPort("127.0.0.1:1"), netip.MustParseAddrPort("127.0.0.1:2"), established, 7})
		writeTable(t, dir, "tcp6", row{server6, client6, established, coralUID}, row{client6, server6, established, otherUID})
		uid, err := PeerUID(client6, server6)
		require.NoError(t, err)
		assert.Equal(t, otherUID, uid)
	})
	t.Run("dual-stack listener sees an IPv4 client as ::ffff:127.0.0.1", func(t *testing.T) {
		dir := useTables(t)
		writeTable(t, dir, "tcp", row{client, server, established, otherUID}) // the client's own IPv4 socket
		writeTable(t, dir, "tcp6", row{mappedSv, mappedCl, established, coralUID})
		uid, err := PeerUID(mappedCl, mappedSv)
		require.NoError(t, err)
		assert.Equal(t, otherUID, uid)
	})
	t.Run("a mapped row in tcp6 matches an unmapped lookup", func(t *testing.T) {
		dir := useTables(t)
		writeTable(t, dir, "tcp", row{server, client, established, coralUID})
		writeTable(t, dir, "tcp6", row{mappedCl, mappedSv, established, otherUID})
		uid, err := PeerUID(client, server)
		require.NoError(t, err)
		assert.Equal(t, otherUID, uid)
	})
}

// Fail closed: no live client row is an error, never a uid.
func TestPeerUIDFailsClosed(t *testing.T) {
	t.Run("no row", func(t *testing.T) {
		dir := useTables(t)
		writeTable(t, dir, "tcp", row{server, client, established, coralUID})
		_, err := PeerUID(client, server)
		assert.True(t, errors.Is(err, ErrNotFound), "%v", err)
	})
	t.Run("only a TIME_WAIT or closed row (they show uid 0)", func(t *testing.T) {
		dir := useTables(t)
		writeTable(t, dir, "tcp", row{client, server, stateTimeWait, 0}, row{client, server, stateClose, 0})
		_, err := PeerUID(client, server)
		assert.True(t, errors.Is(err, ErrNotFound), "%v", err)
	})
	t.Run("no tables at all", func(t *testing.T) {
		useTables(t)
		_, err := PeerUID(client, server)
		assert.True(t, errors.Is(err, ErrNotFound), "%v", err)
	})
	t.Run("a bad uid", func(t *testing.T) {
		dir := useTables(t)
		table := header + fmt.Sprintf("   0: %s %s 01 00000000:00000000 00:00000000 00000000  x1x        0 1 1\n", kernelAddr(client), kernelAddr(server))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "tcp"), []byte(table), 0o644))
		_, err := PeerUID(client, server)
		assert.Error(t, err)
		assert.False(t, errors.Is(err, ErrNotFound))
	})
	t.Run("another port is another connection", func(t *testing.T) {
		dir := useTables(t)
		writeTable(t, dir, "tcp", row{netip.MustParseAddrPort("127.0.0.1:51235"), server, established, coralUID})
		_, err := PeerUID(client, server)
		assert.True(t, errors.Is(err, ErrNotFound), "%v", err)
	})
}

// Big-endian machines print the words unswapped.
func TestParseAddrBigEndian(t *testing.T) {
	old := hostLittleEndian
	hostLittleEndian = false
	t.Cleanup(func() { hostLittleEndian = old })
	got, err := parseAddr("7F000001:1F90")
	require.NoError(t, err)
	assert.Equal(t, netip.MustParseAddrPort("127.0.0.1:8080"), got)
}

// Where the kernel cannot be asked, callers keep their own rules.
func TestSameUserUnsupportedElsewhere(t *testing.T) {
	if Supported {
		t.Skip("covered by the real-request test")
	}
	same, supported := SameUser(httptestRequest())
	assert.False(t, same)
	assert.False(t, supported)
}

// #3905 (Security): an unknown table layout makes SameUser answer unsupported (the caller's
// own rules, as on a system without the check); every other failure still refuses.
func TestSameUserLayoutIsUnsupported(t *testing.T) {
	if !Supported {
		t.Skip("no peer check on this system")
	}
	old := ownerCheck
	t.Cleanup(func() { ownerCheck = old })
	r := httptestRequest()
	r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey,
		&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8420}))
	for _, tc := range []struct {
		err             error
		same, supported bool
	}{
		{fmt.Errorf("%w: an inpcb item of 112 bytes", ErrLayout), false, false},
		{ErrNotFound, false, true},
		{errAmbiguous, false, true},
		{errors.New("open process: access denied"), false, true},
		{nil, true, true},
	} {
		ownerCheck = func(netip.AddrPort, netip.AddrPort) (bool, error) { return tc.err == nil, tc.err }
		same, supported := SameUser(r)
		assert.Equal(t, tc.same, same, "%v", tc.err)
		assert.Equal(t, tc.supported, supported, "%v", tc.err)
	}
}
