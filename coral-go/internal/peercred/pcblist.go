package peercred

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
)

// macOS: the net.inet.tcp.pcblist_n sysctl (what netstat reads) is a header,
// then for each TCP socket a run of items, each {u32 len, u32 kind, ...} padded
// to 8 bytes: the inpcb (addresses), then the socket (uid), then buffers, stats
// and the tcpcb. The layouts are xnu's private struct xinpcb_n and xsocket_n
// (bsd/netinet/in_pcb.h, bsd/sys/socketvar.h), host byte order except the ports.
// Parsed here, on every system, so the tests can feed it bytes.
const (
	xsoSocket = 0x001 // XSO_SOCKET
	xsoInpcb  = 0x010 // XSO_INPCB

	inpcbFPort = 16 // u16, network order
	inpcbLPort = 18
	inpcbVFlag = 44 // INP_IPV4 1, INP_IPV6 2
	inpcbFAddr = 48 // 16 bytes: in6_addr, or an IPv4 address in the last 4
	inpcbLAddr = 64

	socketUID = 64 // so_uid, u32

	inpIPv4 = 0x1
	inpIPv6 = 0x2
)

// The item sizes this layout is known at (sizeof xinpcb_n and xsocket_n: read on Darwin 25
// arm64; cross-build.yml runs the real-table tests on the macOS arm64 and Intel runners).
// Any other size means the structs changed: ErrLayout, never a guess at where the fields
// moved.
var (
	knownInpcbLens  = map[int]bool{104: true}
	knownSocketLens = map[int]bool{104: true}
)

// errAmbiguous: two sockets with the client's addresses and different users.
var errAmbiguous = errors.New("peercred: the connection's socket has more than one owner")

// parsePCBList finds the uid of the socket whose local address is client and
// remote address is server. le is the host byte order (a test seam).
func parsePCBList(b []byte, client, server netip.AddrPort, le bool) (int, error) {
	order := binary.ByteOrder(binary.BigEndian)
	if le {
		order = binary.LittleEndian
	}
	if len(b) < 4 {
		return -1, fmt.Errorf("peercred: pcblist of %d bytes", len(b))
	}
	off := int(order.Uint32(b))
	header := off // the list starts and ends with an xinpgen of this length
	uid, found, pending := -1, false, false
	for off+8 <= len(b) {
		n := int(order.Uint32(b[off:]))
		kind := order.Uint32(b[off+4:])
		if n < 8 || off+n > len(b) {
			break // a short read
		}
		if n == header && off+n == len(b) {
			// The trailing xinpgen. Its second word is the socket count, not an item kind:
			// with exactly 16 sockets it reads as XSO_INPCB (0x10) and was taken for an inpcb
			// of an unknown size, so every owner check failed (#4136, a macOS CI flake).
			break
		}
		item := b[off : off+n]
		switch kind {
		case xsoInpcb: // every item is checked: one changed struct changes them all
			if !knownInpcbLens[n] {
				return -1, fmt.Errorf("%w: an inpcb item of %d bytes", ErrLayout, n)
			}
			local, ok1 := pcbAddr(item, inpcbLAddr, inpcbLPort)
			remote, ok2 := pcbAddr(item, inpcbFAddr, inpcbFPort)
			pending = ok1 && ok2 && local == client && remote == server
		case xsoSocket:
			if !knownSocketLens[n] {
				return -1, fmt.Errorf("%w: a socket item of %d bytes", ErrLayout, n)
			}
			if pending {
				pending = false
				u := int(order.Uint32(item[socketUID:]))
				if found && u != uid {
					return -1, errAmbiguous
				}
				uid, found = u, true
			}
		}
		off += (n + 7) &^ 7
	}
	if !found {
		return -1, ErrNotFound
	}
	return uid, nil
}

// pcbAddr reads one end of an inpcb item. Only a pcb that is IPv4 or IPv6 (not
// both: an unbound dual-stack listener) has an address to compare.
func pcbAddr(item []byte, addrAt, portAt int) (netip.AddrPort, bool) {
	port := binary.BigEndian.Uint16(item[portAt:])
	var addr netip.Addr
	switch item[inpcbVFlag] & (inpIPv4 | inpIPv6) {
	case inpIPv4:
		addr = netip.AddrFrom4([4]byte(item[addrAt+12 : addrAt+16]))
	case inpIPv6:
		addr = netip.AddrFrom16([16]byte(item[addrAt : addrAt+16])).Unmap()
	default:
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(addr, port), true
}
