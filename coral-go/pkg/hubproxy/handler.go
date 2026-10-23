package hubproxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	nethttputil "net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/subgentic/coral/pkg/tunnel"
)

// Handler is the /api/remote/{server}/* reverse proxy.
type Handler struct {
	resolver Resolver
	opts     Options
	optErr   error // invalid Options: every WebSocket upgrade is refused
}

// New returns a proxy Handler. Mount it with chi on a pattern containing a
// {server} URL param, e.g. r.Handle("/api/remote/{server}/*", h).
func New(resolver Resolver) *Handler { return &Handler{resolver: resolver} }

const mountPrefix = "/api/remote/"

func (h *Handler) writeErr(w http.ResponseWriter, code int, msg, server string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	body := map[string]string{"error": msg}
	if !h.opts.HideServerID {
		body["server"] = server
	}
	_ = json.NewEncoder(w).Encode(body)
}

// writeCodedErr is writeErr with a machine-readable code, for refusals the client acts on.
func (h *Handler) writeCodedErr(w http.ResponseWriter, status int, msg, code, server string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]string{"error": msg, "code": code}
	if !h.opts.HideServerID {
		body["server"] = server
	}
	_ = json.NewEncoder(w).Encode(body)
}

func isUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

func (h *Handler) mark(ctx context.Context, id, status, msg string) {
	if sr, ok := h.resolver.(StatusReporter); ok {
		sr.MarkStatus(context.WithoutCancel(ctx), id, status, msg)
	}
}

// upstreamPath extracts the escaped path after /api/remote/{server}.
func upstreamPath(r *http.Request) (string, bool) {
	p := r.URL.EscapedPath()
	i := strings.Index(p, mountPrefix)
	if i < 0 {
		return "", false
	}
	rest := p[i+len(mountPrefix):]
	j := strings.IndexByte(rest, '/')
	if j < 0 {
		return "", false
	}
	return rest[j:], true
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The Coral Hub user behind the request goes with every tunnel stream as Header.HubUser, so
	// the machine can cap what one viewer holds open (#3642). Read here, before the header
	// allowlist, which forwards X-Coral-Hub-User itself only on board writes.
	if vs := r.Header.Values(tunnel.HubUserHeader); len(vs) == 1 {
		r = r.WithContext(tunnel.WithHubUser(r.Context(), vs[0]))
	}
	id := chi.URLParam(r, "server")
	rawPath, ok := upstreamPath(r)
	if id == "" || !ok {
		h.writeErr(w, http.StatusNotFound, "unknown server", id)
		return
	}
	upgrade := isUpgrade(r)

	_, rawPath, err := SanitizePath(rawPath)
	if err != nil {
		h.writeErr(w, http.StatusBadRequest, "invalid path", id)
		return
	}
	dec, _ := url.PathUnescape(rawPath)
	if (upgrade && !wsPathAllowed(dec)) || (!upgrade && !httpPathAllowed(dec)) {
		h.writeErr(w, http.StatusForbidden, "path not allowed", id)
		return
	}

	t, err := h.resolver.Resolve(r.Context(), id)
	switch {
	case errors.Is(err, ErrUnknownServer):
		h.writeErr(w, http.StatusNotFound, "unknown server", id)
		return
	case errors.Is(err, ErrOffline):
		h.writeErr(w, http.StatusBadGateway, "remote unreachable", id)
		return
	case errors.Is(err, ErrKeyUnreadable):
		h.mark(r.Context(), id, StatusKeyUnreadable, "stored key cannot be decrypted")
		h.writeErr(w, http.StatusBadGateway, "remote key unreadable", id)
		return
	case err != nil:
		slog.Warn("remote proxy: resolve failed", "server", id, "error", err)
		h.writeErr(w, http.StatusBadGateway, "remote unavailable", id)
		return
	}
	if v, ok := t.(Validator); ok {
		if err := v.Validate(); err != nil {
			if errors.Is(err, ErrBlocked) {
				h.writeErr(w, http.StatusBadGateway, ErrBlocked.Error(), id)
			} else {
				h.writeErr(w, http.StatusBadGateway, "remote unreachable", id)
			}
			return
		}
	}

	if upgrade {
		h.bridgeWS(w, r, id, t, rawPath)
		return
	}
	h.proxyHTTP(w, r, id, t, dec, rawPath)
}

func (h *Handler) proxyHTTP(w http.ResponseWriter, r *http.Request, id string, t Target, dec, raw string) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	deadlines := &proxyDeadlines{cancel: cancel, abortBody: func() {
		cancel()
		// Context cancellation stops the upstream; a read deadline also releases
		// a transport goroutine blocked reading the browser's request body.
		_ = http.NewResponseController(w).SetReadDeadline(time.Now())
	}}
	defer deadlines.stop()
	request := r.WithContext(ctx)
	if r.Body == nil || r.Body == http.NoBody {
		deadlines.bodyProgress(0, true)
	} else {
		deadlines.startBody()
		request.Body = &headerTimeoutBody{ReadCloser: r.Body, remaining: r.ContentLength, progress: deadlines.bodyProgress}
	}

	rp := &nethttputil.ReverseProxy{
		Transport:     targetTransport{t},
		FlushInterval: -1,
		Rewrite: func(pr *nethttputil.ProxyRequest) {
			out := pr.Out
			out.URL.Path, out.URL.RawPath = dec, raw
			out.URL.RawQuery = stripAPIKey(pr.In.URL.RawQuery)
			// Untrusted far side: allowlist, not denylist, on every target (#4297). A direct remote
			// got only the denylist below, so a browser's X-API-Key, X-Coral-* and Sec-Fetch-* went
			// downstream; the Authorization the target sets in RoundTrip is added after this.
			enc := out.Header.Values("Accept-Encoding")
			allowlistHeaders(out.Header, out.Method, dec)
			if !isTunnel(t) {
				dropHubUserHeader(out.Header) // only a tunnel carries a Hub user the connector vouches for
				// A direct remote relays gzip unchanged (the connector decodes for a tunnel, which is
				// why Accept-Encoding is not on the allowlist): it is no secret, so it goes on.
				for _, v := range enc {
					out.Header.Add("Accept-Encoding", v)
				}
			}
			for _, hd := range []string{"Authorization", "Cookie", "Origin", "Referer", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip"} {
				out.Header.Del(hd)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			deadlines.headersDone()
			// Only the remote's own key refusal means the key was rejected (#3649): a
			// 401 that is an application answer (session_token_required for a call
			// without the remote's session token) or a 403 (demo limit reached,
			// forbidden path, ...) is passed through; treating it as a key failure
			// would mark a healthy server "unauthorized" and hide the real error. A
			// refused Host is reported as such.
			if err := Refusal(resp); err != nil {
				return err
			}
			// The remote's own static build is not the build of the page this hub serves. The reverse proxy would append it to
			// the hub's X-Coral-Build, and the dashboard's update banner would read "hub, remote" and never match (#3990).
			resp.Header.Del("X-Coral-Build")
			switch {
			case h.opts.ResponseFilter != nil:
				return h.opts.ResponseFilter(resp)
			case isTunnel(t):
				// The hub's security middleware may already have set Cache-Control on w, and the reverse proxy ADDS
				// the filtered upstream value as a second header line. The filter owns the field for relayed content.
				w.Header().Del("Cache-Control")
				return StrictResponseFilter(resp)
			}
			// A direct remote's bytes are as untrusted as a connector's (#4297): a page it answers under
			// /api/remote/<id>/api/... would otherwise run on this origin.
			w.Header().Del("Cache-Control")
			return directResponseFilter(resp)
		},
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			if r.Context().Err() != nil && !deadlines.bodyExpired() {
				return // browser went away
			}
			if deadlines.bodyExpired() {
				// The browser's upload was too slow or stopped: its fault, not the machine's outage, so the
				// server is not marked unreachable and the reply says which limit it hit.
				msg := "upload stalled"
				if deadlines.uploadCut() == CodeUploadTooSlow {
					msg = "upload too slow"
				}
				h.writeCodedErr(w, http.StatusRequestTimeout, msg, deadlines.uploadCut(), id)
				return
			}
			h.writeUpstreamErr(w, req.Context(), id, err)
		},
	}
	rp.ServeHTTP(deadlineWriter(w), request)
}

const (
	uploadStallTimeout = 30 * time.Second
	uploadTimeout      = 10 * time.Minute
)

// proxyDeadlines bounds the upload separately from waiting for response headers.
// An early response stops only the header timer: its upload still has bounds.
// Progress means the target consumed body bytes; a target that stops reading
// for 30 s also counts as a stall, even if the browser has more bytes available.
// All transitions, including timeout callbacks, serialize with handler cleanup.
type proxyDeadlines struct {
	mu                                 sync.Mutex
	cancel                             context.CancelFunc
	abortBody                          func()
	header, idle, total                *time.Timer
	lastProgress                       time.Time
	bodyDone, headersReceived, stopped bool
	bodyTimedOut                       bool
	bodyStalled                        bool // the cut was the stall limit, not the total cap
}

func (d *proxyDeadlines) startBody() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastProgress = time.Now()
	d.idle = time.AfterFunc(uploadStallTimeout, func() { d.expireBody(true) })
	d.total = time.AfterFunc(uploadTimeout, func() { d.expireBody(false) })
}

func (d *proxyDeadlines) expireBody(idle bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped || d.bodyDone {
		return
	}
	if idle {
		// A read may have made progress as this callback was starting. Check
		// the current idle deadline instead of trusting a stale timer firing.
		if left := uploadStallTimeout - time.Since(d.lastProgress); left > 0 {
			d.idle.Reset(left)
			return
		}
	}
	d.bodyTimedOut = true
	d.bodyStalled = idle
	d.abortBody()
}

func (d *proxyDeadlines) bodyProgress(n int, complete bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped || d.bodyDone {
		return
	}
	if n > 0 {
		d.lastProgress = time.Now()
	}
	if complete {
		d.bodyDone = true
		stopTimer(d.idle)
		stopTimer(d.total)
		if !d.headersReceived {
			d.header = time.AfterFunc(headerTimeout, d.cancel)
		}
	}
}

func (d *proxyDeadlines) headersDone() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.headersReceived = true
	stopTimer(d.header)
}

// Expiring the browser read deadline can cancel its request context as well;
// retain the timeout reply instead of treating that as a browser disconnect.
func (d *proxyDeadlines) bodyExpired() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.bodyTimedOut
}

// Codes a cut upload answers (408, "code" in the body), for the dashboard to say which limit it hit (#6190).
const (
	CodeUploadStalled = "upload_stalled"
	CodeUploadTooSlow = "upload_too_slow"
)

// uploadCut is the code for an upload the proxy ended: the stall limit or the total cap.
func (d *proxyDeadlines) uploadCut() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.bodyStalled {
		return CodeUploadStalled
	}
	return CodeUploadTooSlow
}

func (d *proxyDeadlines) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopped = true
	stopTimer(d.header)
	stopTimer(d.idle)
	stopTimer(d.total)
}

func stopTimer(t *time.Timer) {
	if t != nil {
		t.Stop()
	}
}

// headerTimeoutBody notifies the handler when a target consumes the upload.
// A known-length body need not be read again for EOF, so count its bytes too.
type headerTimeoutBody struct {
	io.ReadCloser
	remaining int64
	progress  func(int, bool)
}

func (b *headerTimeoutBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	complete := err == io.EOF
	if b.remaining > 0 {
		b.remaining -= int64(n)
		complete = complete || b.remaining == 0
	}
	b.progress(n, complete)
	return n, err
}

// respWriteTimeout bounds each write of a proxied reply to the browser (#4172). A browser that
// stops reading (a tab in the background, a stalled link) would otherwise block the proxy's copy
// of the reply, and with it the machine's stream and its slot, for as long as the connection
// lasts: an event stream has no idle timer to end it. The limit applies to a write that cannot
// make progress, never to the silence between events.
var respWriteTimeout = 15 * time.Second

// deadlineWriter is w with a write deadline of respWriteTimeout set before every write and flush,
// through http.ResponseController (a writer without deadline support is used as it is).
func deadlineWriter(w http.ResponseWriter) http.ResponseWriter {
	return &deadlineResponseWriter{ResponseWriter: w, rc: http.NewResponseController(w)}
}

type deadlineResponseWriter struct {
	http.ResponseWriter
	rc *http.ResponseController
}

func (d *deadlineResponseWriter) arm() { _ = d.rc.SetWriteDeadline(time.Now().Add(respWriteTimeout)) }

func (d *deadlineResponseWriter) Write(p []byte) (int, error) {
	d.arm()
	return d.ResponseWriter.Write(p)
}

func (d *deadlineResponseWriter) Flush() {
	d.arm()
	_ = d.rc.Flush()
}

// Unwrap lets http.ResponseController reach the connection's own methods.
func (d *deadlineResponseWriter) Unwrap() http.ResponseWriter { return d.ResponseWriter }

func (h *Handler) writeUpstreamErr(w http.ResponseWriter, ctx context.Context, id string, err error) {
	var se *tunnel.StreamError
	switch {
	case errors.As(err, &se) && se.Code == tunnel.CodeLocalMode:
		// The machine's own setting is below what the hub allows. Say so; this is not an outage,
		// so the server is not marked unreachable.
		mode := se.Message
		if mode != "view" && mode != "interact" {
			mode = "view"
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "local_mode", "mode": mode})
	case errors.As(err, &se) && se.Code == tunnel.CodePathNotAllowed:
		// The connector's route table refused this path or query (for example an older machine that does not
		// accept a newer parameter). That is an answer, not an outage: 403, and the server is not marked.
		h.writeCodedErr(w, http.StatusForbidden, "path not allowed", tunnel.CodePathNotAllowed, id)
	case errors.As(err, &se) && se.Code == tunnel.CodeTooManyStreams, errors.Is(err, tunnel.ErrTooManyStreams):
		// The connector (or this session) is at its concurrent-stream cap: a busy dashboard, not an outage.
		// 503 with a short Retry-After, and the server keeps its status.
		w.Header().Set("Retry-After", "1")
		h.writeCodedErr(w, http.StatusServiceUnavailable, "remote busy", tunnel.CodeTooManyStreams, id)
	case errors.As(err, &se) && se.Code == tunnel.CodeUpstream && se.Status == http.StatusTooManyRequests:
		// The machine's own Coral refused the upgrade for being busy (its cap on open terminals or dashboards, #5011): the machine is
		// reachable and says so. Busy like the stream cap, with the Retry-After it gave (a short default when it gave none).
		retry := se.RetryAfter
		if retry == "" {
			retry = "5"
		}
		w.Header().Set("Retry-After", retry)
		h.writeCodedErr(w, http.StatusServiceUnavailable, "remote busy", tunnel.CodeTooManyStreams, id)
	case errors.As(err, &se) && se.Code == tunnel.CodeBadRequest:
		// The connector could not accept this request as sent (bad header, method or body). The request is at
		// fault, not the machine: 400, not marked.
		h.writeCodedErr(w, http.StatusBadRequest, "bad request", tunnel.CodeBadRequest, id)
	case errors.Is(err, ErrRemoteRejected):
		h.mark(ctx, id, StatusUnauthorized, "remote rejected API key")
		h.writeErr(w, http.StatusBadGateway, "remote rejected API key", id)
	case errors.Is(err, ErrHostRefused):
		h.mark(ctx, id, StatusHostRefused, ErrHostRefused.Error())
		h.writeErr(w, http.StatusBadGateway, ErrHostRefused.Error(), id)
	case errors.Is(err, ErrBlocked):
		h.writeErr(w, http.StatusBadGateway, ErrBlocked.Error(), id)
	default:
		h.mark(ctx, id, StatusUnreachable, "connect failed")
		h.writeErr(w, http.StatusBadGateway, "remote unreachable", id)
	}
}

// targetTransport adapts a Target to http.RoundTripper for ReverseProxy.
type targetTransport struct{ t Target }

func (tt targetTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return tt.t.RoundTrip(req)
}
