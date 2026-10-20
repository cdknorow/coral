package routes

import (
	"sync"
	"time"

	"github.com/coder/websocket"
)

// wsRegistry tracks the open sockets of the dashboard and terminal routes. A hijacked connection is not one http.Server.Shutdown waits for or ends, and
// its handler runs on the request's context, which Shutdown does not cancel: without this a socket stayed open (its handler running, a terminal attached)
// until the process exited, over a database that had already closed (#5366).
type wsRegistry struct {
	mu      sync.Mutex
	conns   map[*websocket.Conn]struct{}
	closing bool
	wg      sync.WaitGroup
}

// wsOpen is the process's registry.
var wsOpen = &wsRegistry{conns: map[*websocket.Conn]struct{}{}}

// wsShutdownGrace is how long CloseWebsockets waits for the peers to answer a close frame, and then for the handlers to return.
const wsShutdownGrace = 500 * time.Millisecond

// add registers conn; the returned func unregisters it when the handler returns. ok is false once the registry is closing: the caller closes the socket.
func (g *wsRegistry) add(conn *websocket.Conn) (remove func(), ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closing {
		return nil, false
	}
	g.conns[conn] = struct{}{}
	g.wg.Add(1)
	return func() {
		g.mu.Lock()
		delete(g.conns, conn)
		g.mu.Unlock()
		g.wg.Done()
	}, true
}

// closeAll ends every open socket with "going away" (1001), so a dashboard sees the server stop and reconnects, and returns once their handlers have
// returned, or after about 2*grace (1 s). Sockets that have not answered the close by then are dropped. It refuses sockets that open after it.
func (g *wsRegistry) closeAll(grace time.Duration) {
	g.mu.Lock()
	g.closing = true
	conns := make([]*websocket.Conn, 0, len(g.conns))
	for c := range g.conns {
		conns = append(conns, c)
	}
	g.mu.Unlock()

	var closing sync.WaitGroup
	for _, c := range conns {
		closing.Add(1)
		go func() {
			defer closing.Done()
			c.Close(websocket.StatusGoingAway, "Coral is stopping")
		}()
	}
	waitFor(&closing, grace)
	// CloseNow waits for a close that is still waiting on its peer (the library's own 5 s), so a straggler is dropped in its own goroutine and not waited for.
	for _, c := range conns {
		go c.CloseNow()
	}
	waitFor(&g.wg, grace)
}

// waitFor waits for wg, or d.
func waitFor(wg *sync.WaitGroup, d time.Duration) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
	}
}

// CloseWebsockets ends the open dashboard and terminal sockets for a server shutdown (#5366).
func CloseWebsockets() { wsOpen.closeAll(wsShutdownGrace) }
