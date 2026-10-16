package hubproxy

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	nethttputil "net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/cdknorow/coral/pkg/tunnel"
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
	stream := strings.Contains(r.Header.Get("Accept"), "text/event-stream")
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	var timer *time.Timer
	if !stream {
		timer = time.AfterFunc(headerTimeout, cancel)
		defer timer.Stop()
	}

	rp := &nethttputil.ReverseProxy{
		Transport:     targetTransport{t},
		FlushInterval: -1,
		Rewrite: func(pr *nethttputil.ProxyRequest) {
			out := pr.Out
			out.URL.Path, out.URL.RawPath = dec, raw
			out.URL.RawQuery = stripAPIKey(pr.In.URL.RawQuery)
			if isTunnel(t) {
				allowlistHeaders(out.Header, out.Method, dec) // untrusted far side: allowlist, not denylist
			}
			for _, hd := range []string{"Authorization", "Cookie", "Origin", "Referer", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip"} {
				out.Header.Del(hd)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			if timer != nil {
				timer.Stop()
			}
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
			resp.Header.Del("Set-Cookie")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			if r.Context().Err() != nil {
				return // browser went away
			}
			h.writeUpstreamErr(w, req.Context(), id, err)
		},
	}
	rp.ServeHTTP(w, r.WithContext(ctx))
}

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
