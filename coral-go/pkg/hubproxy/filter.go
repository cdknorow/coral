package hubproxy

import (
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// Options configures a Handler.
type Options struct {
	// ResponseFilter runs on every upstream response after the built-in 401
	// handling. Nil means: StrictResponseFilter for TunnelTarget (the connector is
	// not trusted), and the legacy "strip Set-Cookie" for HTTPTarget.
	ResponseFilter func(*http.Response) error
	// AllowedOrigins are the host[:port] patterns (path.Match syntax, case
	// insensitive) a browser's Origin may have for a WebSocket upgrade. Set it to
	// the hub's public host only. With it set, a missing or "null" Origin is
	// refused. Nil keeps the self-hosted default (localhost, loopback and the
	// request Host; a missing Origin is allowed for non-browser clients). A
	// TunnelTarget with no AllowedOrigins is refused: the cloud hub must say which
	// origin it serves.
	AllowedOrigins []string
	// HideServerID leaves the server id out of JSON errors. The cloud hub sets it so
	// an error body is identical for a server that does not exist and one the caller
	// does not own.
	HideServerID bool
}

// NewWithOptions is New with explicit options.
func NewWithOptions(resolver Resolver, o Options) *Handler {
	return &Handler{resolver: resolver, opts: o}
}

func isTunnel(t Target) bool {
	switch t.(type) {
	case TunnelTarget, *TunnelTarget:
		return true
	}
	return false
}

// safeMediaTypes may be served inline from the hub origin. Everything the
// connector can influence that is not on this list is forced to a download.
var safeMediaTypes = map[string]bool{
	"application/json": true, "text/event-stream": true, "text/plain": true, "application/octet-stream": true,
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
}

// stripResponseHeaders are removed from untrusted responses: they set state or
// policy on the hub origin.
var stripResponseHeaders = []string{
	"Set-Cookie", "Set-Cookie2", "Content-Security-Policy", "Content-Security-Policy-Report-Only",
	"Clear-Site-Data", "Refresh", "Link", "Service-Worker-Allowed", "Report-To", "Reporting-Endpoints",
	"Nel", "Strict-Transport-Security", "Alt-Svc", "Public-Key-Pins", "X-Frame-Options",
	"Cross-Origin-Opener-Policy", "Cross-Origin-Embedder-Policy", "Cross-Origin-Resource-Policy",
	"Permissions-Policy", "Origin-Agent-Cluster", "Content-Disposition",
}

// StrictResponseFilter makes a response from an untrusted connector safe to
// relay on the hub origin (threat model P-3h). A file, preview or agent output
// can be attacker-controlled (a cloned repo), and the same proxy URL can be
// loaded as a script, so it neutralises rather than relies on the page's CSP:
//
//   - media types outside the allowlist (HTML, JavaScript, CSS, SVG, XML,
//     anything unknown or missing) are rewritten to application/octet-stream
//     and sent as an attachment;
//   - nosniff and "Content-Security-Policy: sandbox; default-src 'none'" are
//     always set, and CORP is same-origin;
//   - headers that set cookies, policy, service workers or reporting on the hub
//     origin are removed, and so is any Location that leaves the origin.
func StrictResponseFilter(resp *http.Response) error {
	h := resp.Header
	for _, k := range stripResponseHeaders {
		h.Del(k)
	}
	for k := range h {
		if strings.HasPrefix(http.CanonicalHeaderKey(k), "Access-Control-") {
			h.Del(k)
		}
	}
	if loc := h.Get("Location"); loc != "" {
		if u, err := url.Parse(loc); err != nil || u.IsAbs() || u.Host != "" || strings.HasPrefix(loc, "//") || strings.HasPrefix(loc, "\\") {
			h.Del("Location")
		}
	}
	mt, _, err := mime.ParseMediaType(h.Get("Content-Type"))
	if err != nil || !safeMediaTypes[strings.ToLower(mt)] {
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", "attachment")
	}
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	return nil
}

// forwardToConnector is the allowlist of browser request headers sent over a
// tunnel (canonical form). The hub's own CSRF token, cookies, fetch-metadata and
// forwarding headers never reach the connector.
var forwardToConnector = map[string]bool{
	"Accept": true, "Accept-Language": true, "Content-Type": true, "Range": true, "If-Range": true,
	"If-Match": true, "If-None-Match": true, "If-Modified-Since": true, "If-Unmodified-Since": true,
	"Cache-Control": true, "Last-Event-Id": true, "User-Agent": true,
}

func allowlistHeaders(h http.Header) {
	for k := range h {
		if !forwardToConnector[http.CanonicalHeaderKey(k)] {
			delete(h, k)
		}
	}
}
