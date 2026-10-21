package startup

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/safego"
)

// Native TLS (#3355). Remote access on plain HTTP sends the API key, the login cookie
// and every terminal keystroke across the network unencrypted. With --tls-cert and
// --tls-key, the same port also speaks HTTPS (and so WSS): the first byte of each new
// connection says which. Plain HTTP stays available to this computer only, because
// everything local (coral-board, the hooks, agents' CORAL_URL, the hub tunnel) talks
// to http://localhost; from the network, plain HTTP is answered with a redirect to
// https (GET/HEAD) or refused, and is never served.

// loadTLSConfig reads the certificate pair. Neither file: no TLS (nil). Only one, or a
// pair that does not load: an error, so Coral refuses to start rather than run without
// the encryption that was asked for.
func loadTLSConfig(certFile, keyFile string) (*tls.Config, error) {
	certFile, keyFile = strings.TrimSpace(certFile), strings.TrimSpace(keyFile)
	switch {
	case certFile == "" && keyFile == "":
		return nil, nil
	case certFile == "" || keyFile == "":
		return nil, errors.New("TLS needs both --tls-cert and --tls-key (or CORAL_TLS_CERT and CORAL_TLS_KEY): only one was given")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("TLS certificate %s / key %s: %w", certFile, keyFile, err)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		// HTTP/1.1 only: the terminal and live feeds are WebSockets, which need it.
		NextProtos: []string{"http/1.1"},
	}, nil
}

// tlsHandshakeByte is the first byte of a TLS record carrying a handshake.
const tlsHandshakeByte = 0x16

// sniffDeadline bounds how long a new connection may take to send its first byte, so
// a silent client cannot hold a sniffing goroutine forever.
var sniffDeadline = 10 * time.Second

// sniffListener hands http.Server a *tls.Conn for a connection that starts a TLS
// handshake and the plain connection otherwise. net/http completes the handshake
// itself for a *tls.Conn and sets Request.TLS, so the rest of Coral (the Secure
// cookie flag, for one) sees which it was.
type sniffListener struct {
	net.Listener
	cfg   *tls.Config
	conns chan net.Conn
	errc  chan error
	once  sync.Once
	done  chan struct{}
}

func newSniffListener(ln net.Listener, cfg *tls.Config) *sniffListener {
	l := &sniffListener{Listener: ln, cfg: cfg, conns: make(chan net.Conn), errc: make(chan error, 1), done: make(chan struct{})}
	safego.Restart("startup/tls:l.acceptLoop", func() { l.acceptLoop() })
	return l
}

func (l *sniffListener) acceptLoop() {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			select {
			case l.errc <- err:
			case <-l.done:
			}
			return
		}
		go func() { defer safego.Recover("startup/tls:l.sniff"); l.sniff(c) }()
	}
}

// sniff reads the first byte without consuming it, then hands the connection on.
func (l *sniffListener) sniff(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(sniffDeadline))
	br := bufio.NewReader(c)
	first, err := br.Peek(1)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		c.Close()
		return
	}
	var out net.Conn = &peekedConn{Conn: c, r: br}
	if first[0] == tlsHandshakeByte {
		out = tls.Server(out, l.cfg)
	}
	select {
	case l.conns <- out:
	case <-l.done:
		c.Close()
	}
}

func (l *sniffListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case err := <-l.errc:
		return nil, err
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *sniffListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.Listener.Close()
}

// peekedConn reads through the buffer that holds the sniffed byte.
type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *peekedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// requireTLSFromNetwork serves plain HTTP only to this computer once TLS is on. A plain
// request from the network is redirected to https (GET and HEAD, so a browser that
// typed http:// lands on the right page) or refused; it never reaches Coral's routes.
func requireTLSFromNetwork(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil || remoteIsLoopback(r.RemoteAddr) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Connection", "close")
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.Host != "" {
			http.Redirect(w, r, "https://"+r.Host+r.URL.RequestURI(), http.StatusPermanentRedirect)
			return
		}
		http.Error(w, "This Coral accepts only HTTPS from other computers: use https://", http.StatusBadRequest)
	})
}

func remoteIsLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// remoteAccessWarning is logged at startup when Coral is reachable from the network
// without TLS. Empty when there is nothing to warn about.
func remoteAccessWarning(host string, tlsOn bool) string {
	if config.IsLoopbackHost(host) || tlsOn {
		return ""
	}
	return "[startup] WARNING: remote access is on over plain HTTP: the API key, the login cookie and terminal traffic cross the network unencrypted. " +
		"Use Coral Hub (TLS), a TLS reverse proxy, or start Coral with --tls-cert and --tls-key."
}
