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
	// Bound the wait for headers after the upload, including for event streams.
	// A target can answer before it finishes reading the body. Serialize arming
	// with stopping so a late body read cannot restart the timer on that reply.
	var mu sync.Mutex
	var timer *time.Timer
	finished := false
	arm := func() {
		mu.Lock()
		defer mu.Unlock()
		if !finished && timer == nil {
			timer = time.AfterFunc(headerTimeout, cancel)
		}
	}
	stop := func() {
		mu.Lock()
		defer mu.Unlock()
		finished = true
		if timer != nil {
			timer.Stop()
		}
	}
	defer stop()
	request := r.WithContext(ctx)
	if r.Body == nil || r.Body == http.NoBody {
		arm()
	} else {
		request.Body = &headerTimeoutBody{ReadCloser: r.Body, remaining: r.ContentLength, complete: arm}
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
			stop()
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
			if r.Context().Err() != nil {
				return // browser went away
			}
			h.writeUpstreamErr(w, req.Context(), id, err)
		},
	}
	rp.ServeHTTP(deadlineWriter(w), request)
}

// headerTimeoutBody notifies the handler when a target consumes the upload.
// A known-length body need not be read again for EOF, so count its bytes too.
type headerTimeoutBody struct {
	io.ReadCloser
	remaining int64
	complete  func()
}

func (b *headerTimeoutBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if b.remaining > 0 {
		b.remaining -= int64(n)
		if b.remaining == 0 {
			b.complete()
		}
	}
	if err == io.EOF {
		b.complete()
	}
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
