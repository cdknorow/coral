package tunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"nhooyr.io/websocket"
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

// Allow reports whether the request may reach the local API.
func (p PathRules) Allow(method, decodedPath string) bool {
	if !allowedMethods[method] {
		return false
	}
	ok := false
	for _, r := range p.Rules {
		if r.Exact && decodedPath != r.Prefix || !r.Exact && !strings.HasPrefix(decodedPath, r.Prefix) {
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
	// MaxRequestBody caps uploads, default 256 MiB.
	MaxRequestBody int64
	// WSReadLimit caps one WebSocket message, default DefaultWSReadLimit.
	WSReadLimit int64

	once   sync.Once
	client *http.Client
	base   *url.URL
	err    error
}

func (l *LocalHandler) init() error {
	l.once.Do(func() { l.client, l.base, l.err = NewLoopbackClient(l.BaseURL) })
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
}

func scrub(h http.Header) http.Header {
	out := http.Header{}
	for k, vs := range h {
		if forwardRequestHeaders[http.CanonicalHeaderKey(k)] {
			out[http.CanonicalHeaderKey(k)] = append([]string(nil), vs...)
		}
	}
	return out
}

func (l *LocalHandler) checkRequest(h Header) (decoded string, ok bool) {
	if err := ValidatePath(h.Path); err != nil || !validQuery(h.Query) {
		return "", false
	}
	if h.Kind == KindHTTP && !strings.HasPrefix(h.Path, "/api/") {
		return "", false // /ws/ is for WebSocket streams only
	}
	decoded, err := url.PathUnescape(h.Path)
	if err != nil {
		return "", false
	}
	method := h.Method
	if h.Kind == KindWS {
		method = http.MethodGet
	}
	if l.AllowPath == nil || !l.AllowPath(method, decoded) {
		return "", false // default deny
	}
	return decoded, true
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
		s.Reject(CodePathNotAllowed, "path not allowed", 0)
		return
	}
	if !allowedMethods[h.Method] {
		s.Reject(CodeBadRequest, "method not allowed", 0)
		return
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
	req.Header = scrub(h.Headers)
	if h.ContentLength > 0 {
		req.ContentLength = h.ContentLength
	}
	if l.Authorize != nil {
		l.Authorize(req.Header)
	}
	resp, err := l.client.Do(req)
	if err != nil {
		s.Reject(CodeUpstream, "local request failed", 0)
		return
	}
	defer resp.Body.Close()
	hdr := resp.Header.Clone()
	for _, k := range []string{"Connection", "Keep-Alive", "Transfer-Encoding", "Upgrade", "Proxy-Authenticate", "Te", "Trailer"} {
		hdr.Del(k)
	}
	hdr.Del("Content-Length") // the body is re-framed
	rewriteLocation(hdr)
	w, err := s.Respond(ResponseHeader{Status: resp.StatusCode, Headers: hdr})
	if err != nil {
		return
	}
	buf := make([]byte, maxChunk)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				_ = w.Close()
			}
			return // anything else: leave the body unterminated so the hub sees a truncated response
		}
	}
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
		s.Reject(CodePathNotAllowed, "path not allowed", 0)
		return
	}
	hdr := scrub(h.Headers)
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
	pumpWS(ctx, up, down)
}

type wsEnd interface {
	Read(context.Context) (websocket.MessageType, []byte, error)
	Write(context.Context, websocket.MessageType, []byte) error
	Close(websocket.StatusCode, string) error
}

func pumpWS(ctx context.Context, a, b wsEnd) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); pump(ctx, cancel, a, b) }()
	go func() { defer wg.Done(); pump(ctx, cancel, b, a) }()
	wg.Wait()
}

func pump(ctx context.Context, cancel context.CancelFunc, src, dst wsEnd) {
	defer cancel()
	for {
		typ, data, err := src.Read(ctx)
		if err != nil {
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
		if err := dst.Write(ctx, typ, data); err != nil {
			return
		}
	}
}
