package auth

import (
	"context"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
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
			// Always allow localhost — but validate Host header to prevent
			// DNS rebinding (attacker domain resolving to 127.0.0.1)
			if isLocalhost(r) {
				// CONNECT requests (MITM proxy) have the target host in r.Host
				// (e.g. "chatgpt.com:443"), not the proxy host. Skip DNS rebinding
				// check for CONNECT since the client is verified as localhost by IP.
				if r.Method == http.MethodConnect || isValidLocalhostHost(r.Host) {
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
			returnTo := r.URL.RequestURI()
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

// isValidLocalhostHost checks that a localhost request's Host header
// is actually localhost or an IP, not an external domain (DNS rebinding defense).
func isValidLocalhostHost(host string) bool {
	if host == "" {
		return true
	}
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	if h == "" || h == "localhost" || h == "127.0.0.1" || h == "::1" {
		return true
	}
	// Allow any IP address (handles 0.0.0.0 binding)
	if net.ParseIP(h) != nil {
		return true
	}
	// Reject external domain names
	return false
}
