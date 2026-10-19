// The headers a forwarded request carries and the ones scrubbed (moved out of local.go, #5091; no behaviour change).

package tunnel

import (
	"net/http"
	"regexp"
)

// BodyLengthHeader carries the machine's declared response length across the (chunk-framed) tunnel.
const BodyLengthHeader = "X-Coral-Content-Length"

// ViaHeader marks a request that the connector executed on behalf of the Cloud Hub.
// Coral's file endpoints apply strict bounds to such requests (no absolute paths
// outside the session's repository, a sensitive-path denylist, generic errors).
const ViaHeader = "X-Coral-Via-Tunnel"

var allowedMethods = map[string]bool{
	http.MethodGet: true, http.MethodHead: true, http.MethodPost: true,
	http.MethodPut: true, http.MethodPatch: true, http.MethodDelete: true,
}

// forwardRequestHeaders is the allowlist of hub-supplied request headers that
// reach the local API. Everything else (cookies, authorization, forwarding and
// fetch-metadata headers, CSRF tokens, anything a compromised hub might invent
// for the local server to trust) is dropped. Keys are in canonical form.
var forwardRequestHeaders = map[string]bool{
	"Accept": true, "Accept-Language": true, "Content-Type": true, "Range": true, "If-Range": true,
	"If-Match": true, "If-None-Match": true, "If-Modified-Since": true, "If-Unmodified-Since": true,
	"Cache-Control": true, "Last-Event-Id": true, "User-Agent": true,
	// The Coral Hub user behind a board write (#3377, #3404): honoured only on board
	// writes, validated by the server (hub-user-<8 hex>).
	"X-Coral-Hub-User": true,
	// The other Coral a relayed cross-team message comes from (#3489): set only by the
	// hub's relay path, forwarded only on POST /api/board/{project}/external-messages,
	// and trusted by the server only on a request this connector stamped.
	"X-Coral-Hub-Server": true,
	// Coral Hub's notice about a team link (#3489): forwarded only on
	// POST /api/board/{project}/hub-notice, trusted only on a request this connector stamped.
	"X-Coral-Hub-Notice": true,
}

// HubUserHeader names the Coral Hub user behind a tunnel board write: the sender of a
// chat post (#3377), and who acted when Coral Hub acts as the Operator (#3404).
const HubUserHeader = "X-Coral-Hub-User"

// hubUserRoute is where the Coral Hub user header is forwarded: a board's own routes,
// /api/board/{project}/... (#3404). It is dropped everywhere else, so no other handler
// can ever see it.
var hubUserRoute = regexp.MustCompile(`^/api/board/[^/]+/.+$`)

// ForwardsHubUser reports whether X-Coral-Hub-User is forwarded for a request: a write
// (not GET or HEAD) to /api/board/{project}/... The hub (pkg/hubproxy) and the connector
// apply the same rule.
func ForwardsHubUser(method, decodedPath string) bool {
	if method == http.MethodGet || method == http.MethodHead {
		return false
	}
	return hubUserRoute.MatchString(decodedPath)
}

// HubServerHeader names the other Coral (its Coral Hub server id) that a relayed
// cross-team message comes from (#3489). Coral Hub sets it on the relay path only and
// strips it from browser requests.
const HubServerHeader = "X-Coral-Hub-Server"

// hubServerRoute is where the Coral Hub server header is forwarded: a relayed message, and a
// relayed file (cross-team artifacts).
var hubServerRoute = regexp.MustCompile(`^/api/board/[^/]+/external-(messages|artifacts)$`)

// ForwardsHubServer reports whether X-Coral-Hub-Server is forwarded for a request: a POST
// to /api/board/{project}/external-messages or .../external-artifacts, and nothing else.
func ForwardsHubServer(method, decodedPath string) bool {
	return method == http.MethodPost && hubServerRoute.MatchString(decodedPath)
}

// HubNoticeHeader marks a notice Coral Hub posts about a team link (#3489). The hub sets
// it only on its own notice requests and strips it from browser requests.
const HubNoticeHeader = "X-Coral-Hub-Notice"

var hubNoticeRoute = regexp.MustCompile(`^/api/board/[^/]+/hub-notice$`)

// ForwardsHubNotice reports whether X-Coral-Hub-Notice is forwarded: a POST to
// /api/board/{project}/hub-notice, and nothing else.
func ForwardsHubNotice(method, decodedPath string) bool {
	return method == http.MethodPost && hubNoticeRoute.MatchString(decodedPath)
}

// scrub keeps the allowlisted hub headers for a request to method and decodedPath.
func scrub(h http.Header, method, decodedPath string) http.Header {
	out := http.Header{}
	for k, vs := range h {
		if c := http.CanonicalHeaderKey(k); forwardRequestHeaders[c] {
			// Appended, not assigned: two spellings of one name stay two values, so the
			// server's "exactly one" checks see them (Security's #3964 finding 1).
			out[c] = append(out[c], vs...)
		}
	}
	if !ForwardsHubUser(method, decodedPath) {
		out.Del(HubUserHeader)
	}
	if !ForwardsHubServer(method, decodedPath) {
		out.Del(HubServerHeader)
	}
	if !ForwardsHubNotice(method, decodedPath) {
		out.Del(HubNoticeHeader)
	}
	// The hub cannot send this header (it is not on the allowlist) and cannot remove it:
	// Coral's file endpoints use it to apply the strict, tunnel-only path bounds.
	out.Set(ViaHeader, "1")
	return out
}
