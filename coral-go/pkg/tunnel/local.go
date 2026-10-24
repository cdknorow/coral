package tunnel

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/pkg/redact"
)

// ── Path guard ──────────────────────────────────────────────────────

// ── Loopback-only client ────────────────────────────────────────────

// ── Route rules ─────────────────────────────────────────────────────

// ── LocalHandler ────────────────────────────────────────────────────

// LocalHandler executes tunnel streams against the local Coral API on
// loopback, enforcing the connector-side guards whatever the hub sends.
type LocalHandler struct {
	wsMu    sync.Mutex // guards openWSs: the websocket streams being forwarded (CloseAbove, #5918)
	openWSs map[*openWS]struct{}
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
	class := classify(h.Kind, dec, h.Query)
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
	stampRole(req.Header, h, h.Method, dec)
	stampTeam(req.Header, h, h.Method, dec)
	stampUploader(req.Header, h, h.Method, dec)
	stampLinks(req.Header, s, h.Method, dec)
	redactRoute := l.redactFor(viewerTier(h))() && redactsRoute(h.Method, dec)
	dropFields := dropsFor(h.Method, dec) && l.viewOnly(viewerTier(h))()
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
	if dropFields && jsonReply(resp.Header) && bodyAllowed(s.Header.Method, resp.StatusCode) {
		// A viewer who can only look gets no waiting_summary (viewdrop.go): the reply is read whole, the field is nulled, and what is sent on is the new body (its length is not the upstream's).
		body, rerr := io.ReadAll(io.LimitReader(respBody, viewDropMax+1))
		if rerr != nil || len(body) > viewDropMax {
			s.Reject(CodeUpstream, "local response could not be prepared for this viewer", 0)
			return
		}
		out, ok := stripViewDropped(body)
		if !ok {
			s.Reject(CodeUpstream, "local response could not be prepared for this viewer", 0)
			return
		}
		respBody, decoded = bytes.NewReader(out), true
		hdr.Del(BodyLengthHeader)
	}
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
	w = guardEventStream(s, resp.Header, w)
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

// a var so a test can lower it

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
