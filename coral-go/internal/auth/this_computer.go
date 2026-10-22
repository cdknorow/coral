package auth

import (
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

// IsThisComputerPeer reports whether r's connection comes from this computer itself: a loopback peer (IsDirectLocalPeer's rule), or one of this computer's own interface addresses with no
// reverse-proxy header (a tool dialing the address Coral listens on). Decided by the connection's address, never a header. It is for answers that are public but meant for this computer's own
// tools (the listener proof, #6103), so a host on the network cannot use them at all.
func IsThisComputerPeer(r *http.Request) bool {
	if IsDirectLocalPeer(r) {
		return true
	}
	if hasProxyHeaders(r) {
		return false
	}
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	return err == nil && ownAddrs.has(ap.Addr().Unmap().WithZone(""))
}

// ownAddrs is the set of this computer's interface addresses, read again once the last read is a few seconds old (an interface comes and goes, and reading them is a system call).
var ownAddrs = &addrSet{ttl: 5 * time.Second, list: func() []net.Addr { a, _ := net.InterfaceAddrs(); return a }}

type addrSet struct {
	mu   sync.Mutex
	ttl  time.Duration
	list func() []net.Addr
	at   time.Time
	set  map[netip.Addr]bool
}

func (s *addrSet) has(a netip.Addr) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.set == nil || time.Since(s.at) > s.ttl {
		s.set = map[netip.Addr]bool{}
		for _, addr := range s.list() {
			if ipn, ok := addr.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(ipn.IP); ok {
					s.set[ip.Unmap().WithZone("")] = true
				}
			}
		}
		s.at = time.Now()
	}
	return s.set[a]
}
