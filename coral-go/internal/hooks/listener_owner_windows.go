//go:build windows

package hooks

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetExtendedTcpTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

const (
	tcpTableOwnerPIDListener = 3 // TCP_TABLE_OWNER_PID_LISTENER
	afInet                   = 2
	afInet6                  = 23
)

// listenerPIDs and pidOwner are the system lookups; a test replaces them.
var (
	listenerPIDs = tcpListenerPIDs
	pidOwner     = processOwner
)

// checkListener: every process listening where a connection to host:port would land is this user's. No listener at all is
// fine (the connection fails by itself). A listener of another account, or one whose owner can't be read, fails closed.
// The image (coral.exe, coral-tray.exe) is only logged when it is something else: dev builds and renamed binaries still work.
func checkListener(host string, port int) error {
	me, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("coral: can't read this user's account to check Coral's port %d: %w", port, err)
	}
	pids, err := listenerPIDs(host, port)
	if err != nil {
		return fmt.Errorf("coral: can't list what listens on port %d to check it is Coral: %w", port, err)
	}
	for _, pid := range pids {
		sid, image, err := pidOwner(pid)
		if err != nil {
			return &ForeignListenerError{Port: port, PID: pid, Why: "can't be checked (" + err.Error() + "), so it may be another account's"}
		}
		if sid == nil || !sid.Equals(me) {
			who := "another account"
			if sid != nil {
				who = sid.String()
			}
			return &ForeignListenerError{Port: port, PID: pid, Why: "belongs to " + who + ", not this user"}
		}
		if base := strings.ToLower(filepath.Base(image)); base != "coral.exe" && base != "coral-tray.exe" {
			slog.Warn("coral: the program on Coral's port is this user's, but not coral.exe or coral-tray.exe (a dev build or a renamed binary?)",
				"port", port, "pid", pid, "image", image)
		}
	}
	return nil
}

func currentUserSID() (*windows.SID, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return u.User.Sid.Copy()
}

// processOwner is pid's user SID and image path.
func processOwner(pid uint32) (*windows.SID, string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return nil, "", err
	}
	defer windows.CloseHandle(h)
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		return nil, "", err
	}
	defer tok.Close()
	u, err := tok.GetTokenUser()
	if err != nil {
		return nil, "", err
	}
	sid, err := u.User.Sid.Copy()
	if err != nil {
		return nil, "", err
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	image := ""
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) == nil {
		image = windows.UTF16ToString(buf[:n])
	}
	return sid, image, nil
}

// tcpListenerPIDs: the PIDs listening on port where a connection to host would land: the host's own address or the
// wildcard of its family. "localhost" is both 127.0.0.1 and ::1.
func tcpListenerPIDs(host string, port int) ([]uint32, error) {
	var want []net.IP
	if strings.EqualFold(host, "localhost") {
		want = []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	} else if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		want = []net.IP{ip}
	} else {
		return nil, errors.New("not an IP address: " + host)
	}
	seen := map[uint32]bool{}
	var pids []uint32
	add := func(pid uint32) {
		if !seen[pid] {
			seen[pid] = true
			pids = append(pids, pid)
		}
	}
	for _, ip := range want {
		if ip4 := ip.To4(); ip4 != nil {
			rows, err := tcpTable(afInet)
			if err != nil {
				return nil, err
			}
			for i := 0; i+24 <= len(rows); i += 24 { // MIB_TCPROW_OWNER_PID: state, localAddr, localPort, remoteAddr, remotePort, pid
				local := net.IP(rows[i+4 : i+8])
				lport := int(binary.BigEndian.Uint16(rows[i+8 : i+10]))
				if lport == port && (local.Equal(ip4) || local.Equal(net.IPv4zero)) {
					add(binary.LittleEndian.Uint32(rows[i+20 : i+24]))
				}
			}
			continue
		}
		rows, err := tcpTable(afInet6)
		if err != nil {
			return nil, err
		}
		for i := 0; i+56 <= len(rows); i += 56 { // MIB_TCP6ROW_OWNER_PID: localAddr[16], scope, localPort, remoteAddr[16], scope, remotePort, state, pid
			local := net.IP(rows[i : i+16])
			lport := int(binary.BigEndian.Uint16(rows[i+20 : i+22]))
			if lport == port && (local.Equal(ip) || local.Equal(net.IPv6unspecified)) {
				add(binary.LittleEndian.Uint32(rows[i+52 : i+56]))
			}
		}
	}
	return pids, nil
}

// tcpTable is the listener table of family af, its rows as raw bytes (after the entry count).
func tcpTable(af uint32) ([]byte, error) {
	var size uint32
	r, _, _ := procGetExtendedTcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(af), tcpTableOwnerPIDListener, 0)
	for attempt := 0; attempt < 4; attempt++ {
		if size == 0 {
			size = 4096
		}
		buf := make([]byte, size)
		r, _, _ = procGetExtendedTcpTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, uintptr(af), tcpTableOwnerPIDListener, 0)
		switch windows.Errno(r) {
		case 0:
			n := binary.LittleEndian.Uint32(buf[0:4])
			rowSize := uint32(24)
			if af == afInet6 {
				rowSize = 56
			}
			end := 4 + n*rowSize
			if end > uint32(len(buf)) {
				end = uint32(len(buf))
			}
			return buf[4:end], nil
		case windows.ERROR_INSUFFICIENT_BUFFER:
			continue // the table grew: size now holds the new size
		default:
			return nil, windows.Errno(r)
		}
	}
	return nil, errors.New("GetExtendedTcpTable: the table kept growing")
}
