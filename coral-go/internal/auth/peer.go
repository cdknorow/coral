package auth

import (
	"context"
	"log"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/peercred"
)

// The loopback pass is per user (#95, audit G4B). On Windows loopback isn't per user: another
// interactive, RDP or terminal-server user of this computer, or a process under WSL2's mirrored
// networking, reaches Coral from 127.0.0.1 too. So a loopback connection whose client socket the
// peer-user check (peercred) doesn't positively attribute to Coral's own OS user is treated as if
// require_auth_on_loopback were on for it: it always needs a credential (an agent's session token,
// the dashboard's sign-in, the API key; a command-line signature already needs the same user), even
// when strict local auth is off, and it never gets a local-only power (IsDirectLocal). Coral's own
// processes (the browser, the app, the tray, the command line, agents and hooks) are attributed to
// its user and keep everything they have.

// Peer reasons: why a loopback connection is not Coral's own user's. Short codes, for the 401's
// detail and the log; the account is never named.
const (
	PeerOtherUser    = "other_user"   // another OS user's process
	PeerUnattributed = "unattributed" // the owner couldn't be told (fail closed)
)

// connPeer is one TCP connection's attribution: worked out once, on the connection's first
// request, and kept for that connection only (never reused across connections, or by process id).
type connPeer struct {
	client, server netip.AddrPort
	once           sync.Once
	reason         string
}

type connPeerKey struct{}

// attributePeer is peercred.Attribute (a test seam: SetPeerAttribution).
var attributePeer = peercred.Attribute

// SetPeerAttribution replaces how a loopback connection is attributed and returns the function
// that puts it back: for tests in other packages, which can't make another OS user's connection.
func SetPeerAttribution(f func(client, server netip.AddrPort) (peercred.Verdict, error)) (restore func()) {
	old := attributePeer
	attributePeer = f
	return func() { attributePeer = old }
}

// ConnContext is the main server's http.Server.ConnContext: it gives each loopback TCP connection
// its attribution, worked out on its first request (in that connection's goroutine, so a socket
// table read never holds up the accept loop) and kept for the connection. A connection that is not
// TCP over loopback gets none, and the loopback pass doesn't apply to it anyway.
func ConnContext(ctx context.Context, c net.Conn) context.Context {
	client, ok1 := tcpAddrPort(c.RemoteAddr())
	server, ok2 := tcpAddrPort(c.LocalAddr())
	if !ok1 || !ok2 || !client.Addr().Unmap().IsLoopback() {
		return ctx
	}
	return context.WithValue(ctx, connPeerKey{}, &connPeer{client: client, server: server})
}

func tcpAddrPort(a net.Addr) (netip.AddrPort, bool) {
	t, ok := a.(*net.TCPAddr)
	if !ok || t == nil {
		return netip.AddrPort{}, false
	}
	return t.AddrPort(), true
}

// PeerReason is why r's loopback connection is not Coral's own user's (PeerOtherUser or
// PeerUnattributed), or "" when it is, when the system can't tell users apart (the rules from
// before #95), or when the server attributes no connections (ConnContext not installed: tests and
// embedders).
func PeerReason(r *http.Request) string {
	cp, _ := r.Context().Value(connPeerKey{}).(*connPeer)
	if cp == nil {
		return ""
	}
	cp.once.Do(func() {
		v, err := attributePeer(cp.client, cp.server)
		switch v {
		case peercred.OtherUser:
			cp.reason = PeerOtherUser
		case peercred.Unattributed:
			cp.reason = PeerUnattributed
		default:
			return
		}
		logPeer(cp.client.Addr(), cp.reason, err)
	})
	return cp.reason
}

// peerLog limits the line for a connection that is not Coral's own user's to one a minute per
// client address and reason, so a mis-attribution on a real machine can be found in the log
// without a line per request.
var peerLog = struct {
	sync.Mutex
	last map[string]time.Time
}{last: map[string]time.Time{}}

func logPeer(addr netip.Addr, reason string, err error) {
	key := addr.String() + " " + reason
	peerLog.Lock()
	if time.Since(peerLog.last[key]) < time.Minute {
		peerLog.Unlock()
		return
	}
	if len(peerLog.last) > 1024 {
		peerLog.last = map[string]time.Time{}
	}
	peerLog.last[key] = time.Now()
	peerLog.Unlock()
	why := "another user's process"
	if reason == PeerUnattributed {
		why = "a process whose user couldn't be told"
		if err != nil {
			why += " (" + err.Error() + ")"
		}
	}
	log.Printf("[auth] a loopback connection from %s is %s (%s): it needs a credential like a request from another computer, and gets no local-only power", addr, why, reason)
}
