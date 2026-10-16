package peercred

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const mibEstablished = 5

// tcpTableBytes builds what GetExtendedTcpTable(TCP_TABLE_OWNER_PID_ALL) returns.
func tcpTableBytes(v6 bool, rows ...tcpRow) []byte {
	le := binary.LittleEndian
	b := le.AppendUint32(nil, uint32(len(rows)))
	port := func(p uint16) []byte { return []byte{byte(p >> 8), byte(p), 0, 0} }
	for _, r := range rows {
		if v6 {
			l, rm := r.local.Addr().As16(), r.remote.Addr().As16()
			b = append(b, l[:]...)
			b = le.AppendUint32(b, 0)
			b = append(b, port(r.local.Port())...)
			b = append(b, rm[:]...)
			b = le.AppendUint32(b, 0)
			b = append(b, port(r.remote.Port())...)
			b = le.AppendUint32(b, r.state)
			b = le.AppendUint32(b, r.pid)
		} else {
			l, rm := r.local.Addr().As4(), r.remote.Addr().As4()
			b = le.AppendUint32(b, r.state)
			b = append(b, l[:]...)
			b = append(b, port(r.local.Port())...)
			b = append(b, rm[:]...)
			b = append(b, port(r.remote.Port())...)
			b = le.AppendUint32(b, r.pid)
		}
	}
	return b
}

func TestTCPTable(t *testing.T) {
	client := netip.MustParseAddrPort("127.0.0.1:51234")
	server := netip.MustParseAddrPort("127.0.0.1:8420")
	client6 := netip.MustParseAddrPort("[::1]:51234")
	server6 := netip.MustParseAddrPort("[::1]:8420")
	find := func(t *testing.T, v6 bool, c, s netip.AddrPort, rows ...tcpRow) (uint32, error) {
		t.Helper()
		parsed, err := parseTCPTable(tcpTableBytes(v6, rows...), v6)
		require.NoError(t, err)
		require.Equal(t, rows, parsed, "round trip")
		return ownerPID(parsed, c, s)
	}

	t.Run("IPv4 row", func(t *testing.T) {
		pid, err := find(t, false, client, server,
			tcpRow{server, client, mibEstablished, 100}, // the server's side of it
			tcpRow{client, server, mibEstablished, 4242})
		require.NoError(t, err)
		assert.Equal(t, uint32(4242), pid)
	})
	t.Run("IPv6 row", func(t *testing.T) {
		pid, err := find(t, true, client6, server6, tcpRow{client6, server6, mibEstablished, 77})
		require.NoError(t, err)
		assert.Equal(t, uint32(77), pid)
	})
	t.Run("an IPv6 row with ::ffff: addresses is the IPv4 connection", func(t *testing.T) {
		rows := []tcpRow{{client, server, mibEstablished, 78}}
		b := tcpTableBytes(true, rows...) // As16 of an IPv4 address is ::ffff:a.b.c.d
		parsed, err := parseTCPTable(b, true)
		require.NoError(t, err)
		pid, err := ownerPID(parsed, client, server)
		require.NoError(t, err)
		assert.Equal(t, uint32(78), pid)
	})
	t.Run("TIME_WAIT, closed, listening and deleted rows are not the client", func(t *testing.T) {
		for _, st := range []uint32{mibClosed, mibListen, mibTimeWait, mibDeleteTCB} {
			_, err := find(t, false, client, server, tcpRow{client, server, st, 0})
			assert.True(t, errors.Is(err, ErrNotFound), "state %d: %v", st, err)
		}
	})
	t.Run("another port is another connection", func(t *testing.T) {
		_, err := find(t, false, client, server, tcpRow{netip.MustParseAddrPort("127.0.0.1:51235"), server, mibEstablished, 1})
		assert.True(t, errors.Is(err, ErrNotFound), "%v", err)
	})
	t.Run("two owners fail closed", func(t *testing.T) {
		_, err := find(t, false, client, server,
			tcpRow{client, server, mibEstablished, 1}, tcpRow{client, server, mibEstablished, 2})
		assert.ErrorIs(t, err, errAmbiguous)
	})
	t.Run("a count larger than the buffer is an error", func(t *testing.T) {
		b := tcpTableBytes(false, tcpRow{client, server, mibEstablished, 1})
		binary.LittleEndian.PutUint32(b, 2)
		_, err := parseTCPTable(b, false)
		assert.Error(t, err)
		_, err = parseTCPTable(b[:3], false)
		assert.Error(t, err)
	})
}
