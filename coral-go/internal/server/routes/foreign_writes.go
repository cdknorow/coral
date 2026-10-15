package routes

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/cdknorow/coral/internal/cloud"
)

// RefuseForeignBrowserWrites refuses a state-changing request a browser sends from
// any page but this server's own (#3511, Security's #3509 finding 1). A page on
// another port of localhost, or any other site, can make a browser send a write to
// Coral: a "simple" POST goes out with no preflight, and the sign-in cookie is
// SameSite=Strict, but every port of localhost is the same site. So a POST, PUT,
// PATCH or DELETE that carries browser evidence (an Origin or Sec-Fetch-Site
// header) must come from this very origin (ownBrowserOrigin), in every mode,
// before any auth check or the default mode's localhost allowance.
//
// Not affected: requests with no browser evidence (curl, scripts, the agents' tools,
// the command line, coral-tray: the identity rules decide those) and requests the
// Coral Hub connector stamped (the hub checks its own browser's origin). GET, HEAD
// and OPTIONS are left to their routes; the websocket upgrades check their Origin
// themselves (wsAcceptOptions).
func RefuseForeignBrowserWrites(next http.Handler) http.Handler {
	return ForeignBrowserWrites(nil)(next)
}

// ForeignBrowserWrites is RefuseForeignBrowserWrites behind a reverse proxy that rewrites
// Host: forwardedHost (auth.KeyStore.ForwardedHost: X-Forwarded-Host, believed only from a
// trusted proxy) names a second host the browser may have reached Coral on.
func ForeignBrowserWrites(forwardedHost func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler { return refuseForeignBrowserWrites(next, forwardedHost) }
}

func refuseForeignBrowserWrites(next http.Handler, forwardedHost func(*http.Request) string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A browser sends Origin and Sec-Fetch-Site once. A request with either repeated is
		// refused whatever its method (#3524): checks that read only the first value (here,
		// CORS, the WebSocket accept) must not disagree about which one counts.
		if len(r.Header.Values("Origin")) > 1 || len(r.Header.Values("Sec-Fetch-Site")) > 1 {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "foreign_origin",
				"message": "Coral accepts changes only from its own page"})
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		browser := r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != ""
		fwd := ""
		if browser && forwardedHost != nil {
			fwd = forwardedHost(r)
		}
		if browser && !cloud.TunneledAuthentic(r) && !ownBrowserOrigin(r, fwd) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "foreign_origin",
				"message": "Coral accepts changes only from its own page"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ownBrowserOrigin: an Origin, when sent, names exactly the host and port the browser
// reached Coral on (case-insensitive host, no prefix or suffix match, so
// localhost.evil.test, 127.0.0.1 for a dashboard on localhost, or another port are
// all foreign; "null" and custom schemes are foreign); Sec-Fetch-Site, when sent, is
// same-origin or none (the user's own navigation). The scheme isn't compared: no other
// server can serve this host and port, and behind a TLS-terminating proxy the request
// arrives over http while the page is https. A Sec-Fetch-Site with no Origin is
// accepted only as same-origin or none. Behind a reverse proxy that rewrites Host, the
// trusted proxy's X-Forwarded-Host (when given) counts as the request's own host too.
func ownBrowserOrigin(r *http.Request, forwardedHost string) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
			u.User != nil || (u.Path != "" && u.Path != "/") ||
			!(strings.EqualFold(u.Host, r.Host) || (forwardedHost != "" && strings.EqualFold(u.Host, forwardedHost))) {
			return false
		}
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return true
	}
	return false
}
