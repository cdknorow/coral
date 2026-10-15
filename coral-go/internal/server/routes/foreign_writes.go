package routes

import (
	"net/http"

	"github.com/cdknorow/coral/internal/cloud"
)

// RefuseForeignBrowserWrites refuses a state-changing request a browser sends from
// any page but this server's own (#3511, Security's #3509 finding 1). A page on
// another port of localhost, or any other site, can make a browser send a write to
// Coral: a "simple" POST goes out with no preflight, and the sign-in cookie is
// SameSite=Strict, but every port of localhost is the same site. So a POST, PUT,
// PATCH or DELETE that carries browser evidence (an Origin or Sec-Fetch-Site
// header) must come from this very origin (sameOriginFetch: the Origin's host is
// the request's Host, and Sec-Fetch-Site, when sent, is same-origin), in every mode.
//
// Not affected: requests with no browser evidence (curl, scripts, the agents' tools,
// the command line, coral-tray: the identity rules decide those) and requests the
// Coral Hub connector stamped (the hub checks its own browser's origin). GET, HEAD
// and OPTIONS are left to their routes; the websocket upgrades check their Origin
// themselves (wsAcceptOptions).
func RefuseForeignBrowserWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		browser := r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != ""
		if browser && !cloud.TunneledAuthentic(r) && !sameOriginFetch(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "foreign_origin",
				"message": "Coral accepts changes only from its own page"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
