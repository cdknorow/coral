package testguard

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Agent shells export the live server's address (CORAL_URL) and the live
// tmux server (TMUX), so a test run from one inherits both. In test binaries,
// init drops TMUX and TMUX_PANE, so nothing reaches the live tmux server
// through the default socket lookup, and makes http.DefaultTransport refuse
// connections to the live Coral server. Clients built on the default
// transport or a Clone of it are covered; a test talks to an httptest server
// instead.
func init() {
	if !enabled() {
		return
	}
	os.Unsetenv("TMUX")
	os.Unsetenv("TMUX_PANE")
	coralURL := os.Getenv("CORAL_URL")
	if os.Getenv(TestURLEnv) == "1" {
		coralURL = "" // a test harness pointed CORAL_URL at its own server
	}
	live := liveServerAddrs(coralURL, os.Getenv("CORAL_PORT"))
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		dial := t.DialContext
		if dial == nil {
			dial = (&net.Dialer{}).DialContext
		}
		t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			checkDial(live, addr)
			return dial(ctx, network, addr)
		}
	}
}

// TestURLEnv, set to "1" in a child process's environment, tells the guard
// that the child's CORAL_URL is the test's own httptest server, not the
// inherited live one. The default port stays refused.
const TestURLEnv = "CORAL_TESTGUARD_TEST_URL"

// defaultPort is the port a Coral server listens on unless told otherwise.
const defaultPort = "8420"

// liveServerAddrs returns the host:port values a test must not dial: the
// inherited CORAL_URL and the loopback addresses on the default port and on
// CORAL_PORT.
func liveServerAddrs(coralURL, coralPort string) map[string]bool {
	addrs := map[string]bool{}
	ports := []string{defaultPort}
	if coralPort != "" {
		ports = append(ports, coralPort)
	}
	if u, err := url.Parse(strings.TrimSpace(coralURL)); err == nil && u.Host != "" {
		port := u.Port()
		if port == "" {
			port = map[string]string{"https": "443"}[u.Scheme]
			if port == "" {
				port = "80"
			}
		}
		addrs[normalizeAddr(net.JoinHostPort(u.Hostname(), port))] = true
		ports = append(ports, port)
	}
	for _, port := range ports {
		for _, host := range []string{"localhost", "127.0.0.1", "::1"} {
			addrs[normalizeAddr(net.JoinHostPort(host, port))] = true
		}
	}
	return addrs
}

func normalizeAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return strings.ToLower(addr)
	}
	return net.JoinHostPort(strings.ToLower(host), port)
}

// checkDial panics when a test dials the live server. A panic, not an
// error: callers often turn a failed request into a quiet fallback, and this
// must stop the run.
func checkDial(live map[string]bool, addr string) {
	if live[normalizeAddr(addr)] {
		panic(fmt.Sprintf("testguard: test dialed %s, the live Coral server (inherited CORAL_URL or the default port); "+
			"point the test at an httptest server", addr))
	}
}
