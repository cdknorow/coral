package proxy

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
)

// Handler is the /api/remote/{server}/* reverse proxy.
type Handler struct {
	resolver Resolver
}

// New returns a proxy Handler. Mount it with chi on a pattern containing a
// {server} URL param, e.g. r.Handle("/api/remote/{server}/*", h).
func New(resolver Resolver) *Handler { return &Handler{resolver: resolver} }

const mountPrefix = "/api/remote/"

func writeErr(w http.ResponseWriter, code int, msg, server string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg, "server": server})
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
	id := chi.URLParam(r, "server")
	rawPath, ok := upstreamPath(r)
	if id == "" || !ok {
		writeErr(w, http.StatusNotFound, "unknown server", id)
		return
	}
	upgrade := isUpgrade(r)

	_, rawPath, err := SanitizePath(rawPath)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid path", id)
		return
	}
	dec, _ := url.PathUnescape(rawPath)
	if (upgrade && !wsPathAllowed(dec)) || (!upgrade && !httpPathAllowed(dec)) {
		writeErr(w, http.StatusForbidden, "path not allowed", id)
		return
	}

	t, err := h.resolver.Resolve(r.Context(), id)
	switch {
	case errors.Is(err, ErrUnknownServer):
		writeErr(w, http.StatusNotFound, "unknown server", id)
		return
	case errors.Is(err, ErrKeyUnreadable):
		h.mark(r.Context(), id, StatusKeyUnreadable, "stored key cannot be decrypted")
		writeErr(w, http.StatusBadGateway, "remote key unreadable", id)
		return
	case err != nil:
		slog.Warn("remote proxy: resolve failed", "server", id, "error", err)
		writeErr(w, http.StatusBadGateway, "remote unavailable", id)
		return
	}
	base, err := ValidateTarget(t)
	if err != nil {
		if errors.Is(err, ErrBlocked) {
			writeErr(w, http.StatusBadGateway, ErrBlocked.Error(), id)
		} else {
			writeErr(w, http.StatusBadGateway, "remote unreachable", id)
		}
		return
	}

	if upgrade {
		h.bridgeWS(w, r, id, t, base, dec, rawPath)
		return
	}
	h.proxyHTTP(w, r, id, t, base, dec, rawPath)
}

func (h *Handler) proxyHTTP(w http.ResponseWriter, r *http.Request, id string, t Target, base *url.URL, dec, raw string) {
	stream := strings.Contains(r.Header.Get("Accept"), "text/event-stream")
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	var timer *time.Timer
	if !stream {
		timer = time.AfterFunc(headerTimeout, cancel)
		defer timer.Stop()
	}

	rp := &nethttputil.ReverseProxy{
		Transport:     transport(t.AllowPrivate),
		FlushInterval: -1,
		Rewrite: func(pr *nethttputil.ProxyRequest) {
			out := pr.Out
			out.URL.Scheme = base.Scheme
			out.URL.Host = base.Host
			out.URL.Path, out.URL.RawPath = dec, raw
			out.URL.RawQuery = stripAPIKey(pr.In.URL.RawQuery)
			out.Host = base.Host
			for _, hd := range []string{"Authorization", "Cookie", "Origin", "Referer", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip"} {
				out.Header.Del(hd)
			}
			out.Header.Set("Authorization", "Bearer "+t.APIKey)
		},
		ModifyResponse: func(resp *http.Response) error {
			if timer != nil {
				timer.Stop()
			}
			// Only 401 means the key was rejected. A remote 403 is an ordinary
			// application answer (demo limit reached, forbidden path, ...) and
			// is passed through; treating it as a key failure would mark a
			// healthy server "unauthorized" and hide the real error.
			if resp.StatusCode == http.StatusUnauthorized {
				return ErrRemoteRejected
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
	switch {
	case errors.Is(err, ErrRemoteRejected):
		h.mark(ctx, id, StatusUnauthorized, "remote rejected API key")
		writeErr(w, http.StatusBadGateway, "remote rejected API key", id)
	case errors.Is(err, ErrBlocked):
		writeErr(w, http.StatusBadGateway, ErrBlocked.Error(), id)
	default:
		h.mark(ctx, id, StatusUnreachable, "connect failed")
		writeErr(w, http.StatusBadGateway, "remote unreachable", id)
	}
}
