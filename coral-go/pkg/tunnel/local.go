package tunnel

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"

	"github.com/cdknorow/coral/pkg/redact"
)

// ── Path guard ──────────────────────────────────────────────────────

// ErrPath is returned by ValidatePath.
var ErrPath = errors.New("tunnel: path not allowed")

// compatDotSlash are the compatibility characters that Unicode NFKC folds to
// "." "/" or "\" (one/two-dot leaders, fullwidth and small forms, fraction and
// division slashes). Go routers do not normalise, but a future one might, so
// they are refused outright.
const compatDotSlash = "\u2024\u2025\u2026\ufe52\uff0e\uff0f\u2215\u2044\uff3c\ufe68\u29f8\u2216"

// ValidatePath is the connector's allowlist for hub-supplied paths. It takes
// the escaped path as it will be sent to the local API and accepts it only if:
//
//   - it starts with /api/ or /ws/;
//   - it contains no percent-encoded ".", "/" or "\" at all (%2e, %2f, %5c);
//   - it has no empty segment ("//"), no "." or ".." segment, also when
//     percent-encoded, and no double encoding (%252e);
//   - no segment contains a backslash, NUL, control character, ";" (path
//     parameters), an encoded "/" (%2f) or invalid UTF-8 (overlong forms);
//   - it is not an absolute URL or scheme-relative target, carries no host
//     part, and contains no "?", "#", whitespace, CR or LF.
//
// Anything else is rejected, not normalised.
func ValidatePath(p string) error {
	if len(p) > 4096 {
		return ErrPath
	}
	if !strings.HasPrefix(p, "/api/") && !strings.HasPrefix(p, "/ws/") {
		return ErrPath
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c <= 0x20 || c >= 0x7f || c == '\\' || c == '?' || c == '#' || c == ';' {
			return ErrPath
		}
	}
	// "." "/" and "\\" are never legitimately percent-encoded by a client; an
	// encoded one is an evasion attempt, wherever it sits in the segment.
	if lp := strings.ToLower(p); strings.Contains(lp, "%2e") || strings.Contains(lp, "%2f") || strings.Contains(lp, "%5c") {
		return ErrPath
	}
	segs := strings.Split(p[1:], "/")
	for i, raw := range segs {
		if raw == "" && i != len(segs)-1 {
			return ErrPath
		}
		if raw == "." || raw == ".." {
			return ErrPath
		}
		d, err := url.PathUnescape(raw)
		if err != nil || !utf8.ValidString(d) {
			return ErrPath
		}
		if d == "." || d == ".." || strings.ContainsAny(d, "/\\;%") || strings.ContainsAny(d, compatDotSlash) {
			return ErrPath
		}
		for j := 0; j < len(d); j++ {
			if d[j] < 0x20 || d[j] == 0x7f {
				return ErrPath
			}
		}
	}
	return nil
}

func validQuery(q string) bool {
	if len(q) > 8192 || !utf8.ValidString(q) {
		return false
	}
	for i := 0; i < len(q); i++ {
		if q[i] <= 0x20 || q[i] >= 0x7f || q[i] == '#' {
			return false
		}
	}
	return true
}

// ── Loopback-only client ────────────────────────────────────────────

// NewLoopbackClient returns an http.Client and base URL for the local Coral
// API. base must be http(s) on a loopback host. The host is resolved once and
// every connection goes to that pinned loopback IP, whatever the request
// says, and the dial is re-checked at connect time. Redirects are never
// followed and proxies are never used.
func NewLoopbackClient(base string) (*http.Client, *url.URL, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, nil, errors.New("tunnel: invalid local url")
	}
	var pinned net.IP
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		pinned = ip
	} else {
		ips, err := net.LookupIP(u.Hostname())
		if err != nil || len(ips) == 0 {
			return nil, nil, errors.New("tunnel: cannot resolve local host")
		}
		pinned = ips[0]
		for _, ip := range ips {
			if !ip.IsLoopback() {
				return nil, nil, errors.New("tunnel: local host is not loopback")
			}
		}
	}
	if !pinned.IsLoopback() {
		return nil, nil, errors.New("tunnel: local host is not loopback")
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	addr := net.JoinHostPort(pinned.String(), port)
	d := &net.Dialer{
		Timeout: 5 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			h, _, err := net.SplitHostPort(address)
			if err != nil {
				return errors.New("tunnel: non-loopback dial refused")
			}
			if ip := net.ParseIP(h); ip == nil || !ip.IsLoopback() {
				return errors.New("tunnel: non-loopback dial refused")
			}
			return nil
		},
	}
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return d.DialContext(ctx, network, addr)
		},
		DisableCompression:  true,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     60 * time.Second,
		// Headers must arrive promptly; bodies (SSE, downloads) stream freely.
		ResponseHeaderTimeout: 60 * time.Second,
	}
	cl := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return cl, u, nil
}

// ── Route rules ─────────────────────────────────────────────────────

// PathRule allows requests whose decoded path starts with Prefix (or equals it,
// when Exact is set) for the listed methods; no Methods means GET and HEAD only.
type PathRule struct {
	Prefix  string
	Exact   bool
	Methods []string
}

// PathRules is an allowlist of routes. Rules only ever add permissions; a
// request matching none is denied. Use rules.Allow as LocalHandler.AllowPath.
// An optional Gate can veto a request that a rule accepted (for example the
// per-server "allow launching from the web" switch).
type PathRules struct {
	Rules []PathRule
	Gate  func(method, decodedPath string) bool
}

// NewPathRules builds PathRules and validates them: a prefix rule must end in "/" so
// that "/api/session/" can never match "/api/sessions-admin", and every rule must
// start with "/". Exact rules are not constrained.
func NewPathRules(rules []PathRule, gate func(method, decodedPath string) bool) (PathRules, error) {
	for _, r := range rules {
		if !strings.HasPrefix(r.Prefix, "/") {
			return PathRules{}, fmt.Errorf("tunnel: path rule %q must start with /", r.Prefix)
		}
		if !r.Exact && !strings.HasSuffix(r.Prefix, "/") {
			return PathRules{}, fmt.Errorf("tunnel: prefix rule %q must end in / (use Exact for a single route)", r.Prefix)
		}
	}
	return PathRules{Rules: rules, Gate: gate}, nil
}

// Allow reports whether the request may reach the local API. A prefix rule that does
// not end in "/" matches nothing (fail closed), even if it was not built with NewPathRules.
func (p PathRules) Allow(method, decodedPath string) bool {
	if !allowedMethods[method] {
		return false
	}
	ok := false
	for _, r := range p.Rules {
		if r.Exact {
			if decodedPath != r.Prefix {
				continue
			}
		} else if !strings.HasSuffix(r.Prefix, "/") || !strings.HasPrefix(decodedPath, r.Prefix) {
			continue
		}
		methods := r.Methods
		if len(methods) == 0 {
			methods = []string{http.MethodGet, http.MethodHead}
		}
		for _, m := range methods {
			if m == method {
				ok = true
			}
		}
	}
	return ok && (p.Gate == nil || p.Gate(method, decodedPath))
}

// BodyLengthHeader carries the machine's declared response length across the (chunk-framed) tunnel.
const BodyLengthHeader = "X-Coral-Content-Length"

// ViaHeader marks a request that the connector executed on behalf of the Cloud Hub.
// Coral's file endpoints apply strict bounds to such requests (no absolute paths
// outside the session's repository, a sensitive-path denylist, generic errors).
const ViaHeader = "X-Coral-Via-Tunnel"

// ── LocalHandler ────────────────────────────────────────────────────

// LocalHandler executes tunnel streams against the local Coral API on
// loopback, enforcing the connector-side guards whatever the hub sends.
type LocalHandler struct {
	// BaseURL of the local API, e.g. http://127.0.0.1:8420. Must be loopback.
	BaseURL string
	// Authorize adds the connector's own local credentials to the outgoing
	// header set (the hub's Authorization and Cookie are always dropped).
	Authorize func(h http.Header)
	// AllowPath decides which routes the hub may reach, on top of ValidatePath.
	// It receives the method and the decoded path (WebSocket streams count as
	// GET). The default is DENY: a nil AllowPath rejects every stream, so a
	// connector that forgets to configure it exposes nothing. Authorize is only
	// applied to requests AllowPath accepted. See PathRules.
	AllowPath func(method, decodedPath string) bool
	// AllowRequest is the query-aware form of AllowPath (the query string matters:
	// file routes must carry project-relative paths). When set it is used instead of
	// AllowPath. See AllowRequest in routes.go for the shared classified table.
	AllowRequest func(method, decodedPath, rawQuery string) bool
	// MaxMode gates client-to-server WebSocket frames by the mode in force
	// at the moment each frame arrives: viewing a terminal needs view, typing or
	// resizing needs interact. A frame above the mode closes the stream (policy
	// violation), so lowering the mode stops input on streams that are already open.
	// Nil means view.
	MaxMode func() Mode
	// MaxRequestBody caps uploads, default 256 MiB.
	MaxRequestBody int64
	// WSReadLimit caps one WebSocket message, default DefaultWSReadLimit.
	WSReadLimit int64
	// ViaValue is the value stamped in ViaHeader, default "1". Coral sets a
	// per-process secret so the local server can tell the connector's requests
	// from a local process that forges the header (any value still makes
	// tunnel-only rules apply, so a forged stamp can only make things stricter).
	ViaValue string
	// RedactTokens turns on token redaction in View replies (#3560): well-known credential
	// formats and private keys are replaced by redact.Marker before a reply leaves this
	// computer. Read on every request; nil means on.
	RedactTokens func() bool
	// Redactor removes secrets from those replies and streams (#3808: Coral sets betterleaks'
	// rules, pkg/redact/betterleaks). Nil refuses every reply that would be redacted: none is
	// ever sent unchecked.
	Redactor redact.Redactor
	// HubLimits bounds what Coral Hub can hold open on this computer at once (#3624); zero
	// values take the defaults (DefaultGlobalInFlight, DefaultLongInFlight,
	// DefaultSearchInFlight, DefaultRequestDeadline, DefaultBodyIdle). The limiter is always on.
	HubLimits HubLimits
	// HubLimitsFunc, when set, gives the pool sizes and per-user caps for each request (the
	// settings, #3642), so a change applies to the next request. RequestDeadline and BodyIdle
	// always come from HubLimits.
	HubLimitsFunc func() HubLimits

	lim    limiterOnce
	once   sync.Once
	client *http.Client
	// timed is client with the hub request deadline as its ResponseHeaderTimeout, for every
	// route but the long-lived ones (#3624).
	timed *http.Client
	base  *url.URL
	err   error
}

func (l *LocalHandler) init() error {
	l.once.Do(func() {
		l.client, l.base, l.err = NewLoopbackClient(l.BaseURL)
		if l.err != nil {
			return
		}
		// The deadline is the transport's ResponseHeaderTimeout: it starts once the whole
		// request, body included, has been written, so a slow upload through the hub isn't cut,
		// and it can't fire after the headers are in (Security's SHOULD 1 and NIT on #3624).
		l.timed = l.client
		if tr, ok := l.client.Transport.(*http.Transport); ok {
			t := tr.Clone()
			t.ResponseHeaderTimeout = l.lim.get(l.HubLimits).deadline
			c := *l.client
			c.Transport = t
			l.timed = &c
		}
	})
	return l.err
}

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

// hubServerRoute is the only route the Coral Hub server header is forwarded to.
var hubServerRoute = regexp.MustCompile(`^/api/board/[^/]+/external-messages$`)

// ForwardsHubServer reports whether X-Coral-Hub-Server is forwarded for a request: a POST
// to /api/board/{project}/external-messages, and nothing else.
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
		if forwardRequestHeaders[http.CanonicalHeaderKey(k)] {
			out[http.CanonicalHeaderKey(k)] = append([]string(nil), vs...)
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

// stamp replaces the default ViaHeader value with ViaValue when one is set.
func (l *LocalHandler) stamp(h http.Header) http.Header {
	if l.ViaValue != "" {
		h.Set(ViaHeader, l.ViaValue)
	}
	return h
}

func (l *LocalHandler) checkRequest(h Header) (decoded string, ok bool) {
	if err := ValidatePath(h.Path); err != nil || !validQuery(h.Query) {
		return "", false
	}
	if h.Kind == KindHTTP && !strings.HasPrefix(h.Path, "/api/") {
		return "", false // /ws/ is for WebSocket streams only
	}
	if h.Kind == KindWS && !strings.HasPrefix(h.Path, "/ws/") {
		return "", false // and WebSocket streams only for /ws/ (SEC-TUN-04)
	}
	decoded, err := url.PathUnescape(h.Path)
	if err != nil {
		return "", false
	}
	method := h.Method
	if h.Kind == KindWS {
		method = http.MethodGet
	}
	switch {
	case l.AllowRequest != nil:
		if !l.AllowRequest(method, decoded, h.Query) {
			return "", false
		}
	case l.AllowPath != nil:
		if !l.AllowPath(method, decoded) {
			return "", false
		}
	default:
		return "", false // default deny
	}
	return decoded, true
}

// localModeDenial reports whether a request was refused only because this machine's local
// maximum mode is below the route's tier (the route itself is known and valid). The hub shows
// the owner a clear message for it; everything else stays an opaque refusal. The answer
// reveals only the owner's own local setting, and only to a request the hub already
// authenticated as theirs.
func (l *LocalHandler) localModeDenial(h Header) (mode string, ok bool) {
	if l.AllowRequest == nil || ValidatePath(h.Path) != nil || !validQuery(h.Query) {
		return "", false
	}
	if h.Kind == KindHTTP && !strings.HasPrefix(h.Path, "/api/") {
		return "", false
	}
	decoded, err := url.PathUnescape(h.Path)
	if err != nil {
		return "", false
	}
	method := h.Method
	if h.Kind == KindWS {
		method = http.MethodGet
	}
	tier := Classify(method, decoded, h.Query)
	cur := currentMode(l.MaxMode)
	if tier == TierNever || tier <= cur {
		return "", false
	}
	return cur.String(), true
}

// reject answers a refused request: a distinct, non-leaky code when only the local mode stands
// in the way, the generic path refusal otherwise.
func (l *LocalHandler) reject(s *Stream) {
	if mode, ok := l.localModeDenial(s.Header); ok {
		s.Reject(CodeLocalMode, mode, 0)
		return
	}
	s.Reject(CodePathNotAllowed, "path not allowed", 0)
}

// ServeStream implements Handler.
func (l *LocalHandler) ServeStream(ctx context.Context, s *Stream) {
	if err := l.init(); err != nil {
		s.Reject(CodeUpstream, "local api unavailable", 0)
		return
	}
	switch s.Header.Kind {
	case KindHTTP:
		l.serveHTTP(ctx, s)
	case KindWS:
		l.serveWS(ctx, s)
	default:
		s.Reject(CodeBadRequest, "bad request", 0)
	}
}

func (l *LocalHandler) target(h Header, dec string) *url.URL {
	u := *l.base
	u.Path, u.RawPath, u.RawQuery = dec, h.Path, h.Query
	return &u
}

func (l *LocalHandler) serveHTTP(ctx context.Context, s *Stream) {
	h := s.Header
	dec, ok := l.checkRequest(h)
	if !ok {
		l.reject(s)
		return
	}
	if !allowedMethods[h.Method] {
		s.Reject(CodeBadRequest, "method not allowed", 0)
		return
	}
	lm := l.lim.get(l.HubLimits)
	class := classify(h.Kind, dec)
	release, perUser, ok := lm.acquire(class, h.HubUser, l.limits())
	if !ok {
		lm.refused(class, dec, perUser)
		busy(s, perUser)
		return
	}
	defer release()
	// A stalled handler can't hold its slot: no response headers within the deadline after the
	// whole request was sent cancels it (the reply body, once it flows, isn't timed). Long-lived
	// routes keep their own timeouts.
	client := l.timed
	if class == classLong {
		client = l.client
	}
	max := l.MaxRequestBody
	if max <= 0 {
		max = 256 << 20
	}
	var body io.Reader
	rb := s.RequestBody(max)
	if h.ContentLength == 0 {
		_, _ = io.Copy(io.Discard, rb) // consume the terminator; arms abort detection
	} else {
		body = rb
	}
	req, err := http.NewRequestWithContext(ctx, h.Method, l.target(h, dec).String(), body)
	if err != nil {
		s.Reject(CodeBadRequest, "bad request", 0)
		return
	}
	req.Header = l.stamp(scrub(h.Headers, h.Method, dec))
	redactRoute := l.redacting() && redactsRoute(h.Method, dec)
	if redactRoute {
		// A partial body could start inside a token: the whole reply is fetched and redacted.
		req.Header.Del("Range")
		req.Header.Del("If-Range")
	}
	if h.ContentLength > 0 {
		req.ContentLength = h.ContentLength
	}
	if l.Authorize != nil {
		l.Authorize(req.Header)
	}
	resp, err := client.Do(req)
	var ne net.Error
	if err != nil && class != classLong && errors.As(err, &ne) && ne.Timeout() {
		s.Reject(CodeUpstream, "local request took too long", 0)
		return
	}
	if err != nil {
		s.Reject(CodeUpstream, "local request failed", 0)
		return
	}
	defer resp.Body.Close()
	// From here the reply must keep moving: no progress for BodyIdle (nothing more from the
	// local API, or the hub not taking what was sent) ends the stream, so its slot is freed
	// (#3642). The hub sees a truncated reply. The timer starts only once the headers are in,
	// so a long poll, which answers when it has something, is never timed while it waits; an
	// event stream (text/event-stream), silent between events by design, is never timed.
	progress := func() {}
	if !eventStream(resp.Header.Get("Content-Type")) {
		idle := time.AfterFunc(lm.bodyIdle, s.abort)
		defer idle.Stop()
		progress = func() { idle.Reset(lm.bodyIdle) }
	}
	hdr := resp.Header.Clone()
	for _, k := range []string{"Connection", "Keep-Alive", "Transfer-Encoding", "Upgrade", "Proxy-Authenticate", "Te", "Trailer"} {
		hdr.Del(k)
	}
	hdr.Del("Content-Length") // the body is re-framed
	hdr.Del(BodyLengthHeader) // never forward a header the machine's own app set under our name
	// The hub never asks for compression (Accept-Encoding is not forwarded), and a compressed body is never
	// relayed: compressing a secret next to attacker-influenced text leaks it through the length (BREACH). gzip
	// from the local app is decoded here, so the hub sees and caps the decoded bytes; anything else is refused.
	respBody := io.Reader(resp.Body)
	decoded := false
	if enc := resp.Header.Values("Content-Encoding"); len(enc) > 0 && !bodyless(h.Method, resp.StatusCode) {
		switch strings.ToLower(strings.TrimSpace(strings.Join(enc, ","))) {
		case "", "identity":
		case "gzip", "x-gzip":
			zr, err := gzip.NewReader(resp.Body)
			if err != nil {
				respondUnsupportedEncoding(s)
				return
			}
			defer zr.Close()
			respBody, decoded = zr, true
		default:
			respondUnsupportedEncoding(s)
			return
		}
	}
	hdr.Del("Content-Encoding")
	if resp.ContentLength >= 0 && !decoded {
		// Advisory only: lets the hub refuse a huge artifact with a clear 413 before streaming. The hub also
		// caps the body itself, so a connector that omits or falsifies this gains nothing. A decoded body's
		// length is not known in advance, so none is claimed for it.
		hdr.Set(BodyLengthHeader, strconv.FormatInt(resp.ContentLength, 10))
	}
	rewriteLocation(hdr)
	if redactRoute && textReply(resp.Header.Get("Content-Type")) && bodyAllowed(s.Header.Method, resp.StatusCode) {
		// X-Coral-Content-Length stays the upstream length when it is known (#3568): it is
		// advisory, read only by the hub to refuse an oversize artifact or panel with a clean 413
		// before streaming, never passed on to the browser, and the hub caps the body by its own
		// limit, so the redacted body's different length breaks nothing. Content-Length itself is
		// already removed (the body is re-framed). A decoded or open-ended reply has none.
		l.serveRedacted(s, resp, respBody, hdr, decoded, progress)
		return
	}
	if resp.ContentLength >= 0 && resp.ContentLength <= wholeReplyMax && !decoded && bodyAllowed(s.Header.Method, resp.StatusCode) {
		// A small reply of known length goes in one write: one turn on the uplink (#3440).
		body := make([]byte, resp.ContentLength)
		if _, err := io.ReadFull(respBody, body); err != nil {
			s.Reject(CodeUpstream, "local response ended early", 0)
			return
		}
		_ = s.RespondWhole(ResponseHeader{Status: resp.StatusCode, Headers: hdr}, body)
		return
	}
	w, err := s.Respond(ResponseHeader{Status: resp.StatusCode, Headers: hdr})
	if err != nil {
		return
	}
	buf := make([]byte, maxChunk)
	for {
		n, rerr := respBody.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			progress()
		}
		if rerr != nil {
			if rerr == io.EOF {
				_ = w.Close()
			}
			return // anything else: leave the body unterminated so the hub sees a truncated response
		}
	}
}

// eventStream reports whether a reply's Content-Type is an event stream.
func eventStream(contentType string) bool {
	mt, _, _ := strings.Cut(contentType, ";")
	return strings.EqualFold(strings.TrimSpace(mt), "text/event-stream")
}

// limits is the pool sizes and per-user caps for a request.
func (l *LocalHandler) limits() HubLimits {
	if l.HubLimitsFunc != nil {
		return l.HubLimitsFunc()
	}
	return l.HubLimits
}

func (l *LocalHandler) redacting() bool { return l.RedactTokens == nil || l.RedactTokens() }

func (l *LocalHandler) redactor() redact.Redactor {
	if l.Redactor == nil {
		return redact.Refuse
	}
	return l.Redactor
}

// redactFailed is the whole answer when a reply couldn't be scanned: it never passes unscanned.
const redactFailed = `{"error":"this reply could not be checked for secrets, so it is not shown through Coral Hub; open it on the computer"}` + "\n"

var redactFailLog sync.Once

// serveRedacted relays a View reply with its tokens replaced (#3560). A reply of known size up
// to wholeReplyMax goes in one write, as unredacted ones do; a larger, open-ended or decoded one
// streams through the redactor's Stream, which holds back only what it can't check yet. Range
// is dropped per route, not per content type, so a binary reply on a redacted View route (an
// image the agent's page serves, say) is a 200 with the whole body: accepted (Security's #3560
// note 2), since only artifact downloads need byte ranges and they are exempt.
func (l *LocalHandler) serveRedacted(s *Stream, resp *http.Response, body io.Reader, hdr http.Header, decoded bool, progress func()) {
	// The whole-reply path reads exactly Content-Length bytes, which for a decoded (gzip) reply is
	// the compressed size, not the body's: such a reply streams instead (#3564, as the unredacted
	// path does).
	if resp.ContentLength >= 0 && resp.ContentLength <= wholeReplyMax && !decoded {
		raw := make([]byte, resp.ContentLength)
		if _, err := io.ReadFull(body, raw); err != nil {
			s.Reject(CodeUpstream, "local response ended early", 0)
			return
		}
		out, ok := l.redactReply(resp.Header, raw)
		if !ok || !redactedJSONOK(resp.Header, raw, out) {
			l.respondRedactFailed(s)
			return
		}
		_ = s.RespondWhole(ResponseHeader{Status: resp.StatusCode, Headers: hdr}, out)
		return
	}
	if jsonReply(resp.Header) {
		// A JSON reply is held whole, redacted and checked before any of it is sent (#3694): one
		// that parsed before redaction and doesn't after is never passed on, it becomes the
		// could-not-be-checked answer. A JSON reply is never a live stream, so holding it costs
		// only the wait for its last byte. It fails closed (#3718, coral-hub Security): a reply
		// over jsonWholeMax is refused (never streamed unchecked), and one that can't get room in
		// the process-wide jsonBudget is answered busy, to retry.
		raw, release, why := readJSONBudgeted(body, resp.ContentLength, decoded, progress)
		defer release()
		switch why {
		case jsonTooLarge:
			respondJSONTooLarge(s)
			return
		case jsonNoBudget:
			respondJSONBusy(s)
			return
		case jsonReadFailed:
			s.Reject(CodeUpstream, "local response ended early", 0)
			return
		}
		out, ok := l.redactReply(resp.Header, raw)
		if !ok || !redactedJSONOK(resp.Header, raw, out) {
			l.respondRedactFailed(s)
			return
		}
		w, err := s.Respond(ResponseHeader{Status: resp.StatusCode, Headers: hdr})
		if err != nil {
			return
		}
		for len(out) > 0 {
			n := min(len(out), maxChunk)
			if _, err := w.Write(out[:n]); err != nil {
				return
			}
			out = out[n:]
			progress()
		}
		_ = w.Close()
		return
	}
	w, err := s.Respond(ResponseHeader{Status: resp.StatusCode, Headers: hdr})
	if err != nil {
		return
	}
	red := l.redactor().NewStream(w)
	buf := make([]byte, maxChunk)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			if !redactWrite(red, buf[:n]) {
				return // the scanner failed mid-stream: leave it unterminated, so the hub sees a truncated reply
			}
			progress()
		}
		if rerr != nil {
			if rerr == io.EOF && redactClose(red) {
				_ = w.Close()
			}
			return
		}
	}
}

// jsonWholeMax bounds one JSON reply serveRedacted holds to check it after redaction (#3718:
// 2 MiB, from 16). Real replies are far below it: the largest 100-message chat page measured on
// 277 MB, 199 MB and 172 MB transcripts was 193 KB. A bigger reply is refused, never streamed
// unchecked; the dashboard pages what could be bigger through Coral Hub (the history transcript,
// the board's export).
var jsonWholeMax = 2 << 20 // a var so a test can lower it

// jsonBudget bounds the bytes held by every JSON reply being checked at once: each holds its
// reply and the redacted copy, counted as they are read. Worst case held: 32 MiB, whatever the
// pools' sizes (it was a reply per stream times the cap, 2 GiB). A reply that can't get room is
// answered busy, to retry (respondJSONBusy).
var jsonBudget = newByteBudget(32 << 20)

// jsonInitialHold is what a JSON reply of unknown length reserves before its first byte (twice
// this, for the reply and its copy): more than any chat page, so a typical reply takes the budget
// once and never grows; a bigger one grows as it arrives. Reserving up front keeps replies that
// each hold part of the budget from starving one another. With the defaults about 64 typical
// replies fit at once (the general pool's size).
var jsonInitialHold = 256 << 10

// byteBudget is a count of bytes in use against a limit; take never waits.
type byteBudget struct {
	mu        sync.Mutex
	used, max int64
}

func newByteBudget(max int64) *byteBudget { return &byteBudget{max: max} }

// take reserves n bytes if they fit, and reports whether they did.
func (b *byteBudget) take(n int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used+n > b.max {
		return false
	}
	b.used += n
	return true
}

func (b *byteBudget) give(n int64) {
	b.mu.Lock()
	b.used -= n
	b.mu.Unlock()
}

type jsonReadResult int

const (
	jsonReadOK jsonReadResult = iota
	jsonTooLarge
	jsonNoBudget
	jsonReadFailed
)

// readJSONBudgeted reads a JSON reply whole, with room reserved in jsonBudget for it and its
// redacted copy (twice its bytes): for a reply of known length exactly that, before its first
// byte; for one of unknown length jsonInitialHold to start with, then more as it outgrows it. A
// reply of known length over jsonWholeMax is refused before a byte is read, one that turns out
// over it while being read too. release gives back everything reserved; the caller defers it on
// every path.
func readJSONBudgeted(body io.Reader, contentLength int64, decoded bool, progress func()) (raw []byte, release func(), why jsonReadResult) {
	var held int64
	release = func() {
		if held > 0 {
			jsonBudget.give(held)
			held = 0
		}
	}
	if contentLength > int64(jsonWholeMax) && !decoded {
		return nil, release, jsonTooLarge
	}
	first := int64(min(jsonInitialHold, jsonWholeMax))
	if contentLength >= 0 && !decoded {
		first = contentLength
	}
	if !jsonBudget.take(2 * first) {
		return nil, release, jsonNoBudget
	}
	held = 2 * first
	buf := make([]byte, maxChunk)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if len(raw)+n > jsonWholeMax {
				return nil, release, jsonTooLarge
			}
			if need := 2*int64(len(raw)+n) - held; need > 0 {
				if !jsonBudget.take(need) {
					return nil, release, jsonNoBudget
				}
				held += need
			}
			raw = append(raw, buf[:n]...)
			progress()
		}
		if err == io.EOF {
			return raw, release, jsonReadOK
		}
		if err != nil {
			return nil, release, jsonReadFailed
		}
	}
}

// respondJSONTooLarge refuses a JSON reply over jsonWholeMax: never sent unchecked (#3718).
func respondJSONTooLarge(s *Stream) {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	_ = s.RespondWhole(ResponseHeader{Status: http.StatusBadGateway, Headers: h},
		[]byte(`{"error":"this reply is too large to be checked for secrets through Coral Hub; open it on the computer","code":"hub_reply_too_large"}`))
}

// respondJSONBusy answers a JSON reply that can't get room in jsonBudget: busy, to retry, as a
// full pool is answered (busy), but 503 (the computer is busy, not the caller over a limit).
func respondJSONBusy(s *Stream) {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Retry-After", busyRetryAfter)
	h.Set(LimitHeader, "connector")
	h.Set("Cache-Control", "no-store")
	_ = s.RespondWhole(ResponseHeader{Status: http.StatusServiceUnavailable, Headers: h},
		[]byte(`{"error":"this computer is checking too many Coral Hub replies at once; try again shortly","code":"hub_busy"}`))
}

// jsonReply reports whether a reply's Content-Type is JSON (application/json or a +json type).
// It goes by the declared type: a JSON body sent as text/plain is streamed through the
// redactor's stream unchecked (Coral's own JSON endpoints all declare application/json).
func jsonReply(hdr http.Header) bool {
	mt, _, err := mime.ParseMediaType(hdr.Get("Content-Type"))
	return err == nil && (mt == "application/json" || strings.HasSuffix(mt, "+json"))
}

// redactReply redacts a reply held whole: by its structure when hdr declares JSON. A panic is a
// failure, so a reply is never sent unchecked.
func (l *LocalHandler) redactReply(hdr http.Header, raw []byte) (out []byte, ok bool) {
	defer func() {
		if recover() != nil {
			out, ok = nil, false
		}
	}()
	out, err := l.redactor().Reply(raw, jsonReply(hdr))
	return out, err == nil
}

// redactedJSONOK is false when a JSON reply that parsed before redaction doesn't after it (#3694:
// the redactor must never be what breaks a reply). A reply that didn't parse to begin with is
// the upstream's own, and is passed redacted as it is.
func redactedJSONOK(hdr http.Header, raw, out []byte) bool {
	return !jsonReply(hdr) || !json.Valid(raw) || json.Valid(out)
}

func (l *LocalHandler) respondRedactFailed(s *Stream) {
	redactFailLog.Do(func() { slog.Warn("[tunnel] a reply could not be checked for secrets; it was not sent to Coral Hub") })
	w, err := s.Respond(ResponseHeader{Status: http.StatusBadGateway, Headers: http.Header{"Content-Type": {"application/json"}}})
	if err != nil {
		return
	}
	_, _ = io.WriteString(w, redactFailed)
	_ = w.Close()
}

// redactWrite and redactClose turn an error or a panic in the stream into a failure, so a reply
// is never sent unscanned.
func redactWrite(r redact.Stream, b []byte) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	_, err := r.Write(b)
	return err == nil
}

func redactClose(r redact.Stream) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return r.Close() == nil
}

// bodyless reports whether a response to method with status carries no body.
func bodyless(method string, status int) bool {
	return method == http.MethodHead || status == http.StatusNoContent || status == http.StatusNotModified || (status >= 100 && status < 200)
}

// respondUnsupportedEncoding answers a local response whose Content-Encoding the
// connector will not relay. It is an answer (502 with a JSON error), not a stream
// failure, so the hub does not take the machine for unreachable.
func respondUnsupportedEncoding(s *Stream) {
	w, err := s.Respond(ResponseHeader{Status: http.StatusBadGateway, Headers: http.Header{"Content-Type": {"application/json"}}})
	if err != nil {
		return
	}
	_, _ = io.WriteString(w, `{"error":"unsupported content encoding"}`+"\n")
	_ = w.Close()
}

// rewriteLocation keeps redirects meaningful without leaking the loopback
// address: absolute same-host targets become relative, others are dropped.
func rewriteLocation(h http.Header) {
	loc := h.Get("Location")
	if loc == "" {
		return
	}
	u, err := url.Parse(loc)
	if err != nil {
		h.Del("Location")
		return
	}
	if u.IsAbs() || u.Host != "" {
		if ip := net.ParseIP(u.Hostname()); (ip != nil && ip.IsLoopback()) || u.Hostname() == "localhost" {
			u.Scheme, u.Host, u.User = "", "", nil
			h.Set("Location", u.String())
			return
		}
		h.Del("Location")
	}
}

func (l *LocalHandler) serveWS(ctx context.Context, s *Stream) {
	h := s.Header
	dec, ok := l.checkRequest(h)
	if !ok {
		l.reject(s)
		return
	}
	lm := l.lim.get(l.HubLimits)
	release, perUser, ok := lm.acquire(classLong, h.HubUser, l.limits())
	if !ok {
		lm.refused(classLong, dec, perUser)
		busy(s, perUser)
		return
	}
	defer release()
	hdr := l.stamp(scrub(h.Headers, http.MethodGet, dec))
	if l.Authorize != nil {
		l.Authorize(hdr)
	}
	t := l.target(h, dec)
	switch t.Scheme {
	case "https":
		t.Scheme = "wss"
	default:
		t.Scheme = "ws"
	}
	up, resp, err := websocket.Dial(ctx, t.String(), &websocket.DialOptions{HTTPClient: l.client, HTTPHeader: hdr, Subprotocols: h.Protocols})
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		s.Reject(CodeUpstream, "local websocket failed", status)
		return
	}
	lim := l.WSReadLimit
	if lim <= 0 {
		lim = DefaultWSReadLimit
	}
	up.SetReadLimit(lim)
	down, err := s.AcceptWS(up.Subprotocol())
	if err != nil {
		up.Close(websocket.StatusInternalError, "")
		return
	}
	down.SetReadLimit(lim)
	// Always gated: an unset MaxMode means view, so a LocalHandler built without one
	// never forwards terminal input.
	gate := func(data []byte) bool { return FrameTier(dec, data) <= currentMode(l.MaxMode) }
	var toHub *wsRedact
	if redactsRoute(http.MethodGet, dec) {
		// What a View stream sends to the hub is redacted (#3562); the setting is read per frame.
		toHub = newWSRedact(ctx, down, l.redactor(), l.redacting, strings.HasPrefix(dec, "/ws/terminal/"))
	}
	pumpWS(ctx, up, down, gate, toHub)
}

type wsEnd interface {
	Read(context.Context) (websocket.MessageType, []byte, error)
	Write(context.Context, websocket.MessageType, []byte) error
	Close(websocket.StatusCode, string) error
}

// pumpWS copies frames both ways. gate, when non-nil, vets frames travelling from b
// (the hub side) to a (the local server); a refused frame closes both ends. toHub, when
// non-nil, redacts the frames travelling from a to b (#3562).
func pumpWS(ctx context.Context, a, b wsEnd, gate func([]byte) bool, toHub *wsRedact) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); pump(ctx, cancel, a, b, nil, toHub) }()
	go func() { defer wg.Done(); pump(ctx, cancel, b, a, gate, nil) }()
	wg.Wait()
}

func pump(ctx context.Context, cancel context.CancelFunc, src, dst wsEnd, gate func([]byte) bool, red *wsRedact) {
	defer cancel()
	for {
		typ, data, err := src.Read(ctx)
		if err != nil {
			if red != nil {
				red.close() // what the stream still holds goes out before the close
			}
			code := websocket.CloseStatus(err)
			switch {
			case code == -1 || code == websocket.StatusNoStatusRcvd || code == websocket.StatusAbnormalClosure:
				dst.Close(websocket.StatusInternalError, "connection lost")
			default:
				reason := ""
				var ce websocket.CloseError
				if errors.As(err, &ce) {
					reason = ce.Reason
				}
				dst.Close(code, reason)
			}
			return
		}
		if gate != nil && !gate(data) {
			src.Close(websocket.StatusPolicyViolation, "not allowed in this access mode")
			dst.Close(websocket.StatusPolicyViolation, "not allowed in this access mode")
			return
		}
		if red != nil {
			if err := red.write(typ, data); err != nil {
				return
			}
			continue
		}
		if err := dst.Write(ctx, typ, data); err != nil {
			return
		}
	}
}

// bodyAllowed: whether a response to method with status carries the body its
// Content-Length announces (not HEAD, 1xx, 204 or 304).
func bodyAllowed(method string, status int) bool {
	return method != http.MethodHead && status >= 200 && status != http.StatusNoContent && status != http.StatusNotModified
}
