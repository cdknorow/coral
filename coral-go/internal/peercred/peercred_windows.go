//go:build windows

package peercred

import (
	"errors"
	"fmt"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Supported reports whether this platform can tell the peer's user.
const Supported = true

const tcpTableOwnerPIDAll = 5 // TCP_TABLE_OWNER_PID_ALL

var (
	iphlpapi                = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpTable = iphlpapi.NewProc("GetExtendedTcpTable")
)

// ownerIsMe: the process owning the client socket runs as this process's user
// (the token user SIDs are equal).
func ownerIsMe(client, server netip.AddrPort) (bool, error) {
	pid, err := socketPID(client, server)
	if err != nil {
		return false, err
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false, fmt.Errorf("peercred: open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	// A pid is not reused while a handle to its process is open: if the socket
	// still belongs to pid now, h is the process that owns it.
	again, err := socketPID(client, server)
	if err != nil {
		return false, err
	}
	if again != pid {
		return false, errAmbiguous
	}
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		return false, fmt.Errorf("peercred: open token of process %d: %w", pid, err)
	}
	defer tok.Close()
	theirs, err := tok.GetTokenUser()
	if err != nil {
		return false, err
	}
	mine, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false, err
	}
	return theirs.User.Sid.Equals(mine.User.Sid), nil
}

// socketPID reads the table of the client's address family.
func socketPID(client, server netip.AddrPort) (uint32, error) {
	family, v6 := uint32(windows.AF_INET), false
	if client.Addr().Is6() {
		family, v6 = windows.AF_INET6, true
	}
	b, err := tcpTable(family)
	if err != nil {
		return 0, err
	}
	rows, err := parseTCPTable(b, v6)
	if err != nil {
		return 0, err
	}
	return ownerPID(rows, client, server)
}

func tcpTable(family uint32) ([]byte, error) {
	if err := procGetExtendedTcpTable.Find(); err != nil {
		return nil, err
	}
	size := uint32(16 << 10)
	for attempt := 0; attempt < 8; attempt++ { // the table can grow between calls
		buf := make([]byte, size)
		r, _, _ := procGetExtendedTcpTable.Call(
			uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)),
			0, uintptr(family), tcpTableOwnerPIDAll, 0)
		switch windows.Errno(r) {
		case windows.ERROR_SUCCESS:
			return buf, nil
		case windows.ERROR_INSUFFICIENT_BUFFER:
			size += 4 << 10
		default:
			return nil, fmt.Errorf("peercred: GetExtendedTcpTable: %w", windows.Errno(r))
		}
	}
	return nil, errors.New("peercred: GetExtendedTcpTable kept growing")
}
