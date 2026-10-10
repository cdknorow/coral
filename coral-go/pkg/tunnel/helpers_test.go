package tunnel

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const testToken = "tok-secret"

// hubHarness is a hub /tunnel endpoint on a real socket. Sessions it accepts
// are published on Sessions.
type hubHarness struct {
	Server   *httptest.Server
	Sessions chan *Session
	Cfg      SessionConfig
}

func newHub(t *testing.T, cfg SessionConfig) *hubHarness {
	t.Helper()
	h := &hubHarness{Sessions: make(chan *Session, 16), Cfg: cfg}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := AcceptHTTP(w, r, func(_ context.Context, tok string) (any, error) {
			if tok != testToken {
				return nil, ErrUnauthorized
			}
			return "server-1", nil
		}, h.Cfg)
		if err != nil {
			return
		}
		h.Sessions <- s
	}))
	t.Cleanup(h.Server.Close)
	return h
}

func (h *hubHarness) URL() string { return "ws" + strings.TrimPrefix(h.Server.URL, "http") }

func (h *hubHarness) next(t *testing.T) *Session {
	t.Helper()
	select {
	case s := <-h.Sessions:
		t.Cleanup(func() { s.Close() })
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("no session")
		return nil
	}
}

// connect dials the hub with a LocalHandler against local and returns both
// ends.
func connect(t *testing.T, h *hubHarness, local http.Handler, mod func(*LocalHandler)) (*Session, *ClientSession) {
	t.Helper()
	ls := httptest.NewServer(local)
	t.Cleanup(ls.Close)
	lh := &LocalHandler{BaseURL: ls.URL}
	if mod != nil {
		mod(lh)
	}
	cs, err := Dial(context.Background(), h.URL(), testToken, ClientOptions{
		SessionConfig: h.Cfg, Handler: lh, CoralVersion: "test", AllowInsecure: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { cs.Close() })
	return h.next(t), cs
}

// flakyProxy is a TCP forwarder whose connections can be severed (drop) or
// frozen (blackhole: bytes stop flowing but sockets stay open).
type flakyProxy struct {
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
	black bool
}

func newFlakyProxy(t *testing.T, target string) *flakyProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := &flakyProxy{ln: ln}
	t.Cleanup(func() { ln.Close(); p.Drop() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			u, err := net.Dial("tcp", target)
			if err != nil {
				c.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, c, u)
			p.mu.Unlock()
			go p.pipe(c, u)
			go p.pipe(u, c)
		}
	}()
	return p
}

func (p *flakyProxy) pipe(dst, src net.Conn) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			for {
				p.mu.Lock()
				b := p.black
				p.mu.Unlock()
				if !b {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			dst.Close()
			return
		}
	}
}

func (p *flakyProxy) Addr() string { return p.ln.Addr().String() }
func (p *flakyProxy) Drop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		c.Close()
	}
	p.conns = nil
}
func (p *flakyProxy) Blackhole(on bool) { p.mu.Lock(); p.black = on; p.mu.Unlock() }

func goroutines() int { return runtime.NumGoroutine() }
