package startup

import (
	"context"
	"net"
)

// listenTCP opens one of Coral's own listeners. On Windows it holds its port exclusively (listenControl, SO_EXCLUSIVEADDRUSE),
// so no other socket can bind a more specific address on Coral's port while Coral runs: with Coral on 0.0.0.0, a second socket
// could otherwise bind 127.0.0.1 on the same port and take this computer's loopback connections (agents, the command line),
// the more specific address winning (#224, reproduced by the Security Reviewer). Elsewhere it is net.Listen.
func listenTCP(addr string) (net.Listener, error) {
	lc := net.ListenConfig{Control: listenControl}
	return lc.Listen(context.Background(), "tcp", addr)
}
