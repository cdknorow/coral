package hubproxy

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/subgentic/coral/pkg/tunnel"
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
	return &Handler{resolver: resolver, opts: o, optErr: ValidateOptions(o)}
}

func isTunnel(t Target) bool {
	switch t.(type) {
	case TunnelTarget, *TunnelTarget:
		return true
	}
	return false
}

// SandboxCSP is set on every response whose bytes an agent, a repo or a remote
// controls: if a browser opens it as a document anyway, it gets an opaque origin
// and runs no script.
const SandboxCSP = "sandbox; default-src 'none'"

// PreviewCSP is SandboxCSP for an HTML document Coral shows as a preview (#3593): still a
// sandbox with no scripts and nothing fetched from anywhere, but its own inline <style> and
// data: fonts and images apply, so a self-contained page isn't shown unstyled. A url() or
// @import in that CSS can't load anything: default-src 'none' leaves no host source, and
// font-src and img-src allow data: only. frame-ancestors 'self': only Coral's own pages may
// frame it.
const PreviewCSP = "sandbox; default-src 'none'; style-src 'unsafe-inline'; font-src data:; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"

// InlineSafe reports whether content of this media type (no parameters,
// lower case) may be shown inline on a Coral origin. Anything else that
// untrusted code can influence is sent as a download.
func InlineSafe(mediaType string) bool { return safeMediaTypes[mediaType] }

// Disposition builds a Content-Disposition of kind ("inline" or "attachment")
// that keeps only a sanitised base name of filename.
func Disposition(kind, filename string) string {
	name := dispositionFilename(mime.FormatMediaType(kind, map[string]string{"filename": filename}))
	if name == "" {
		return kind
	}
	return kind + "; filename=" + name
}

// safeMediaTypes may be served inline from the hub origin. Everything the
// connector can influence that is not on this list is forced to a download.
var safeMediaTypes = map[string]bool{
	"application/json": true, "text/event-stream": true, "text/plain": true, "application/octet-stream": true,
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
	// Harmless media (never SVG, HTML or anything scriptable). They render in the
	// sandboxed response and are not offered as downloads.
	"image/avif": true, "image/bmp": true, "image/x-icon": true, "image/vnd.microsoft.icon": true,
	"audio/mpeg": true, "audio/ogg": true, "audio/wav": true, "audio/webm": true,
	"video/mp4": true, "video/webm": true,
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
	// Every check below looks headers up by their canonical name, so the names are made
	// canonical first: a connector's lowercase "set-cookie" must not slip past (#3410).
	// (pkg/tunnel already canonicalizes a tunnel response; this filter can be given others.)
	h := tunnel.CanonicalHeader(resp.Header)
	if h == nil {
		h = http.Header{}
	}
	resp.Header = h
	filename := dispositionFilename(h.Get("Content-Disposition"))
	for _, k := range stripResponseHeaders {
		h.Del(k)
	}
	for k := range h {
		if strings.HasPrefix(http.CanonicalHeaderKey(k), "Access-Control-") {
			delete(h, k) // by the key as it is (SEC-TUN-03)
		}
	}
	// Only a path on this origin survives (#5086): a browser reads slash-backslash as two slashes (and drops a tab or newline inside a URL), which Go's parser
	// calls a path, so a connector's Location of slash, backslash and a host used to pass this check and send the browser off the hub's origin. Every
	// Location header counts, not only the first.
	for _, loc := range h.Values("Location") {
		if !sameOriginLocation(loc) {
			h.Del("Location")
			break
		}
	}
	mt, params, err := mime.ParseMediaType(h.Get("Content-Type"))
	mt = strings.ToLower(mt)
	if err != nil || !safeMediaTypes[mt] {
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", attachment(filename))
	} else {
		// Set (not Add) the single parsed type so a duplicate Content-Type header
		// cannot survive, and keep only a sanitised attachment filename.
		h.Set("Content-Type", mime.FormatMediaType(mt, params))
		if filename != "" {
			h.Set("Content-Disposition", attachment(filename))
		}
	}
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", SandboxCSP)
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	// Whatever the connector says about caching is ignored: this is one account's content on a shared origin. A
	// browser cache would keep it after sign-out, revoke, or on a shared browser (Coral marks artifacts
	// "public, max-age=1y, immutable").
	h.Del("Cache-Control")
	advisoryLength := h.Get(tunnel.BodyLengthHeader)
	h.Del(tunnel.BodyLengthHeader) // advisory, between connector and hub only: never relayed to the browser
	h.Del("Expires")
	h.Del("Etag")
	h.Del("Last-Modified")
	h.Set("Cache-Control", "private, no-store")
	if resp.Request != nil && resp.Request.URL != nil {
		switch pth := resp.Request.URL.Path; {
		case artifactPath.MatchString(pth):
			capBody(resp, advisoryLength, MaxArtifactBytes, "artifact_too_large")
		case panelContentPath.MatchString(pth):
			capBody(resp, advisoryLength, MaxPanelBytes, "panel_too_large")
		}
	}
	return nil
}

// sameOriginLocation reports whether a Location is a path on the origin that answered: it starts with one "/" (not "//" and not "/\\"), holds no backslash,
// control character or space, and parses as a path with no scheme, host or userinfo.
func sameOriginLocation(loc string) bool {
	if len(loc) == 0 || loc[0] != '/' || (len(loc) > 1 && (loc[1] == '/' || loc[1] == '\\')) {
		return false
	}
	for i := 0; i < len(loc); i++ {
		if c := loc[i]; c <= 0x20 || c == 0x7f || c == '\\' {
			return false
		}
	}
	u, err := url.Parse(loc)
	return err == nil && !u.IsAbs() && u.Host == "" && u.User == nil
}

// MaxPanelBytes is the most the hub relays for one Agent UI panel: the machine accepts 2 MiB of HTML and prepends a small
// bridge script; the dashboard runs it in a sandbox and refuses more.
const MaxPanelBytes = 2<<20 + 16<<10

var panelContentPath = regexp.MustCompile(`^/api/agent/ui/[A-Za-z0-9_-]{1,64}/content$`)

// MaxArtifactBytes is the most the hub relays for one artifact. Coral accepts uploads up to 64 MiB; the hub
// relays at most 25 MiB through a browser fetch.
const MaxArtifactBytes = 25 << 20

var artifactPath = regexp.MustCompile(`^/api/artifacts/[0-9a-f]{64}$`)

var errArtifactTooLarge = errors.New("artifact exceeds the hub's relay limit")

// capBody enforces a relay limit (MaxArtifactBytes for artifacts, MaxPanelBytes for panels): a declared length over the limit is answered with a clear 413, and an
// undeclared (chunked) body is cut off with an error once it passes the limit, never streamed without bound.
func capBody(resp *http.Response, advisoryLength string, limit int64, code string) {
	declared := resp.ContentLength
	if declared < 0 { // tunnel responses are chunk-framed: the length is what the connector's header claims
		if n, err := strconv.ParseInt(advisoryLength, 10, 64); err == nil && n >= 0 {
			declared = n
		}
	}
	if declared > limit {
		_ = resp.Body.Close()
		body := []byte(`{"error":"` + code + `"}` + "\n")
		resp.StatusCode, resp.Status = http.StatusRequestEntityTooLarge, "413 Request Entity Too Large"
		resp.Body = io.NopCloser(bytes.NewReader(body))
		resp.ContentLength = int64(len(body))
		resp.Header.Set("Content-Type", "application/json")
		resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
		resp.Header.Del("Content-Disposition")
		return
	}
	// Whatever length was claimed, never relay more than the limit (a connector can lie about it).
	resp.Body = &cappedBody{rc: resp.Body, left: limit}
}

type cappedBody struct {
	rc   io.ReadCloser
	left int64
}

func (c *cappedBody) Read(p []byte) (int, error) {
	if c.left <= 0 {
		// The limit is reached: a body of EXACTLY the limit must end cleanly, so probe for one more byte.
		var one [1]byte
		if n, err := io.ReadFull(c.rc, one[:]); n == 1 {
			return 0, errArtifactTooLarge
		} else if err == io.ErrUnexpectedEOF || err == io.EOF {
			return 0, io.EOF
		} else {
			return 0, err
		}
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.rc.Read(p)
	c.left -= int64(n)
	return n, err
}

func (c *cappedBody) Close() error { return c.rc.Close() }

// dispositionFilename returns a safe ASCII basename from a Content-Disposition header,
// or "" when there is none. Everything the connector supplies is reduced to
// [A-Za-z0-9._-], at most 100 characters, with no leading dot.
func dispositionFilename(cd string) string {
	if cd == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(cd)
	if err != nil {
		return ""
	}
	name := params["filename"]
	if i := strings.LastIndexAny(name, "/\\"); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= 100 {
			break
		}
	}
	return strings.TrimLeft(b.String(), ".")
}

func attachment(filename string) string {
	if filename == "" {
		return "attachment"
	}
	return "attachment; filename=" + filename
}

// ValidateOptions rejects configurations that would weaken the origin check. The cloud
// hub calls it at startup; a Handler built with invalid options also refuses every
// WebSocket upgrade (fail closed).
func ValidateOptions(o Options) error {
	for _, p := range o.AllowedOrigins {
		if err := validOriginPattern(p); err != nil {
			return err
		}
	}
	return nil
}

func validOriginPattern(p string) error {
	if p == "" || strings.ContainsAny(p, "/\\ ") || strings.Contains(p, "://") {
		return errors.New("hubproxy: AllowedOrigins entries are host[:port] patterns, not URLs")
	}
	if !strings.ContainsAny(p, "*?[") {
		return nil // an exact host
	}
	// A wildcard in the host must be anchored on a registrable-looking suffix: "*", "*.com"
	// and "*:*" would let any site open a stream to a user's machine. A wildcard only in
	// the port ("hub.example.com:*") is fine.
	host, _, _ := strings.Cut(p, ":")
	if !strings.ContainsAny(host, "*?[") {
		if host == "" {
			return errors.New("hubproxy: AllowedOrigins pattern has no host")
		}
		return nil
	}
	if !strings.HasPrefix(host, "*.") || strings.ContainsAny(host[2:], "*?[") || !strings.Contains(host[2:], ".") {
		return errors.New("hubproxy: a wildcard AllowedOrigins host must look like *.example.com")
	}
	return nil
}

// dropHubUserHeader removes X-Coral-Hub-User in any spelling.
func dropHubUserHeader(h http.Header) {
	for k := range h {
		if http.CanonicalHeaderKey(k) == tunnel.HubUserHeader {
			delete(h, k)
		}
	}
}

// plainSandbox reports whether a response's Content-Security-Policy is safely sandboxed: ONE header
// line (a second line is a second policy, and the browser enforces both, so which "sandbox" wins is
// not ours to guess), no comma (a comma separates policies inside one line), and exactly one
// directive named sandbox, which is bare: no allow-scripts, no allow-same-origin, so the document
// is an opaque origin that runs nothing. 'sandbox allow-scripts; sandbox' fails: the first sandbox
// directive is the one a browser reads, and a later bare one doesn't undo it.
func plainSandbox(values []string) bool {
	if len(values) != 1 || strings.Contains(values[0], ",") {
		return false
	}
	bare := 0
	for _, d := range strings.Split(values[0], ";") {
		fields := strings.Fields(d)
		if len(fields) == 0 || !strings.EqualFold(fields[0], "sandbox") {
			continue
		}
		if len(fields) != 1 {
			return false // a sandbox directive with tokens
		}
		bare++
	}
	return bare == 1
}

// directResponseFilter is StrictResponseFilter for a remote Coral reached directly (self-hosted
// --hub). A response that arrives sandboxed (Coral's own previews: PreviewCSP, the file-content
// preview's policy) keeps its policy, type and disposition kind, since the sandbox is what makes
// them safe to show; everything else is filtered as a connector's reply is.
func directResponseFilter(resp *http.Response) error {
	csp := resp.Header.Get("Content-Security-Policy")
	if !plainSandbox(resp.Header.Values("Content-Security-Policy")) {
		return StrictResponseFilter(resp)
	}
	contentType := resp.Header.Get("Content-Type")
	kind, params, _ := mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
	if err := StrictResponseFilter(resp); err != nil {
		return err
	}
	if mt, p, err := mime.ParseMediaType(contentType); err == nil && mt != "" {
		resp.Header.Set("Content-Type", mime.FormatMediaType(strings.ToLower(mt), p))
	}
	if kind == "inline" || kind == "attachment" {
		resp.Header.Set("Content-Disposition", Disposition(kind, params["filename"]))
	} else {
		resp.Header.Del("Content-Disposition")
	}
	resp.Header.Set("Content-Security-Policy", csp)
	return nil
}

// forwardToConnector is the allowlist of browser request headers sent over a
// tunnel (canonical form). The hub's own CSRF token, cookies, fetch-metadata and
// forwarding headers never reach the connector.
var forwardToConnector = map[string]bool{
	"Accept": true, "Accept-Language": true, "Content-Type": true, "Range": true, "If-Range": true,
	"If-Match": true, "If-None-Match": true, "If-Modified-Since": true, "If-Unmodified-Since": true,
	"Cache-Control": true, "Last-Event-Id": true, "User-Agent": true,
	// The Coral Hub user behind a board write (#3377, #3404); only on those routes, see below.
	tunnel.HubUserHeader: true,
	// The client's request id, only on the five routes that take it (#5841), see below.
	tunnel.IdempotencyKeyHeader: true,
}

// allowlistHeaders keeps the allowlisted headers of a request to method and the
// machine's decodedPath. X-Coral-Hub-User only goes with a board write,
// /api/board/{project}/... other than GET or HEAD (the connector applies the same rule).
//
// Keys are compared and deleted as written: h.Del would only remove the canonical
// spelling, so a non-canonical "x-coral-hub-user" key could otherwise survive
// (SEC-TUN-01). Defence in depth: the connector's scrub canonicalizes first and drops
// it there too.
func allowlistHeaders(h http.Header, method, decodedPath string) {
	hubUser := tunnel.ForwardsHubUser(method, decodedPath)
	idem := tunnel.ForwardsIdempotencyKey(method, decodedPath)
	for k := range h {
		canon := http.CanonicalHeaderKey(k)
		if !forwardToConnector[canon] || (canon == tunnel.HubUserHeader && !hubUser) || (canon == tunnel.IdempotencyKeyHeader && !idem) {
			delete(h, k)
		}
	}
}
