package hubproxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

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
func DialWebSocket(ctx context.Context, t Target, path, rawQuery string, protocols []string) (WSConn, error) {
	_, raw, err := SanitizePath(path)
	if err != nil {
		return nil, err
	}
	dec, _ := url.PathUnescape(raw)
	if !wsPathAllowed(dec) {
		return nil, ErrPathNotAllowed
	}
	if v, ok := t.(Validator); ok {
		if err := v.Validate(); err != nil {
			return nil, err
		}
	}
	return t.DialWS(ctx, raw, rawQuery, protocols)
}

func dialWS(ctx context.Context, t HTTPTarget, base *url.URL, raw, rawQuery string, protocols []string) (*websocket.Conn, error) {
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

// legacyOrigins are the self-hosted hub's default WebSocket origins.
var legacyOrigins = []string{"localhost", "localhost:*", "127.0.0.1", "127.0.0.1:*", "[::1]", "[::1]:*"}

// originPatterns returns the host patterns a WebSocket Origin may match and
// whether an Origin header is required. ok is false when the upgrade must be
// refused outright (a tunnel target with no configured origin).
func (h *Handler) originPatterns(r *http.Request, t Target) (patterns []string, required, ok bool) {
	if h.optErr != nil {
		return nil, true, false
	}
	if len(h.opts.AllowedOrigins) > 0 {
		return h.opts.AllowedOrigins, true, true
	}
	if isTunnel(t) {
		return nil, true, false
	}
	patterns = append([]string(nil), legacyOrigins...)
	if r.Host != "" {
		patterns = append(patterns, r.Host)
	}
	return patterns, false, true
}

// originAllowed checks the browser's Origin against patterns. It runs before
// anything is dialled, so a hostile page cannot make the hub open a stream to
// the user's machine even if the browser then refuses the handshake.
func originAllowed(r *http.Request, patterns []string, required bool) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return !required
	}
	if o == "null" {
		return false
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	for _, p := range patterns {
		if m, err := path.Match(strings.ToLower(p), strings.ToLower(u.Host)); err == nil && m {
			return true
		}
	}
	return false
}

func (h *Handler) bridgeWS(w http.ResponseWriter, r *http.Request, id string, t Target, raw string) {
	patterns, required, ok := h.originPatterns(r, t)
	if !ok || !originAllowed(r, patterns, required) {
		h.writeErr(w, http.StatusForbidden, "origin not allowed", id)
		return
	}
	// Dial the remote first so failures can still be reported as HTTP errors.
	up, err := t.DialWS(r.Context(), raw, r.URL.RawQuery, requestedProtocols(r))
	if err != nil {
		h.writeUpstreamErr(w, r.Context(), id, err)
		return
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
	pol := PolicyFrom(r.Context())
	if pol.Closed != nil {
		go func() {
			select {
			case <-pol.Closed:
				// Access was withdrawn: end both sides with a code the UI can recognise.
				down.Close(StatusAccessChanged, "access changed")
				up.Close(websocket.StatusGoingAway, "")
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		pumpFrom(ctx, cancel, down, up, "remote connection lost", clientGate{pol.AllowClientFrame, pol.ReadOnlyNotice}, pol.Closed)
	}()
	go func() {
		defer wg.Done()
		pumpFrom(ctx, cancel, up, down, "browser connection lost", clientGate{}, pol.Closed)
	}()
	wg.Wait()
}

// pump copies frames from src to dst. When src ends, dst is closed with src's
// close code and reason if it sent one, or with 1011 and lostMsg if it dropped.
func pump(ctx context.Context, cancel context.CancelFunc, src, dst WSConn, lostMsg string) {
	pumpFrom(ctx, cancel, src, dst, lostMsg, clientGate{}, nil)
}

// clientGate is the read-only gate on frames from the browser (StreamPolicy).
type clientGate struct {
	allow  func() bool
	notice func() map[string]any
}

// readOnlyNoticeEvery bounds the notices for dropped frames without an id (keystrokes).
const readOnlyNoticeEvery = 2 * time.Second

// frameID returns the integer "id" of a JSON text frame, if it has one (#3276). Only an
// integer that fits a JSON-safe range is taken; anything else is no id.
func frameID(typ websocket.MessageType, data []byte) (int64, bool) {
	if typ != websocket.MessageText || len(data) == 0 || data[0] != '{' {
		return 0, false
	}
	var f struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(data, &f) != nil || len(f.ID) == 0 || len(f.ID) > 16 {
		return 0, false
	}
	// A bare non-negative integer literal only: no string, sign, fraction or exponent.
	for _, c := range f.ID {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(string(f.ID), 10, 64)
	if err != nil || id > 1<<53 {
		return 0, false
	}
	return id, true
}

// pumpFrom is pump with an optional gate: while gate.allow returns false, frames read from
// src are dropped instead of forwarded (a read-only terminal), and src is told when the gate
// has a notice.
func pumpFrom(ctx context.Context, cancel context.CancelFunc, src, dst WSConn, lostMsg string, gate clientGate, withdrawn <-chan struct{}) {
	defer cancel()
	var lastNotice time.Time
	for {
		typ, data, err := src.Read(ctx)
		if err != nil {
			if withdrawn != nil {
				select {
				case <-withdrawn:
					// Access was withdrawn (this is why the other side dropped): say so.
					dst.Close(StatusAccessChanged, "access changed")
					return
				default:
				}
			}
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
		if gate.allow != nil && !gate.allow() {
			if gate.notice != nil && typ == websocket.MessageText {
				id, hasID := frameID(typ, data)
				if hasID || time.Since(lastNotice) >= readOnlyNoticeEvery {
					frame := map[string]any{}
					for k, v := range gate.notice() {
						frame[k] = v
					}
					if hasID {
						frame["id"] = id
					}
					if b, err := json.Marshal(frame); err == nil {
						src.Write(ctx, websocket.MessageText, b)
					}
					lastNotice = time.Now()
				}
			}
			continue
		}
		if err := dst.Write(ctx, typ, data); err != nil {
			return
		}
	}
}
