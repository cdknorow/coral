package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"nhooyr.io/websocket"
)

const wsReadLimit = 16 << 20

// wsURL builds the ws/wss URL for an upstream path.
func wsURL(base *url.URL, raw, rawQuery string) string {
	scheme := "ws"
	if base.Scheme == "https" {
		scheme = "wss"
	}
	dec, _ := url.PathUnescape(raw)
	u := url.URL{Scheme: scheme, Host: base.Host, Path: dec, RawPath: raw, RawQuery: stripAPIKey(rawQuery)}
	return u.String()
}

// DialWebSocket dials {t.URL}{path} as a websocket with the API key, using the
// same SSRF safeguards as the proxy (validation plus dial-time IP check).
// path is an escaped path under /ws/ or /api/. A 401/403 handshake response
// yields ErrRemoteRejected. protocols are requested subprotocols.
func DialWebSocket(ctx context.Context, t Target, path, rawQuery string, protocols []string) (*websocket.Conn, error) {
	_, raw, err := SanitizePath(path)
	if err != nil {
		return nil, err
	}
	dec, _ := url.PathUnescape(raw)
	if !wsPathAllowed(dec) {
		return nil, ErrPathNotAllowed
	}
	base, err := ValidateTarget(t)
	if err != nil {
		return nil, err
	}
	return dialWS(ctx, t, base, raw, rawQuery, protocols)
}

func dialWS(ctx context.Context, t Target, base *url.URL, raw, rawQuery string, protocols []string) (*websocket.Conn, error) {
	conn, resp, err := websocket.Dial(ctx, wsURL(base, raw, rawQuery), &websocket.DialOptions{
		HTTPClient:   &http.Client{Transport: transport(t.AllowPrivate)},
		HTTPHeader:   http.Header{"Authorization": []string{"Bearer " + t.APIKey}},
		Subprotocols: protocols,
	})
	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
			return nil, ErrRemoteRejected
		}
		return nil, err
	}
	conn.SetReadLimit(wsReadLimit)
	return conn, nil
}

func requestedProtocols(r *http.Request) []string {
	var out []string
	for _, v := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func (h *Handler) bridgeWS(w http.ResponseWriter, r *http.Request, id string, t Target, base *url.URL, dec, raw string) {
	// Dial the remote first so failures can still be reported as HTTP errors.
	up, err := dialWS(r.Context(), t, base, raw, r.URL.RawQuery, requestedProtocols(r))
	if err != nil {
		h.writeUpstreamErr(w, r.Context(), id, err)
		return
	}

	patterns := []string{"localhost", "localhost:*", "127.0.0.1", "127.0.0.1:*", "[::1]", "[::1]:*"}
	if r.Host != "" {
		patterns = append(patterns, r.Host)
	}
	opts := &websocket.AcceptOptions{OriginPatterns: patterns}
	if sp := up.Subprotocol(); sp != "" {
		opts.Subprotocols = []string{sp}
	}
	down, err := websocket.Accept(w, r, opts)
	if err != nil {
		up.Close(websocket.StatusGoingAway, "browser handshake failed")
		return
	}
	down.SetReadLimit(wsReadLimit)

	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); pump(ctx, cancel, down, up, "remote connection lost") }()
	go func() { defer wg.Done(); pump(ctx, cancel, up, down, "browser connection lost") }()
	wg.Wait()
}

// pump copies frames from src to dst. When src ends, dst is closed with src's
// close code and reason if it sent one, or with 1011 and lostMsg if it dropped.
func pump(ctx context.Context, cancel context.CancelFunc, src, dst *websocket.Conn, lostMsg string) {
	defer cancel()
	for {
		typ, data, err := src.Read(ctx)
		if err != nil {
			code := websocket.CloseStatus(err)
			reason := ""
			var ce websocket.CloseError
			if errors.As(err, &ce) {
				reason = ce.Reason
			}
			switch {
			case ctx.Err() != nil && code == -1:
				// Peer pump already finished and is closing both sides.
				dst.Close(websocket.StatusGoingAway, "")
			case code == -1:
				dst.Close(websocket.StatusInternalError, lostMsg)
			case code == websocket.StatusNoStatusRcvd || code == websocket.StatusAbnormalClosure || code == websocket.StatusTLSHandshake:
				dst.Close(websocket.StatusNormalClosure, "")
			default:
				if len(reason) > 123 {
					reason = reason[:123]
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
