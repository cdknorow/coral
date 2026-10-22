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
	// The sending team of a relayed message or file, and the marker of a link snapshot (#5572): each forwarded only on its own routes (scrub).
	"X-Coral-Hub-Source-Team": true,
	"X-Coral-Hub-Links":       true,
	// The client's request id for the five routes that start something (#5841): forwarded only on those routes (scrub).
	"Idempotency-Key": true,
}

// HubUserHeader names the Coral Hub user behind a tunnel board write: the sender of a
// chat post (#3377), and who acted when Coral Hub acts as the Operator (#3404).
const HubUserHeader = "X-Coral-Hub-User"

// hubUserRoute is where the Coral Hub user header is forwarded: a board's own routes,
// /api/board/{project}/... (#3404). It is dropped everywhere else, so no other handler
// can ever see it.
var hubUserRoute = regexp.MustCompile(`^/api/board/[^/]+/.+$`)

// ForwardsHubUser reports whether X-Coral-Hub-User is forwarded for a request: a write
// (not GET or HEAD) to /api/board/{project}/..., a POST to one of the five routes that
// take an Idempotency-Key (#5841: the key is scoped to the hub user). The hub (pkg/hubproxy) and
// the connector apply the same rule. (The image upload gets the hub user too, but from the stream's
// own HubUser field, never from a header: see stampUploader.)
func ForwardsHubUser(method, decodedPath string) bool {
	if ForwardsIdempotencyKey(method, decodedPath) {
		return true
	}
	if method == http.MethodGet || method == http.MethodHead {
		return false
	}
	return hubUserRoute.MatchString(decodedPath)
}

// IdempotencyKeyHeader is the client's request id for a request that starts something (#5841): Coral answers a repeat of the same request (same hub user, same route, same key, same
// body) with the first answer instead of starting a second agent or team.
const IdempotencyKeyHeader = "Idempotency-Key"

// idempotentRoute is the five routes that take an Idempotency-Key: launch, launch-team, one agent's resume and wake (not a team's), and a one-shot task run.
var idempotentRoute = regexp.MustCompile(`^/api/(sessions/(launch|launch-team)|sessions/live/[^/]+/(resume|wake)|tasks/run)$`)

// ForwardsIdempotencyKey reports whether Idempotency-Key is forwarded for a request: a POST to one of those five routes, and nothing else. The hub (pkg/hubproxy) and the connector apply
// the same rule.
func ForwardsIdempotencyKey(method, decodedPath string) bool {
	return method == http.MethodPost && idempotentRoute.MatchString(decodedPath)
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

// HubSourceTeamHeader names the sending team (its generated board id, b- and ten characters) of a relayed cross-team message or file (#5572). The hub sets it from the link pair, overwriting any
// value a sending Coral supplied; it is forwarded only where X-Coral-Hub-Server is, and the server trusts it exactly like that header: only on a request this connector stamped.
const HubSourceTeamHeader = "X-Coral-Hub-Source-Team"

// The cross-team link snapshot (#5572): Coral Hub sends the full list of links as POST HubLinksPath, with HubLinksHeader set to HubLinksSnapshot (forwarded only on that path). The connector adds
// two headers of its own, which no hub can send (they are not on the allowlist): HubLinksSessionHeader, the id this connector made for the tunnel session the request arrived on (the first
// snapshot of a session is accepted whatever its version), and HubLinksCapHeader "1" when the hub advertised CapCrossTeamLinks on THIS session. Both are stamped only on a snapshot and on a relayed
// message or file.
const (
	HubLinksPath          = "/api/cloud/cross-team-links"
	HubLinksHeader        = "X-Coral-Hub-Links"
	HubLinksSnapshot      = "snapshot"
	HubLinksSessionHeader = "X-Coral-Hub-Links-Session"
	HubLinksCapHeader     = "X-Coral-Hub-Links-Cap"
)

// ForwardsHubLinks reports whether X-Coral-Hub-Links is forwarded: a POST to HubLinksPath, and nothing else.
func ForwardsHubLinks(method, decodedPath string) bool {
	return method == http.MethodPost && decodedPath == HubLinksPath
}

// stampsLinksSession reports whether the connector adds the two session headers: a snapshot, and a relayed message or file.
func stampsLinksSession(method, decodedPath string) bool {
	return ForwardsHubLinks(method, decodedPath) || ForwardsHubServer(method, decodedPath)
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
	if !ForwardsIdempotencyKey(method, decodedPath) {
		out.Del(IdempotencyKeyHeader)
	}
	if !ForwardsHubServer(method, decodedPath) {
		out.Del(HubServerHeader)
	}
	if !ForwardsHubNotice(method, decodedPath) {
		out.Del(HubNoticeHeader)
	}
	if !ForwardsHubServer(method, decodedPath) {
		out.Del(HubSourceTeamHeader)
	}
	if !ForwardsHubLinks(method, decodedPath) {
		out.Del(HubLinksHeader)
	}
	// Never from the hub: the connector's own, below (stampLinks).
	out.Del(HubLinksSessionHeader)
	out.Del(HubLinksCapHeader)
	// The hub cannot send this header (it is not on the allowlist) and cannot remove it:
	// Coral's file endpoints use it to apply the strict, tunnel-only path bounds.
	out.Set(ViaHeader, "1")
	return out
}

// stampLinks adds the connector's own two headers (HubLinksSessionHeader, HubLinksCapHeader) on a snapshot and on a relayed message or file: what this connector knows of the tunnel session the
// stream arrived on. The hub cannot send either (scrub drops them), so the server may trust them on a request carrying the per-process stamp.
func stampLinks(out http.Header, s *Stream, method, decodedPath string) {
	out.Del(HubLinksSessionHeader)
	out.Del(HubLinksCapHeader)
	if !stampsLinksSession(method, decodedPath) || s.sessionID == "" {
		return
	}
	out.Set(HubLinksSessionHeader, s.sessionID)
	if s.linksCap {
		out.Set(HubLinksCapHeader, "1")
	}
}
