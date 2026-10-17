package peercred

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pcb is one socket of a synthetic pcblist_n: an inpcb item, then its socket item.
type pcb struct {
	vflag         byte
	local, remote netip.AddrPort
	uid           uint32
	noSocket      bool // the socket item is missing
}

// pcbList builds what net.inet.tcp.pcblist_n returns, in byte order o.
func pcbList(o binary.ByteOrder, pcbs ...pcb) []byte {
	b := make([]byte, 24) // xinpgen
	o.PutUint32(b, 24)
	item := func(kind uint32, n int) []byte {
		it := make([]byte, n)
		o.PutUint32(it, uint32(n))
		o.PutUint32(it[4:], kind)
		return it
	}
	put := func(it []byte, addrAt, portAt int, ap netip.AddrPort, vflag byte) {
		binary.BigEndian.PutUint16(it[portAt:], ap.Port())
		if vflag == inpIPv4 {
			a := ap.Addr().As4()
			copy(it[addrAt+12:], a[:])
		} else {
			a := ap.Addr().As16()
			copy(it[addrAt:], a[:])
		}
	}
	for _, p := range pcbs {
		in := item(xsoInpcb, 104)
		in[inpcbVFlag] = p.vflag
		put(in, inpcbLAddr, inpcbLPort, p.local, p.vflag)
		put(in, inpcbFAddr, inpcbFPort, p.remote, p.vflag)
		b = append(b, in...)
		if !p.noSocket {
			so := item(xsoSocket, 104)
			o.PutUint32(so[socketUID:], p.uid)
			b = append(b, so...)
		}
		b = append(b, item(0x020, 208)...) // the tcpcb, ignored
	}
	return append(b, item(uint32(len(pcbs)), 24)...) // the trailing xinpgen: {len, socket count, gen, sogen}
}

// #4136: the trailing xinpgen's second word is the socket count. With 16 sockets it equals
// XSO_INPCB, and the trailer was read as an inpcb item of 24 bytes: an unknown layout, so the
// owner check failed whenever the computer had exactly 16 TCP sockets (seen on a macOS runner).
func TestParsePCBListTrailerIsNotAnItem(t *testing.T) {
	client := netip.MustParseAddrPort("127.0.0.1:51234")
	server := netip.MustParseAddrPort("127.0.0.1:8420")
	for count := 1; count <= 40; count++ {
		var pcbs []pcb
		for i := 0; i < count-1; i++ {
			pcbs = append(pcbs, pcb{inpIPv4, netip.AddrPortFrom(client.Addr(), uint16(40000+i)), server, 0, false})
		}
		pcbs = append(pcbs, pcb{inpIPv4, client, server, 501, false})
		uid, err := parsePCBList(pcbList(binary.LittleEndian, pcbs...), client, server, true)
		require.NoError(t, err, "%d sockets", count)
		assert.Equal(t, 501, uid, "%d sockets", count)
	}
}

func TestParsePCBList(t *testing.T) {
	le := binary.LittleEndian
	client := netip.MustParseAddrPort("127.0.0.1:51234")
	server := netip.MustParseAddrPort("127.0.0.1:8420")
	client6 := netip.MustParseAddrPort("[::1]:51234")
	server6 := netip.MustParseAddrPort("[::1]:8420")
	other := pcb{inpIPv4, netip.MustParseAddrPort("127.0.0.1:40000"), server, 0, false}

	t.Run("IPv4 client", func(t *testing.T) {
		b := pcbList(le, other, pcb{inpIPv4, client, server, 501, false})
		uid, err := parsePCBList(b, client, server, true)
		require.NoError(t, err)
		assert.Equal(t, 501, uid)
	})
	t.Run("IPv6 client", func(t *testing.T) {
		b := pcbList(le, pcb{inpIPv6, client6, server6, 502, false})
		uid, err := parsePCBList(b, client6, server6, true)
		require.NoError(t, err)
		assert.Equal(t, 502, uid)
	})
	t.Run("an IPv6 socket talking IPv4 (::ffff:) matches the IPv4 addresses", func(t *testing.T) {
		mapped := func(ap netip.AddrPort) netip.AddrPort {
			return netip.AddrPortFrom(netip.AddrFrom16(ap.Addr().As16()), ap.Port())
		}
		b := pcbList(le, pcb{inpIPv6, mapped(client), mapped(server), 503, false})
		uid, err := parsePCBList(b, client, server, true)
		require.NoError(t, err)
		assert.Equal(t, 503, uid)
	})
	t.Run("the ends must match the right way round", func(t *testing.T) {
		b := pcbList(le, pcb{inpIPv4, server, client, 501, false}) // the server's own accepted socket
		_, err := parsePCBList(b, client, server, true)
		assert.True(t, errors.Is(err, ErrNotFound), "%v", err)
	})
	t.Run("::1 is not 0.0.0.1", func(t *testing.T) {
		b := pcbList(le, pcb{inpIPv4, netip.MustParseAddrPort("0.0.0.1:51234"), netip.MustParseAddrPort("0.0.0.1:8420"), 501, false})
		_, err := parsePCBList(b, client6, server6, true)
		assert.True(t, errors.Is(err, ErrNotFound), "%v", err)
	})
	t.Run("an unbound dual-stack pcb has no address to compare", func(t *testing.T) {
		b := pcbList(le, pcb{inpIPv4 | inpIPv6, client6, server6, 501, false})
		_, err := parsePCBList(b, client6, server6, true)
		assert.True(t, errors.Is(err, ErrNotFound), "%v", err)
	})
	t.Run("two owners fail closed", func(t *testing.T) {
		b := pcbList(le, pcb{inpIPv4, client, server, 501, false}, pcb{inpIPv4, client, server, 0, false})
		_, err := parsePCBList(b, client, server, true)
		assert.ErrorIs(t, err, errAmbiguous)
	})
	t.Run("a matching inpcb without its socket item is not found", func(t *testing.T) {
		b := pcbList(le, pcb{inpIPv4, client, server, 501, true}, other)
		_, err := parsePCBList(b, client, server, true)
		assert.True(t, errors.Is(err, ErrNotFound), "%v", err)
	})
	t.Run("big-endian hosts", func(t *testing.T) {
		b := pcbList(binary.BigEndian, pcb{inpIPv4, client, server, 501, false})
		uid, err := parsePCBList(b, client, server, false)
		require.NoError(t, err)
		assert.Equal(t, 501, uid)
	})
	t.Run("short and damaged lists give an error or the right uid, never a panic", func(t *testing.T) {
		full := pcbList(le, pcb{inpIPv4, client, server, 501, false})
		for n := 0; n < len(full); n++ {
			uid, err := parsePCBList(full[:n], client, server, true)
			if err == nil { // cut after the socket item: the uid is all there
				assert.Equal(t, 501, uid, "truncated at %d", n)
			}
		}
		bad := pcbList(le, pcb{inpIPv4, client, server, 501, false})
		le.PutUint32(bad[24:], 40) // an inpcb item too short to hold the addresses
		_, err := parsePCBList(bad, client, server, true)
		assert.ErrorIs(t, err, ErrLayout)
	})
	// #3905 (Security): a struct size this build doesn't know is ErrLayout, never a guess at
	// where so_uid moved, on any item, not only the connection's.
	t.Run("an unknown item size is ErrLayout", func(t *testing.T) {
		for name, at := range map[string]int{
			"inpcb of the connection":  24,
			"socket of the connection": 24 + 104,
			"inpcb of another socket":  24 + 104 + 104 + 208,
		} {
			b := pcbList(le, pcb{inpIPv4, client, server, 501, false}, other)
			le.PutUint32(b[at:], 112)
			_, err := parsePCBList(b, client, server, true)
			assert.ErrorIs(t, err, ErrLayout, name)
		}
	})
}
