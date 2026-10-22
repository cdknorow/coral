//go:build windows

package hooks

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/subgentic/coral/internal/agentenv"
	"github.com/subgentic/coral/internal/cliauth"
)

var procGetExtendedTcpTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

const (
	tcpTableOwnerPIDListener = 3 // TCP_TABLE_OWNER_PID_LISTENER
	afInet                   = 2
	afInet6                  = 23
)

// listenerPIDs, pidOwner, coralFolders, tcpRows, selfSID and fileOwner are the system lookups; a test replaces them.
var (
	listenerPIDs = tcpListenerPIDs
	pidOwner     = processOwner
	coralFolders = allowedCoralFolders
	tcpRows      = tcpListeners
	selfSID      = currentUserSID
	fileOwner    = fileOwnerSID
)

// checkListener: the process that would receive a connection to host:port is Coral's (#224 B): its token's user is this user's
// SID, and its image is coral.exe or coral-tray.exe in a folder Coral runs from (the running tool's own folder, which covers a
// dev build and the installed tools, or the Coral install under Program Files). No listener at all is fine (the connection
// fails by itself). Anything else, or a listener whose owner can't be read, fails closed with a reason.
func checkListener(host string, port int) error {
	owners, err := allowedOwners()
	if err != nil {
		return fmt.Errorf("coral: can't read this user's account to check Coral's port %d: %w", port, err)
	}
	pids, err := listenerPIDs(host, port)
	if err != nil {
		return fmt.Errorf("coral: can't list what listens on port %d to check it is Coral: %w", port, err)
	}
	self := uint32(os.Getpid())
	for _, pid := range pids {
		if pid == self {
			continue // this very process listens there (a test's in-process server): nothing to check; a tool never listens on Coral's port itself
		}
		sid, image, err := pidOwner(pid)
		if err != nil {
			return &ForeignListenerError{Port: port, PID: pid, Why: "can't be checked (" + err.Error() + "), so it may be another account's"}
		}
		if !ownedByOneOf(sid, owners) {
			who := "another account"
			if sid != nil {
				who = sid.String()
			}
			return &ForeignListenerError{Port: port, PID: pid, Why: "belongs to " + who + ", not this user or the owner of Coral's data folder"}
		}
		if !isCoralImage(image, coralFolders()) && !sameExecutable(image) {
			return &ForeignListenerError{Port: port, PID: pid, Why: "is " + image + ", not Coral's coral.exe or coral-tray.exe from its own folder"}
		}
	}
	return nil
}

// sameExecutable: image is this tool's own executable file. A test runs its CLI as a child of the test binary, which serves on
// the port; a real tool never listens on Coral's port, so an impostor would have to be the user's own coral-board or coral-agent.
func sameExecutable(image string) bool {
	exe, err := os.Executable()
	if err != nil || image == "" {
		return false
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return strings.EqualFold(filepath.Clean(exe), filepath.Clean(image))
}

// isCoralImage: image is coral.exe or coral-tray.exe directly in one of folders.
func isCoralImage(image string, folders []string) bool {
	if image == "" {
		return false
	}
	if base := strings.ToLower(filepath.Base(image)); base != "coral.exe" && base != "coral-tray.exe" {
		return false
	}
	dir := filepath.Clean(filepath.Dir(image))
	for _, f := range folders {
		if f != "" && strings.EqualFold(dir, filepath.Clean(f)) {
			return true
		}
	}
	return false
}

// allowedCoralFolders: where Coral's server runs from. The running tool's own folder (coral-board and the hooks sit beside
// coral.exe and coral-tray.exe in the install, and in a dev build's output folder), and the Coral install under Program Files
// (both the x64 and the x86 folder: the release MSI was x86 until #93).
func allowedCoralFolders() []string {
	var out []string
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		out = append(out, filepath.Dir(exe))
	}
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
		if p := os.Getenv(env); p != "" {
			out = append(out, filepath.Join(p, "Coral"))
		}
	}
	return out
}

// allowedOwners: the accounts Coral's server may run as, for this tool. This user, and the owner of the session's token file and
// of Coral's data folder: a tool started inside a sandbox that runs commands as another local account (Codex's elevated
// Windows sandbox) has another SID than the user running Coral, whose data folder and token file are the user's (Security's
// note on #224). A well-known service or group owner (SYSTEM, LocalService, NetworkService, Administrators) is never added:
// those are not one person's Coral, and a service such as the one this computer has on 8420 runs as one of them.
func allowedOwners() ([]*windows.SID, error) {
	me, err := selfSID()
	if err != nil {
		return nil, err
	}
	owners := []*windows.SID{me}
	paths := []string{cliauth.DataDir()}
	if p := strings.TrimSpace(os.Getenv(agentenv.SessionAuthFileEnv)); p != "" {
		paths = append(paths, p)
	}
	for _, p := range paths {
		if sid, err := fileOwner(p); err == nil && sid != nil && isPersonSID(sid) && !ownedByOneOf(sid, owners) {
			owners = append(owners, sid)
		}
	}
	return owners, nil
}

// isPersonSID: sid is not a well-known service or group account.
func isPersonSID(sid *windows.SID) bool {
	for _, wk := range []windows.WELL_KNOWN_SID_TYPE{windows.WinLocalSystemSid, windows.WinLocalServiceSid, windows.WinNetworkServiceSid, windows.WinBuiltinAdministratorsSid, windows.WinBuiltinUsersSid, windows.WinWorldSid, windows.WinAuthenticatedUserSid} {
		if sid.IsWellKnown(wk) {
			return false
		}
	}
	return true
}

func ownedByOneOf(sid *windows.SID, owners []*windows.SID) bool {
	if sid == nil {
		return false
	}
	for _, o := range owners {
		if sid.Equals(o) {
			return true
		}
	}
	return false
}

// fileOwnerSID is the owner of the file or folder at path.
func fileOwnerSID(path string) (*windows.SID, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return nil, err
	}
	return owner.Copy()
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

// listenRow is one TCP listener: its local address, port and process.
type listenRow struct {
	addr net.IP
	port int
	pid  uint32
}

// winningPIDs: the processes a connection to ip:port would reach. A listener on ip itself wins over one on its family's
// wildcard (0.0.0.0 or ::), the more specific address taking the connection (the Security Reviewer's #224 finding), so when
// both exist only the specific one is returned: that is the one to check.
func winningPIDs(rows []listenRow, ip net.IP, port int) []uint32 {
	wildcard := net.IPv4zero
	if ip.To4() == nil {
		wildcard = net.IPv6unspecified
	}
	var exact, wild []uint32
	for _, r := range rows {
		if r.port != port {
			continue
		}
		switch {
		case r.addr.Equal(ip):
			exact = append(exact, r.pid)
		case r.addr.Equal(wildcard):
			wild = append(wild, r.pid)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return wild
}

// tcpListenerPIDs: the processes a connection to host:port would reach, per address family. "localhost" is both 127.0.0.1 and
// ::1, and each family's winner is returned.
func tcpListenerPIDs(host string, port int) ([]uint32, error) {
	var want []net.IP
	if strings.EqualFold(host, "localhost") {
		want = []net.IP{net.IPv4(127, 0, 0, 1).To4(), net.IPv6loopback}
	} else if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			ip = ip4
		}
		want = []net.IP{ip}
	} else {
		return nil, errors.New("not an IP address: " + host)
	}
	seen := map[uint32]bool{}
	var pids []uint32
	for _, ip := range want {
		af := uint32(afInet6)
		if ip.To4() != nil {
			af = afInet
		}
		rows, err := tcpRows(af)
		if err != nil {
			return nil, err
		}
		for _, pid := range winningPIDs(rows, ip, port) {
			if !seen[pid] {
				seen[pid] = true
				pids = append(pids, pid)
			}
		}
	}
	return pids, nil
}

// tcpListeners is the listener table of family af.
func tcpListeners(af uint32) ([]listenRow, error) {
	raw, err := tcpTable(af)
	if err != nil {
		return nil, err
	}
	var rows []listenRow
	if af == afInet {
		for i := 0; i+24 <= len(raw); i += 24 { // MIB_TCPROW_OWNER_PID: state, localAddr, localPort, remoteAddr, remotePort, pid
			rows = append(rows, listenRow{
				addr: net.IP(append([]byte(nil), raw[i+4:i+8]...)),
				port: int(binary.BigEndian.Uint16(raw[i+8 : i+10])),
				pid:  binary.LittleEndian.Uint32(raw[i+20 : i+24]),
			})
		}
		return rows, nil
	}
	for i := 0; i+56 <= len(raw); i += 56 { // MIB_TCP6ROW_OWNER_PID: localAddr[16], scope, localPort, remoteAddr[16], scope, remotePort, state, pid
		rows = append(rows, listenRow{
			addr: net.IP(append([]byte(nil), raw[i:i+16]...)),
			port: int(binary.BigEndian.Uint16(raw[i+20 : i+22])),
			pid:  binary.LittleEndian.Uint32(raw[i+52 : i+56]),
		})
	}
	return rows, nil
}

// tcpTable is the listener table of family af, its rows as raw bytes (after the entry count).
func tcpTable(af uint32) ([]byte, error) {
	var size uint32
	procGetExtendedTcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(af), tcpTableOwnerPIDListener, 0)
	for attempt := 0; attempt < 4; attempt++ {
		if size == 0 {
			size = 4096
		}
		buf := make([]byte, size)
		r, _, _ := procGetExtendedTcpTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, uintptr(af), tcpTableOwnerPIDListener, 0)
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
