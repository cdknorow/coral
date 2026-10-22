package startup

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/auth"
	"github.com/subgentic/coral/internal/safego"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/firewall"
)

// serveListeners serves httpServer on every listener, in the background, with the dashboard's address and any
// remote-access warning logged; a server error goes to onErr (logged as fatal when nil).
func serveListeners(httpServer *http.Server, lns []net.Listener, cfg *config.Config, tls bool, onErr func(error)) {
	if onErr == nil {
		onErr = func(err error) { log.Printf("[FATAL] Server error: %v", err) }
	}
	go func() {
		defer safego.Recover("startup/startup:464")
		if tls {
			log.Printf("Coral dashboard: https://localhost:%d (TLS; plain http:// only from this computer)", cfg.Port)
		} else {
			log.Printf("Coral dashboard: http://localhost:%d", cfg.Port)
		}
		if w := remoteAccessWarning(cfg.Host, cfg.TLS); w != "" {
			log.Print(w)
		}
		for _, ln := range lns[1:] {
			go func(ln net.Listener) {
				defer safego.Recover("startup/startup:474")
				if err := httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
					onErr(err)
				}
			}(ln)
		}
		if err := httpServer.Serve(lns[0]); err != nil && err != http.ErrServerClosed {
			onErr(err)
		}
	}()
}

// listenAll opens one listener per address in cfg.ListenHosts(), all on one port: the first
// binds cfg.Port (0 picks a free one) and the rest bind the port it got. The automatic
// 127.0.0.1 listener is logged once. On any failure every listener opened so far is closed.
func listenAll(cfg *config.Config) ([]net.Listener, int, error) {
	if err := config.HostsError(cfg.Host); err != nil {
		return nil, 0, err
	}
	// On Windows the first listen on a network address makes Windows ask whether to allow the program, and Allow makes a rule for the
	// whole program: say so before the prompt appears (#97).
	if w := firewall.BindWarning(cfg.Host, runtime.GOOS, config.IsLoopbackHost(cfg.Host)); w != "" {
		log.Print(w)
	}
	hosts := cfg.ListenHosts()
	port := cfg.Port
	var lns []net.Listener
	for _, h := range hosts {
		ln, err := listenTCP(net.JoinHostPort(strings.Trim(h, "[]"), strconv.Itoa(port)))
		if err != nil {
			for _, open := range lns {
				open.Close()
			}
			return nil, 0, fmt.Errorf("can't listen on %s port %d (in use?): %w", displayHost(h), port, err)
		}
		if port == 0 {
			if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
				port = tcp.Port
			}
		}
		lns = append(lns, ln)
	}
	if cfg.AutoLoopback() {
		log.Printf("[startup] --host %q names only network addresses: also listening on 127.0.0.1:%d for this computer's agents, command line and dashboard (--no-auto-loopback turns it off)", cfg.Host, port)
	} else if cfg.NoAutoLoopback && !config.IsLoopbackHost(cfg.AgentHost()) {
		log.Printf("[startup] WARNING: --no-auto-loopback with only network addresses: agents started here reach Coral at %s, where only the API key or a dashboard sign-in is accepted, so their board and task calls will be refused. Leave the loopback listener on (and use --strict-local-auth on a shared computer) if Coral runs agents here.", cfg.AgentHost())
	}
	if len(hosts) > 1 {
		shown := make([]string, len(hosts))
		for i, h := range hosts {
			shown[i] = displayHost(h)
		}
		log.Printf("[startup] listening on %s, port %d", strings.Join(shown, ", "), port)
	}
	return lns, port, nil
}

func displayHost(h string) string {
	if config.IsWildcardHost(h) {
		return "every interface"
	}
	return h
}

// restrictToLoopback forces a local-only bind. When the requested host would
// have been reachable from the network it logs how to opt in, so an operator
// who passed --host or CORAL_HOST is told why the bind is local. A --host list
// that is already loopback only (127.0.0.1,::1) is kept as given (#3594, QA's
// observation): it was being collapsed to 127.0.0.1 without a word.
func restrictToLoopback(cfg *config.Config, reason string) {
	if config.IsLoopbackHost(cfg.Host) {
		return
	}
	log.Printf("[startup] remote access is off (%s): binding 127.0.0.1 instead of %q. Enable it in Settings > Privacy (remote_access_enabled=true) and restart Coral to accept network connections.", reason, cfg.Host)
	cfg.Host = "127.0.0.1"
}

// widenForRemote is the other half of restrictToLoopback: Coral listens on this computer only by default (#5124), so when remote access is on
// (the saved setting or --remote) and no host was asked for, it listens on every interface. A host that was asked for (--host, CORAL_HOST) is kept.
func widenForRemote(cfg *config.Config) {
	if cfg.HostExplicit || !config.IsLoopbackHost(cfg.Host) {
		return
	}
	cfg.Host = "0.0.0.0"
	log.Printf("[startup] remote access is on: listening on every interface, so other devices on this network can connect")
}

// maxRequestHeaderBytes caps one request's headers (Go's default is 1 MiB per connection; the tunnel allows 64 KiB in all). Past it the
// server answers 431 before any handler runs (#4602).
const maxRequestHeaderBytes = 64 << 10

// newHTTPServer is the main listener's server: the timeouts and limits every connection gets.
func newHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 10 * time.Second, // Prevents slowloris attacks
		WriteTimeout:      0,                // Disabled for WebSocket/SSE
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    maxRequestHeaderBytes,
		// Each loopback connection is attributed to its OS user, once (#95): the loopback pass is
		// only for Coral's own account.
		ConnContext: auth.ConnContext,
	}
}
