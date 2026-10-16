package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/cdknorow/coral/internal/safego"
)

// Connector-opened streams: every stream is opened by the hub except these, one request each,
// of a kind the hub advertised in Welcome (relay.go #3489, signin.go #3966). The connector
// never opens a kind the hub didn't advertise; the hub closes anything else unanswered.
//
// Wire, on a connector-opened yamux stream:
//
//	connector -> hub   frame: Header{Kind, Method: "POST", Path, ContentLength: n}
//	connector -> hub   frame: the JSON body, n bytes (n <= the path's cap)
//	hub -> connector   frame: ResponseHeader{Status, Headers (only Retry-After)}
//	hub -> connector   frame: the JSON response body (at most the kind's reply cap)
//
// One request per stream, then both sides close it.

// inboundKind is one kind of connector-opened stream.
type inboundKind struct {
	kind       Kind
	capability string
	pathCap    func(path string) int // the request body cap for a path, 0 if it isn't one
	maxReply   int                   // the largest reply body
	timeout    time.Duration         // one whole request, on the connector and in the hub's hook
	busy       []byte                // the fixed reply body with 429
	failed     []byte                // the fixed reply body with 502 (a panic, an oversized reply)
}

// inboundKinds are every kind this package knows, in the order the hub checks them.
var inboundKinds = []*inboundKind{relayKind, signinKind}

// inboundReadDeadline bounds how long the hub waits for a request's header and body
// (Security: enforced before any lookup).
const inboundReadDeadline = 5 * time.Second

// inboundHook is the hub's hook for a kind, or nil when the hub doesn't serve it.
type inboundHook func(ctx context.Context, s *Session, path string, body []byte) RelayResponse

func (c SessionConfig) inboundHook(k *inboundKind) inboundHook {
	switch k.kind {
	case KindRelay:
		if c.OnRelay != nil {
			return func(ctx context.Context, s *Session, path string, body []byte) RelayResponse {
				return c.OnRelay(ctx, s, RelayRequest{Path: path, Body: body})
			}
		}
	case KindSignin:
		if c.OnSignin != nil {
			return func(ctx context.Context, s *Session, path string, body []byte) RelayResponse {
				return RelayResponse(c.OnSignin(ctx, s, SigninRequest{Op: signinOp(path), Body: body}))
			}
		}
	}
	return nil
}

// advertisedHooks keeps, of the capabilities the hub chose, those this package knows and the
// hub serves, and returns them with their hooks.
func (c SessionConfig) advertisedHooks(chosen []string) ([]string, map[Kind]inboundHook) {
	var caps []string
	hooks := map[Kind]inboundHook{}
	for _, name := range chosen {
		for _, k := range inboundKinds {
			if name != k.capability || hooks[k.kind] != nil {
				continue
			}
			if h := c.inboundHook(k); h != nil {
				caps = append(caps, name)
				hooks[k.kind] = h
			}
		}
	}
	return caps, hooks
}

func inboundKindFor(kind Kind) *inboundKind {
	for _, k := range inboundKinds {
		if k.kind == kind {
			return k
		}
	}
	return nil
}

// validInboundHeader reports whether h is exactly a request of kind k: POST, one of its paths,
// nothing else, and a body length within the path's cap.
func validInboundHeader(k *inboundKind, h Header) bool {
	max := k.pathCap(h.Path)
	return h.Kind == k.kind && h.Method == http.MethodPost && max > 0 &&
		h.Query == "" && len(h.Headers) == 0 && len(h.Protocols) == 0 && h.HubUser == "" &&
		h.ContentLength >= 0 && h.ContentLength <= int64(max)
}

// serveInbound handles one connector-opened stream on the hub: a valid request of an
// advertised kind goes to its hook, anything else is closed unanswered. The header, the body
// cap and the read deadline are checked before the hook runs.
func (s *Session) serveInbound(st io.ReadWriteCloser, setDeadline func(time.Time) error) {
	defer st.Close()
	var k *inboundKind
	// A panic in the hook must not take the hub down: a fixed line (never the request), 502
	// with the kind's fixed body, and the stream closes.
	defer func() {
		if p := recover(); p != nil {
			slog.Error("tunnel: connector-request handler panicked")
			if k != nil {
				_ = setDeadline(time.Now().Add(inboundReadDeadline))
				writeInboundResponse(st, RelayResponse{Status: http.StatusBadGateway, Body: k.failed})
			}
		}
	}()
	_ = setDeadline(time.Now().Add(inboundReadDeadline))
	b, err := readFrame(st, 1<<10)
	if err != nil {
		return
	}
	// Exactly the header: an unknown key is refused, not ignored.
	var h Header
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if dec.Decode(&h) != nil || dec.Decode(new(json.RawMessage)) != io.EOF {
		return
	}
	hook := s.hooks[h.Kind]
	if k = inboundKindFor(h.Kind); k == nil || hook == nil || !validInboundHeader(k, h) {
		k = nil
		return
	}
	body, err := readFrameGrowing(st, k.pathCap(h.Path))
	if err != nil || int64(len(body)) != h.ContentLength {
		return
	}
	if int(s.inflight.Add(1)) > s.cfg.MaxRelayInFlight {
		s.inflight.Add(-1)
		_ = setDeadline(time.Now().Add(inboundReadDeadline))
		writeInboundResponse(st, RelayResponse{Status: http.StatusTooManyRequests, Body: k.busy, RetryAfter: 1})
		return
	}
	defer s.inflight.Add(-1)
	_ = setDeadline(time.Time{})
	ctx, cancel := context.WithTimeout(context.Background(), k.timeout)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = st.Close() })
	defer stop()
	go func() {
		defer safego.Recover("tunnel/inbound:155")
		select {
		case <-s.ys.CloseChan():
			cancel()
		case <-ctx.Done():
		}
	}()
	resp := hook(ctx, s, h.Path, body)
	if len(resp.Body) > k.maxReply {
		resp = RelayResponse{Status: http.StatusBadGateway, Body: k.failed}
	}
	_ = setDeadline(time.Now().Add(inboundReadDeadline))
	writeInboundResponse(st, resp)
}

func writeInboundResponse(w io.Writer, r RelayResponse) {
	rh := ResponseHeader{Status: r.Status}
	if r.RetryAfter > 0 {
		rh.Headers = http.Header{"Retry-After": {strconv.Itoa(r.RetryAfter)}}
	}
	if writeFrame(w, rh) != nil {
		return
	}
	body := r.Body
	if body == nil {
		body = []byte("{}")
	}
	_ = writeRawFrame(w, body)
}

// readFrameGrowing reads one length-prefixed frame of at most max bytes, allocating only
// as bytes actually arrive (readFrame allocates the declared length up front).
func readFrameGrowing(r io.Reader, max int) ([]byte, error) {
	var lb [4]byte
	if _, err := io.ReadFull(r, lb[:]); err != nil {
		return nil, err
	}
	n := int64(lb[0])<<24 | int64(lb[1])<<16 | int64(lb[2])<<8 | int64(lb[3])
	if n > int64(max) {
		return nil, ErrHeaderTooLarge
	}
	var buf bytes.Buffer
	if _, err := io.CopyN(&buf, r, n); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeRawFrame writes b as one length-prefixed frame, as is (writeFrame JSON-encodes).
func writeRawFrame(w io.Writer, b []byte) error {
	buf := make([]byte, 4+len(b))
	buf[0], buf[1], buf[2], buf[3] = byte(len(b)>>24), byte(len(b)>>16), byte(len(b)>>8), byte(len(b))
	copy(buf[4:], b)
	_, err := w.Write(buf)
	return err
}

// advertised reports whether the hub advertised capability for this tunnel.
func (c *ClientSession) advertised(capability string) bool {
	for _, cap := range c.capabilities {
		if cap == capability {
			return true
		}
	}
	return false
}

// callInbound sends one request of kind k through the tunnel and returns the hub's answer.
// unsupported is returned when the hub didn't advertise the kind or closed the stream
// without answering.
func (c *ClientSession) callInbound(ctx context.Context, k *inboundKind, path string, body []byte, unsupported error) (RelayResponse, error) {
	max := k.pathCap(path)
	if max == 0 {
		return RelayResponse{}, fmt.Errorf("tunnel: %q is not a %s path", path, k.kind)
	}
	if len(body) > max {
		return RelayResponse{}, fmt.Errorf("tunnel: %s request over %d bytes", k.kind, max)
	}
	if !c.advertised(k.capability) {
		return RelayResponse{}, unsupported
	}
	ctx, cancel := context.WithTimeout(ctx, k.timeout)
	defer cancel()
	st, err := c.ys.OpenStream()
	if err != nil {
		return RelayResponse{}, err
	}
	defer st.Close()
	stop := context.AfterFunc(ctx, func() { _ = st.Close() })
	defer stop()
	fail := func(err error) (RelayResponse, error) { return RelayResponse{}, c.inboundErr(ctx, err, unsupported) }
	if err := writeFrame(st, Header{Kind: k.kind, Method: http.MethodPost, Path: path, ContentLength: int64(len(body))}); err != nil {
		return fail(err)
	}
	if err := writeRawFrame(st, body); err != nil {
		return fail(err)
	}
	b, err := readFrame(st, c.cfg.Limits.MaxHeaderBytes)
	if err != nil {
		return fail(err)
	}
	var rh ResponseHeader
	if json.Unmarshal(b, &rh) != nil || rh.Status < 200 || rh.Status > 599 {
		return RelayResponse{}, ErrBadHeader
	}
	respBody, err := readFrame(st, k.maxReply)
	if err != nil {
		return fail(err)
	}
	out := RelayResponse{Status: rh.Status, Body: respBody}
	if v := http.Header(rh.Headers).Get("Retry-After"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 3600 {
			out.RetryAfter = n
		}
	}
	return out, nil
}

func (c *ClientSession) inboundErr(ctx context.Context, err, unsupported error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if c.ys.IsClosed() {
		return errInboundDisconnected
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		// The hub closed the stream without an answer: it doesn't serve this kind for us.
		return unsupported
	}
	return err
}

// errInboundDisconnected: the tunnel itself went away during the request (not a refusal).
var errInboundDisconnected = fmt.Errorf("tunnel: disconnected during the request: %w", ErrSessionClosed)
