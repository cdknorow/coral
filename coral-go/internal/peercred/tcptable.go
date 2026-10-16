package peercred

import (
	"encoding/binary"
	"fmt"
	"net/netip"
)

// Windows: GetExtendedTcpTable(TCP_TABLE_OWNER_PID_ALL) returns a u32 row count
// and then MIB_TCPROW_OWNER_PID rows (AF_INET) or MIB_TCP6ROW_OWNER_PID rows
// (AF_INET6). Addresses are in network order, the ports are network order in the
// first two bytes of their u32, and the other u32s are host (little-endian)
// order. Parsed here, on every system, so the tests can feed it bytes.
const (
	tcpRow4Size = 24 // dwState, dwLocalAddr, dwLocalPort, dwRemoteAddr, dwRemotePort, dwOwningPid
	tcpRow6Size = 56 // ucLocalAddr[16], dwLocalScopeId, dwLocalPort, ucRemoteAddr[16], dwRemoteScopeId, dwRemotePort, dwState, dwOwningPid

	// MIB_TCP_STATE values a client socket in the middle of a request cannot be in.
	mibClosed    = 1
	mibListen    = 2
	mibTimeWait  = 11
	mibDeleteTCB = 12
)

type tcpRow struct {
	local, remote netip.AddrPort
	state, pid    uint32
}

// parseTCPTable reads a MIB_TCPTABLE_OWNER_PID (v6 false) or
// MIB_TCP6TABLE_OWNER_PID (v6 true).
func parseTCPTable(b []byte, v6 bool) ([]tcpRow, error) {
	if len(b) < 4 {
		return nil, fmt.Errorf("peercred: tcp table of %d bytes", len(b))
	}
	n := int(binary.LittleEndian.Uint32(b))
	size := tcpRow4Size
	if v6 {
		size = tcpRow6Size
	}
	if n < 0 || n > (len(b)-4)/size {
		return nil, fmt.Errorf("peercred: tcp table says %d rows in %d bytes", n, len(b))
	}
	rows := make([]tcpRow, 0, n)
	for i := 0; i < n; i++ {
		r := b[4+i*size : 4+(i+1)*size]
		port := func(at int) uint16 { return binary.BigEndian.Uint16(r[at:]) }
		u32 := func(at int) uint32 { return binary.LittleEndian.Uint32(r[at:]) }
		if v6 {
			rows = append(rows, tcpRow{
				local:  netip.AddrPortFrom(netip.AddrFrom16([16]byte(r[0:16])).Unmap(), port(20)),
				remote: netip.AddrPortFrom(netip.AddrFrom16([16]byte(r[24:40])).Unmap(), port(44)),
				state:  u32(48),
				pid:    u32(52),
			})
		} else {
			rows = append(rows, tcpRow{
				local:  netip.AddrPortFrom(netip.AddrFrom4([4]byte(r[4:8])), port(8)),
				remote: netip.AddrPortFrom(netip.AddrFrom4([4]byte(r[12:16])), port(16)),
				state:  u32(0),
				pid:    u32(20),
			})
		}
	}
	return rows, nil
}

// ownerPID finds the process owning the live socket whose local address is
// client and remote address is server.
func ownerPID(rows []tcpRow, client, server netip.AddrPort) (uint32, error) {
	var pid uint32
	found := false
	for _, r := range rows {
		if r.local != client || r.remote != server {
			continue
		}
		switch r.state {
		case mibClosed, mibListen, mibTimeWait, mibDeleteTCB:
			continue // not a live client socket (TIME_WAIT rows show pid 0)
		}
		if found && r.pid != pid {
			return 0, errAmbiguous
		}
		pid, found = r.pid, true
	}
	if !found {
		return 0, ErrNotFound
	}
	return pid, nil
}
