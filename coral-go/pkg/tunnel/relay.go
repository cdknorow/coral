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
)

// Relay (#3489, coral-hub PR #260): a Coral may ask Coral Hub to deliver a cross-team
// message to another Coral of the same account, and to list the targets its owner linked
// it to. It is the one kind of stream the CONNECTOR opens; every other stream is opened by
// the hub. The hub advertises the "relay" capability in Welcome only for a tunnel it will
// take relays from (its account has the relay on and the server has a link); a connector
// never opens one otherwise, and a hub without an OnRelay hook closes it at once.
//
// Wire, on a connector-opened yamux stream:
//
//	connector -> hub   frame: Header{Kind: "relay", Method: "POST", Path: RelayMessagePath or RelayTargetsPath, ContentLength: n}
//	connector -> hub   frame: the JSON body, n bytes (n <= the path's cap)
//	hub -> connector   frame: ResponseHeader{Status, Headers (only Retry-After)}
//	hub -> connector   frame: the JSON response body (at most RelayMaxResponse)
//
// One request per stream, then both sides close it.

// CapabilityRelay is the capability a hub advertises (Welcome.Capabilities) when it takes
// relay streams from this tunnel.
const CapabilityRelay = "relay"

// KindRelay is the stream kind of a connector-opened relay request.
const KindRelay Kind = "relay"

// The only relay paths, and their request body caps.
const (
	RelayMessagePath = "/relay/v1/message"
	RelayTargetsPath = "/relay/v1/targets"

	RelayMaxMessage  = 64 << 10 // the whole message request
	RelayMaxTargets  = 1 << 10  // {"from_board": "..."}
	RelayMaxResponse = 64 << 10 // a response body (a target list is at most 50 entries)
)

// relayCap returns the request body cap for a relay path, or 0 if the path isn't one.
func relayCap(path string) int {
	switch path {
	case RelayMessagePath:
		return RelayMaxMessage
	case RelayTargetsPath:
		return RelayMaxTargets
	}
	return 0
}

// relayTimeout bounds a whole relay request on the connector (the hub waits up to 20 s
// for the target Coral).
const relayTimeout = 30 * time.Second

// relayReadDeadline bounds how long the hub waits for a relay request's header and body
// (Security: enforced before any lookup).
const relayReadDeadline = 5 * time.Second

// ErrRelayUnsupported: the hub didn't advertise the relay for this tunnel (an older hub,
// the relay off for the account, or no link for this server).
var ErrRelayUnsupported = errors.New("tunnel: Coral Hub doesn't relay messages for this server")

// RelayRequest is what the hub's OnRelay hook gets: the path (one of the two relay paths)
// and the body, already within its cap. The source server is the Session's identity,
// never anything in the request.
type RelayRequest struct {
	Path string
	Body []byte
}

// RelayResponse is the hub's answer. RetryAfter (seconds, > 0) is sent as Retry-After.
type RelayResponse struct {
	Status     int
	Body       []byte
	RetryAfter int
}

// RelayFunc handles one relay request on the hub. s is the tunnel it came from (its
// Identity is the source server). The "relay" capability is fixed at the handshake, so
// it is no permission: OnRelay must re-check the account, the link and the target's mode
// on EVERY call (a link revoked or a mode lowered since the tunnel connected applies at
// once).
type RelayFunc func(ctx context.Context, s *Session, req RelayRequest) RelayResponse

// validRelayHeader reports whether h is exactly a relay request header: kind relay, POST,
// one of the two paths, nothing else, and a body length within the path's cap.
func validRelayHeader(h Header) bool {
	max := relayCap(h.Path)
	return h.Kind == KindRelay && h.Method == http.MethodPost && max > 0 &&
		h.Query == "" && len(h.Headers) == 0 && len(h.Protocols) == 0 &&
		h.ContentLength >= 0 && h.ContentLength <= int64(max)
}

// serveRelay handles one connector-opened stream on the hub: a valid relay request goes to
// OnRelay, anything else is closed unanswered. The header, the body cap and the read
// deadline are checked before the hook runs.
func (s *Session) serveRelay(st io.ReadWriteCloser, setDeadline func(time.Time) error) {
	defer st.Close()
	// A panic in the hook must not take the hub down: a fixed line (never the request),
	// 502 target_error, and the stream closes.
	defer func() {
		if p := recover(); p != nil {
			slog.Error("tunnel: relay handler panicked")
			_ = setDeadline(time.Now().Add(relayReadDeadline))
			writeRelayResponse(st, RelayResponse{Status: http.StatusBadGateway, Body: []byte(`{"ok":false,"error":"target_error"}`)})
		}
	}()
	_ = setDeadline(time.Now().Add(relayReadDeadline))
	b, err := readFrame(st, 1<<10)
	if err != nil {
		return
	}
	// Exactly the header: an unknown key is refused, not ignored.
	var h Header
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if dec.Decode(&h) != nil || dec.More() || !validRelayHeader(h) {
		return
	}
	body, err := readFrameGrowing(st, relayCap(h.Path))
	if err != nil || int64(len(body)) != h.ContentLength {
		return
	}
	if int(s.relays.Add(1)) > s.cfg.MaxRelayInFlight {
		s.relays.Add(-1)
		_ = setDeadline(time.Now().Add(relayReadDeadline))
		writeRelayResponse(st, RelayResponse{Status: http.StatusTooManyRequests, Body: []byte(`{"ok":false,"error":"rate_limited"}`), RetryAfter: 1})
		return
	}
	defer s.relays.Add(-1)
	_ = setDeadline(time.Time{})
	ctx, cancel := context.WithTimeout(context.Background(), relayTimeout)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = st.Close() })
	defer stop()
	go func() {
		select {
		case <-s.ys.CloseChan():
			cancel()
		case <-ctx.Done():
		}
	}()
	resp := s.cfg.OnRelay(ctx, s, RelayRequest{Path: h.Path, Body: body})
	if len(resp.Body) > RelayMaxResponse {
		resp = RelayResponse{Status: http.StatusBadGateway, Body: []byte(`{"ok":false,"error":"target_error"}`)}
	}
	_ = setDeadline(time.Now().Add(relayReadDeadline))
	writeRelayResponse(st, resp)
}

func writeRelayResponse(w io.Writer, r RelayResponse) {
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

// CanRelay reports whether the hub advertised the relay for this tunnel.
func (c *ClientSession) CanRelay() bool {
	for _, cap := range c.capabilities {
		if cap == CapabilityRelay {
			return true
		}
	}
	return false
}

// Relay sends one relay request (RelayMessagePath or RelayTargetsPath) through the tunnel
// and returns the hub's answer: its status, its JSON body and Retry-After (seconds, 0 if
// none). It fails with ErrRelayUnsupported when the hub didn't advertise the relay.
func (c *ClientSession) Relay(ctx context.Context, path string, body []byte) (RelayResponse, error) {
	max := relayCap(path)
	if max == 0 {
		return RelayResponse{}, fmt.Errorf("tunnel: %q is not a relay path", path)
	}
	if len(body) > max {
		return RelayResponse{}, fmt.Errorf("tunnel: relay request over %d bytes", max)
	}
	if !c.CanRelay() {
		return RelayResponse{}, ErrRelayUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, relayTimeout)
	defer cancel()
	st, err := c.ys.OpenStream()
	if err != nil {
		return RelayResponse{}, err
	}
	defer st.Close()
	stop := context.AfterFunc(ctx, func() { _ = st.Close() })
	defer stop()
	if err := writeFrame(st, Header{Kind: KindRelay, Method: http.MethodPost, Path: path, ContentLength: int64(len(body))}); err != nil {
		return RelayResponse{}, c.relayErr(ctx, err)
	}
	if err := writeRawFrame(st, body); err != nil {
		return RelayResponse{}, c.relayErr(ctx, err)
	}
	b, err := readFrame(st, c.cfg.Limits.MaxHeaderBytes)
	if err != nil {
		return RelayResponse{}, c.relayErr(ctx, err)
	}
	var rh ResponseHeader
	if json.Unmarshal(b, &rh) != nil || rh.Status < 100 || rh.Status > 599 {
		return RelayResponse{}, ErrBadHeader
	}
	respBody, err := readFrame(st, RelayMaxResponse)
	if err != nil {
		return RelayResponse{}, c.relayErr(ctx, err)
	}
	out := RelayResponse{Status: rh.Status, Body: respBody}
	if v := http.Header(rh.Headers).Get("Retry-After"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 3600 {
			out.RetryAfter = n
		}
	}
	return out, nil
}

func (c *ClientSession) relayErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if c.ys.IsClosed() {
		return errRelayDisconnected
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		// The hub closed the stream without an answer: it doesn't take relays from us.
		return ErrRelayUnsupported
	}
	return err
}

// errRelayDisconnected: the tunnel itself went away during the request (not a refusal).
var errRelayDisconnected = fmt.Errorf("tunnel: disconnected during the relay request: %w", ErrSessionClosed)
