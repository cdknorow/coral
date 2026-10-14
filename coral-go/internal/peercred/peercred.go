// Package peercred tells which local user is on the other end of a loopback TCP
// request (Linux).
//
// Requests from this computer bypass Coral's API-key auth, and the dashboard is
// recognised by browser headers that any local program can forge. On a shared
// host or in a pod with sidecars, that lets another local user drive Coral as if
// it were Coral's own user. On Linux the kernel says who owns each socket: the
// client's row in /proc/net/tcp (or tcp6), local address = the request's remote
// address and remote address = the server's local address, carries the uid of
// the process that opened it. SameUser compares that uid with Coral's own.
//
// It fails closed: a row that cannot be found (after one re-read, for the race
// with accept), a TIME_WAIT or listening row, or any parse error is "not the
// same user". Elsewhere (macOS, Windows) it reports unsupported and callers keep
// their own rules.
//
// Limits: a program running as Coral's own user cannot be told apart (it could
// read Coral's files anyway); a client in another network namespace that reaches
// Coral through a proxy shows up as the proxy's socket, which is refused unless
// the proxy runs as Coral's user.
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
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Supported reports whether this platform can tell the peer's user.
var Supported = runtime.GOOS == "linux"

// procNet is where the socket tables live (a test seam).
var procNet = "/proc/net"

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

// SameUser reports whether the loopback TCP client of r runs as this process's
// user. supported is false where the kernel cannot be asked (not Linux) or the
// request is not a TCP connection over loopback: the caller applies its own rules.
// On Linux, any failure to find the owner is "not the same user" (fail closed).
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
	uid, err := PeerUID(client, server)
	if err != nil {
		return false, true
	}
	return uid == os.Geteuid(), true
}
