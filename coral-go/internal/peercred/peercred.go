// Package peercred tells which local user is on the other end of a loopback TCP
// request (Linux, macOS, Windows).
//
// Requests from this computer bypass Coral's API-key auth, and the dashboard is
// recognised by browser headers that any local program can forge. On a shared
// host or in a pod with sidecars, that lets another local user drive Coral as if
// it were Coral's own user. The kernel knows who owns each socket. The client's
// socket is the one whose local address is the request's remote address and
// whose remote address is the server's local address:
//   - Linux: its row in /proc/net/tcp (or tcp6) carries the uid of the process
//     that opened it;
//   - macOS: its entry in the net.inet.tcp.pcblist_n sysctl carries the socket's
//     uid (the same table netstat reads);
//   - Windows: its row in GetExtendedTcpTable carries the owning process id, whose
//     token names the user's SID.
//
// SameUser compares that user with Coral's own.
//
// It fails closed: a socket that cannot be found (after one re-read, for the race
// with accept), a TIME_WAIT or listening row, two rows that disagree, a process
// that cannot be opened, or any parse error is "not the same user". On other
// systems it reports unsupported and callers keep their own rules.
//
// Limits: a program running as Coral's own user cannot be told apart (it could
// read Coral's files anyway); a client in another network namespace that reaches
// Coral through a proxy shows up as the proxy's socket, which is refused unless
// the proxy runs as Coral's user. On Windows, a process that exits between the
// connect and the check is refused, and the pid could in theory be reused by then.
//
// Used by the cloud write routes (#3149). Worth considering later for other
// routes that act for the user from a loopback request: /send, /keys and
// /answer-prompt, auto-accept enable, and the settings writes.
package peercred

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// procNet is where the socket tables live (a test seam).
var procNet = "/proc/net"

// ErrLayout: the kernel's socket table isn't in a layout this build knows (macOS reads
// xnu's private structs). SameUser then answers unsupported, never a guess.
var ErrLayout = errors.New("peercred: the kernel's socket table has an unknown layout")

// ownerCheck is ownerIsMe (a test seam).
var ownerCheck = ownerIsMe

// ErrNotFound: no live socket row for the connection.
var ErrNotFound = errors.New("peercred: no socket row for the connection")

// TCP states (include/net/tcp_states.h) a client socket in the middle of a request cannot be in.
const (
	stateTimeWait = 0x06
	stateClose    = 0x07
	stateListen   = 0x0A
)

// PeerUID returns the uid owning the client socket of the loopback connection
// client -> server, from the kernel's socket tables. It re-reads once if the row
// is missing.
func PeerUID(client, server netip.AddrPort) (int, error) {
	for attempt := 0; attempt < 2; attempt++ {
		uid, err := lookup(client, server)
		if !errors.Is(err, ErrNotFound) {
			return uid, err
		}
	}
	return -1, ErrNotFound
}

func lookup(client, server netip.AddrPort) (int, error) {
	client, server = unmap(client), unmap(server)
	for _, name := range []string{"tcp", "tcp6"} {
		uid, err := scan(filepath.Join(procNet, name), client, server)
		if err == nil {
			return uid, nil
		}
		if !errors.Is(err, ErrNotFound) && !errors.Is(err, os.ErrNotExist) {
			return -1, err
		}
	}
	return -1, ErrNotFound
}

// scan finds the row whose local address is client and remote address is server.
func scan(path string, client, server netip.AddrPort) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return -1, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	header := true
	for sc.Scan() {
		if header { // "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid ..."
			header = false
			continue
		}
		f := strings.Fields(sc.Text())
		if len(f) < 8 {
			continue
		}
		local, err1 := parseAddr(f[1])
		remote, err2 := parseAddr(f[2])
		if err1 != nil || err2 != nil || local != client || remote != server {
			continue
		}
		st, err := strconv.ParseUint(f[3], 16, 8)
		if err != nil {
			return -1, fmt.Errorf("peercred: bad state %q", f[3])
		}
		if st == stateTimeWait || st == stateClose || st == stateListen {
			continue // not a live client socket (TIME_WAIT rows also show uid 0)
		}
		uid, err := strconv.Atoi(f[7])
		if err != nil || uid < 0 {
			return -1, fmt.Errorf("peercred: bad uid %q", f[7])
		}
		return uid, nil
	}
	if err := sc.Err(); err != nil {
		return -1, err
	}
	return -1, ErrNotFound
}

// parseAddr reads "0100007F:1F90" (IPv4) or a 32-hex-digit IPv6 address and a
// port. The kernel prints each 32-bit word of the address as the host-order
// value of its network-order bytes, so on a little-endian machine every word's
// bytes are reversed; the port is plain host order.
func parseAddr(s string) (netip.AddrPort, error) {
	host, port, ok := strings.Cut(s, ":")
	if !ok || (len(host) != 8 && len(host) != 32) || len(port) != 4 {
		return netip.AddrPort{}, fmt.Errorf("peercred: bad address %q", s)
	}
	raw, err := hex.DecodeString(host)
	if err != nil {
		return netip.AddrPort{}, err
	}
	p, err := strconv.ParseUint(port, 16, 16)
	if err != nil {
		return netip.AddrPort{}, err
	}
	for i := 0; i < len(raw); i += 4 {
		if hostLittleEndian {
			raw[i], raw[i+1], raw[i+2], raw[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
		}
	}
	addr, _ := netip.AddrFromSlice(raw)
	return unmap(netip.AddrPortFrom(addr, uint16(p))), nil
}

// hostLittleEndian: the byte order the kernel printed the words in (a test seam).
var hostLittleEndian = littleEndian()

func littleEndian() bool {
	switch runtime.GOARCH {
	case "ppc64", "s390x", "mips", "mips64", "sparc64":
		return false
	}
	return true
}

// unmap turns ::ffff:a.b.c.d into a.b.c.d, so a dual-stack listener's view of an
// IPv4 client matches the client's own IPv4 row.
func unmap(ap netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}

// Verdict is whose a loopback connection's client socket is.
type Verdict int

const (
	// Unsupported: this system can't be asked, or its socket table has a layout this build doesn't
	// know. The caller applies the rules for a system without the check.
	Unsupported Verdict = iota
	// Self: the socket's owner is positively this process's user (Windows: the same token user SID,
	// elevated or not).
	Self
	// OtherUser: the socket's owner is another user.
	OtherUser
	// Unattributed: the owner couldn't be told: no row (after a re-read), two rows that disagree, a
	// process or token that can't be opened, any error. Fail closed: as another user's.
	Unattributed
)

// retryWait is how long Attribute waits before re-reading a socket table that has no row for the
// connection yet (the race with accept, or a short-lived client).
var retryWait = 20 * time.Millisecond

// Attribute tells whose the loopback client socket client -> server is (#95). Only a positive
// answer is Self; anything that can't be told is Unattributed, never a guess. err says why for
// Unattributed (and for a layout this build doesn't know).
func Attribute(client, server netip.AddrPort) (Verdict, error) {
	if !Supported {
		return Unsupported, nil
	}
	client, server = unmap(client), unmap(server)
	var err error
	for attempt := 0; attempt < 2; attempt++ { // re-read once: the race with accept
		if attempt > 0 {
			time.Sleep(retryWait)
		}
		var me bool
		if me, err = ownerCheck(client, server); err == nil {
			if me {
				return Self, nil
			}
			return OtherUser, nil
		}
		if !errors.Is(err, ErrNotFound) {
			break
		}
	}
	if errors.Is(err, ErrLayout) {
		logLayoutOnce(err)
		return Unsupported, err // this OS version's table isn't one we know: as an unsupported system
	}
	return Unattributed, err
}

// SameUser reports whether the loopback TCP client of r runs as this process's
// user. supported is false where the kernel cannot be asked (see Supported) or
// the request is not a TCP connection over loopback: the caller applies its own
// rules. Where it is supported, any failure to find the owner is "not the same
// user" (fail closed).
func SameUser(r *http.Request) (same, supported bool) {
	if !Supported {
		return false, false
	}
	client, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return false, true
	}
	la, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	tcp, ok := la.(*net.TCPAddr)
	if !ok || tcp == nil {
		return false, true
	}
	server := tcp.AddrPort()
	if !client.Addr().Unmap().IsLoopback() {
		return false, false // not a local request: not this package's question
	}
	switch v, err := Attribute(client, server); v {
	case Self:
		return true, true
	case Unsupported:
		return false, false
	case Unattributed:
		logRefused(unmap(client), err)
	}
	return false, true
}

var layoutOnce sync.Once

// logLayoutOnce says, once per process, that the kernel's socket table has a layout this
// build doesn't know (a macOS update that changed xnu's private structs): requests are then
// handled as on a system without the check, never refused as another user's or accepted as
// Coral's own on a guess.
func logLayoutOnce(err error) {
	layoutOnce.Do(func() {
		log.Printf("[peercred] peer user unknown on this OS version: %v (%s/%s); loopback requests get the rules for systems without a peer check", err, runtime.GOOS, runtime.GOARCH)
	})
}

// refusedLog limits the fail-closed log line to one a minute.
var refusedLog struct {
	sync.Mutex
	last time.Time
}

// logRefused says why a loopback request was refused as another user's when its owner could
// not be determined.
func logRefused(client netip.AddrPort, err error) {
	refusedLog.Lock()
	defer refusedLog.Unlock()
	if time.Since(refusedLog.last) < time.Minute {
		return
	}
	refusedLog.last = time.Now()
	log.Printf("[peercred] could not tell which user owns the loopback connection from %s (%v): refused as another user", client, err)
}
