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
	"unicode"
	"unicode/utf8"

	"github.com/cdknorow/coral/internal/safego"
	"github.com/cdknorow/coral/pkg/tunnel"
	"github.com/coder/websocket"
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
	// Never forward the browser's api_key (#3356): the target adds its own credential. The
	// HTTP target's wsURL strips it too, but a tunnel target passes the query as given.
	return t.DialWS(ctx, raw, stripAPIKey(rawQuery), protocols)
}

func dialWS(ctx context.Context, t HTTPTarget, base *url.URL, raw, rawQuery string, protocols []string) (*websocket.Conn, error) {
	conn, resp, err := websocket.Dial(ctx, wsURL(base, raw, rawQuery), &websocket.DialOptions{
		HTTPClient:   &http.Client{Transport: transport(t.AllowPrivate)},
		HTTPHeader:   http.Header{"Authorization": []string{"Bearer " + t.APIKey}},
		Subprotocols: protocols,
	})
	if err != nil {
		if rerr := Refusal(resp); rerr != nil { // #3649: a key or Host refusal, not any 401/403
			return nil, rerr
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

// legacyOrigins are the self-hosted hub's default WebSocket origins besides the
// request's own host: none. Other ports of localhost are not this hub's page (#3511).
var legacyOrigins = []string{}

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
	up, err := t.DialWS(r.Context(), raw, stripAPIKey(r.URL.RawQuery), requestedProtocols(r))
	if err != nil {
		if token, busy := tooManyStreams(err); busy {
			// A browser can't read a refused handshake's status, so a busy machine's refusal
			// would only look like a dropped terminal that keeps reconnecting. The socket is
			// accepted and closed at once, with one close frame, StatusTooManyStreams and the
			// fixed text for the machine's token, which the terminal shows (#3663).
			if down, aerr := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: patterns}); aerr == nil {
				down.Close(StatusTooManyStreams, busyText(token))
			}
			return
		}
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
	// Each side is closed once (#3663, coral-hub): the pump that sees one side end closes the
	// other, and the second pump's exit, or a close the peer already sent, must not send another
	// close frame (Chrome: "Close received after close", and the code and reason are lost).
	upC, downC := &onceConn{WSConn: up}, &onceConn{WSConn: down}

	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	pol := PolicyFrom(r.Context())
	if pol.Closed != nil {
		go func() {
			defer safego.Recover("hubproxy/ws:174")
			select {
			case <-pol.Closed:
				// Access was withdrawn: end both sides with a code the UI can recognise.
				downC.Close(StatusAccessChanged, "access changed")
				upC.Close(websocket.StatusGoingAway, "")
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer safego.Recover("hubproxy/ws:187")
		defer wg.Done()
		pumpFrom(ctx, cancel, downC, upC, "remote connection lost", clientGate{pol.AllowClientFrame, pol.ReadOnlyNotice}, pol.Closed)
	}()
	go func() {
		defer safego.Recover("hubproxy/ws:191")
		defer wg.Done()
		pumpFrom(ctx, cancel, upC, downC, "browser connection lost", clientGate{}, pol.Closed)
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
				if oc, ok := src.(*onceConn); ok {
					oc.peerClosed() // the library answered src's close: it gets no other
				}
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
				dst.Close(code, closeReasonFor(code, reason))
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

// StatusTooManyStreams closes a browser's WebSocket that the machine refused because too many
// streams are open through Coral Hub (its limit, or the user's share of it; #3663). The reason
// says which, and the dashboard shows it and waits before trying again.
const StatusTooManyStreams = websocket.StatusCode(4429)

// tooManyStreams reports whether err is the machine's "too many streams" refusal, with its text.
func tooManyStreams(err error) (string, bool) {
	var se *tunnel.StreamError
	switch {
	case errors.As(err, &se) && se.Code == tunnel.CodeTooManyStreams:
		return se.Message, true
	case errors.Is(err, tunnel.ErrTooManyStreams):
		return "", true
	}
	return "", false
}

// onceConn sends at most one close frame on a connection: the first Close wins, and a close
// the peer sent (which the library already answered) counts as one.
type onceConn struct {
	WSConn
	once sync.Once
}

func (c *onceConn) Close(code websocket.StatusCode, reason string) error {
	var err error
	c.once.Do(func() { err = c.WSConn.Close(code, reason) })
	return err
}

func (c *onceConn) peerClosed() { c.once.Do(func() {}) }

// The fixed texts a busy refusal shows, by the machine's token (tunnel.BusyUser,
// tunnel.BusyMachine): no machine string reaches the browser on StatusTooManyStreams.
const (
	busyUserText    = "You have too many terminals open on this computer through Coral Hub. Close one, or try again shortly."
	busyMachineText = "This computer has too many terminals open through Coral Hub. Close one, or try again shortly."
)

// busyText is the fixed text for a machine's busy token (anything else: the machine is full).
func busyText(token string) string {
	if token == tunnel.BusyUser {
		return busyUserText
	}
	return busyMachineText
}

// closeReasonFor is a relayed close's reason: the fixed text on StatusTooManyStreams (keyed by
// the token in the reason), otherwise the reason normalised.
func closeReasonFor(code websocket.StatusCode, reason string) string {
	if code == StatusTooManyStreams {
		return busyText(reason)
	}
	return normalizeCloseReason(reason)
}

// normalizeCloseReason makes a relayed reason safe for a close frame: valid UTF-8 first (an
// invalid byte becomes U+FFFD), C0 and C1 controls become spaces, Unicode format characters
// (bidi overrides, zero-width characters, ...) and the line and paragraph separators are
// dropped (Security), then at most 123 bytes, cut on a rune boundary (#3663, coral-hub: a byte
// cut could split a character).
func normalizeCloseReason(reason string) string {
	reason = strings.Map(func(r rune) rune {
		switch {
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			return ' '
		case unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029:
			return -1
		}
		return r
	}, strings.ToValidUTF8(reason, "\uFFFD"))
	for len(reason) > 123 {
		_, size := utf8.DecodeLastRuneInString(reason)
		reason = reason[:len(reason)-size]
	}
	return reason
}
