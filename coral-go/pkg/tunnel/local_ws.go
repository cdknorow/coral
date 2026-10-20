// Forwarding a WebSocket and guarding an event stream's writes (moved out of local.go, #5091; no behaviour change).

package tunnel

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/safego"
	"github.com/coder/websocket"
)

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
	stampRole(hdr, h, http.MethodGet, dec)
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
		if status == http.StatusTooManyRequests {
			s.RejectBusy(CodeUpstream, "local websocket failed", status, resp.Header.Get("Retry-After"))
			return
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
		toHub = newWSRedact(ctx, down, l.redactor(), l.redactFor(viewerTier(h)), strings.HasPrefix(dec, "/ws/terminal/"))
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
	go func() {
		defer safego.Recover("tunnel/local:1080")
		defer wg.Done()
		pump(ctx, cancel, a, b, nil, toHub)
	}()
	go func() { defer safego.Recover("tunnel/local:1081"); defer wg.Done(); pump(ctx, cancel, b, a, gate, nil) }()
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
		if err := writeTimed(ctx, dst, typ, data); err != nil {
			return
		}
	}
}

// wsWriteTimeout bounds each frame written on a bridged WebSocket (#4161). A side that stops
// reading (a Coral Hub viewer whose tab or link stalled) would otherwise hold this bridge, its
// goroutines and its stream slot until the tunnel ends: nothing reads the other side's close
// while a write is blocked. After this long the bridge ends; the dashboard reconnects.
var wsWriteTimeout = 15 * time.Second

func writeTimed(ctx context.Context, dst wsEnd, typ websocket.MessageType, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	defer cancel()
	return dst.Write(ctx, typ, data)
}

// eventStreamWriteTimeout bounds one write of an event stream's reply to the hub (#4172). Other
// replies are ended by the BodyIdle timer when the hub stops taking them, but an event stream is
// silent between events by design and has no such timer, so a hub viewer that stopped reading
// would hold the write, the local connection, the handler and the stream slot until the tunnel
// ended. The time is spent blocked in a write only: silence between events is never counted.
var eventStreamWriteTimeout = 15 * time.Second

// guardEventStream wraps w, for an event stream only, so that a write blocked longer than
// eventStreamWriteTimeout ends the stream (as abort does for other replies): the write returns an
// error and the caller's deferred cleanup frees the slot.
func guardEventStream(s *Stream, header http.Header, w io.WriteCloser) io.WriteCloser {
	if !eventStream(header.Get("Content-Type")) {
		return w
	}
	return guardedWriter{w, s.abort}
}

type guardedWriter struct {
	io.WriteCloser
	abort func()
}

func (g guardedWriter) Write(p []byte) (int, error) {
	stop := time.AfterFunc(eventStreamWriteTimeout, g.abort)
	defer stop.Stop()
	return g.WriteCloser.Write(p)
}

// bodyAllowed: whether a response to method with status carries the body its
// Content-Length announces (not HEAD, 1xx, 204 or 304).
func bodyAllowed(method string, status int) bool {
	return method != http.MethodHead && status >= 200 && status != http.StatusNoContent && status != http.StatusNotModified
}
