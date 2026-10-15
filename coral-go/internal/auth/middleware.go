package auth

import (
	"context"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/cdknorow/coral/internal/httputil"
)

// Method is how the middleware let a request in. Handlers that must tell a
// logged-in browser from a script read it with MethodFrom (#3182: only a
// dashboard turns auto-accept on).
type Method int

const (
	MethodNone     Method = iota // not set: no auth middleware in front (tests, embedders)
	MethodLoopback               // a localhost peer (no credentials checked)
	MethodAPIKey                 // the API key, in a header or the query string
	MethodSession                // a session cookie from the /auth login (or the QR link)
)

type methodKey struct{}

// MethodFrom returns how the request was authenticated.
func MethodFrom(ctx context.Context) Method {
	m, _ := ctx.Value(methodKey{}).(Method)
	return m
}

func withMethod(r *http.Request, m Method) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), methodKey{}, m))
}

// Middleware returns an HTTP middleware that enforces API key authentication.
// Localhost requests bypass auth entirely. Static assets and the /auth page
// are accessible without authentication.
func Middleware(ks *KeyStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// A sign-in link (?login_token=…, #3387): from here or another computer.
			if ks.handleLoginToken(w, r) {
				return
			}
			// CONNECT requests (the local MITM proxy for agent browsing) carry the target
			// host in r.Host (e.g. "chatgpt.com:443"), not a proxy host, and no forwarding
			// header; they are trusted by loopback IP alone, as before (#3585 keeps this).
			if r.Method == http.MethodConnect && isLocalhost(r) {
				next.ServeHTTP(w, withMethod(r, MethodLoopback))
				return
			}
			// A direct local client (a loopback peer with no reverse-proxy header) reaches the
			// handlers as MethodLoopback. A request a local reverse proxy forwarded is not direct
			// and falls through to key/session auth, so a proxy that rewrites Host to localhost
			// can't lend its clients this pass (#3585). When require_auth_on_loopback is on, this
			// pass is NOT a credential by itself: the identity middleware then runs in strict mode
			// and refuses a direct-local request that carries no agent token, session or CLI
			// signature (so a signed command line, coral open and agents still work, a tokenless
			// terminal does not) — but a proxied request never reaches that strict path. The Host
			// header is still validated against DNS rebinding.
			if IsDirectLocalPeer(r) {
				if isValidLocalhostHost(r.Host) {
					next.ServeHTTP(w, withMethod(r, MethodLoopback))
					return
				}
				// DNS rebinding attempt: localhost IP but external domain Host
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}

			// Allow the auth page, sign-out (it only ends the caller's own session, so an
			// expired one can still clear its cookie), static assets and the manifest.
			path := r.URL.Path
			if path == "/auth" || path == "/auth/logout" || strings.HasPrefix(path, "/static/") ||
				path == "/manifest.json" || path == "/favicon.ico" {
				next.ServeHTTP(w, r)
				return
			}

			// Check API key (header or query param)
			// Only FAILED attempts count against the rate limit. Counting valid
			// keys too would throttle any legitimate programmatic client (such
			// as a Coral hub polling this server every few seconds) into 429s.
			if key := extractAPIKey(r); key != "" {
				if ks.RateLimited(clientIP(r)) {
					// Too many wrong keys from this address: a limit on signing in (#3434).
					w.Header().Set(httputil.ActionLimitHeader, "action")
					http.Error(w, "Too many authentication attempts", http.StatusTooManyRequests)
					return
				}
				if ks.ValidateKey(key) {
					// Auto-create session for API key auth via query param
					if r.URL.Query().Get("api_key") != "" {
						token, err := ks.CreateSession(clientIP(r), r.UserAgent())
						if err != nil {
							log.Printf("[auth] failed to create session: %v", err)
						} else {
							ks.setSessionCookie(w, r, token)
						}
					}
					next.ServeHTTP(w, withMethod(r, MethodAPIKey))
					return
				}
				ks.RecordFailure(clientIP(r))
				log.Printf("[auth] invalid API key from %s", clientIP(r))
			}

			// Check session cookie
			if token := extractSessionCookie(r); token != "" {
				if ks.ValidateSession(token) {
					next.ServeHTTP(w, withMethod(r, MethodSession))
					return
				}
			}

			// No valid auth — redirect browser requests, reject API requests
			if strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/ws/") {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			returnTo := SafeReturnTo(r.URL.RequestURI())
			http.Redirect(w, r, "/auth?return_to="+url.QueryEscape(returnTo), http.StatusTemporaryRedirect)
		})
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isValidLocalhostHost checks that a localhost request's Host header names this server, not
// an external domain (the DNS-rebinding defence). An empty Host passes (HTTP/1.0).
func isValidLocalhostHost(host string) bool {
	return host == "" || AcceptedLocalHost(host)
}

// acceptedHosts are the names a request from this computer may give as its Host (#3594): the
// loopback names, and the addresses Coral listens on. Set once at startup (SetListenHosts);
// until then, and when Coral listens on every interface, any IP address is accepted.
var acceptedHosts atomic.Pointer[hostSet]

type hostSet struct {
	anyIP bool
	names map[string]bool
}

// SetListenHosts records the addresses Coral listens on (config.ListenHosts): with them, a
// Host naming another IP is refused like an external domain. An empty entry, 0.0.0.0 or ::
// (every interface) keeps any IP address acceptable.
func SetListenHosts(hosts []string) {
	set := &hostSet{names: map[string]bool{}}
	for _, h := range hosts {
		h = strings.ToLower(strings.Trim(strings.TrimSpace(h), "[]"))
		switch h {
		case "", "0.0.0.0", "::":
			set.anyIP = true
		default:
			set.names[h] = true
		}
	}
	acceptedHosts.Store(set)
}

// AcceptedLocalHost reports whether host (a Host header, with or without a port) names this
// server: localhost, a loopback address, or an address Coral listens on.
func AcceptedLocalHost(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	h = strings.ToLower(strings.Trim(h, "[]"))
	if h == "" {
		return false
	}
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	if ip != nil && ip.IsLoopback() {
		return true
	}
	set := acceptedHosts.Load()
	if set == nil || set.anyIP {
		return ip != nil // every interface: any address may be this server's
	}
	if set.names[h] {
		return true
	}
	return ip != nil && set.names[ip.String()]
}

// SafeReturnTo returns p when it is a path on this origin, else "/" (#3418): it must
// start with one "/", and contain no backslash (browsers read "/\" as "//", another
// host) and no control character. The /auth page applies the same rule.
func SafeReturnTo(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsRune(p, '\\') {
		return "/"
	}
	for _, c := range p {
		if c < 0x20 || c == 0x7f {
			return "/"
		}
	}
	return p
}
