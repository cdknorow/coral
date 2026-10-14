package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
	"nhooyr.io/websocket"
)

const (
	handshakeTimeout  = 10 * time.Second
	maxHandshakeBytes = 4 << 10
	maxTokenLen       = 512
)

// SessionConfig tunes a tunnel session. Zero fields take the defaults, which
// follow the spec: ping every 20s, dead after 60s.
type SessionConfig struct {
	MaxStreams     int           // concurrent streams per session, default 256
	WindowSize     uint32        // yamux per-stream window, default 256 KiB
	KeepAlive      time.Duration // ping interval, default 20s
	DeadAfter      time.Duration // declare the peer dead after this, default 60s
	StreamOpenWait time.Duration // default 10s
	Limits         Limits
}

func (c SessionConfig) withDefaults() SessionConfig {
	if c.MaxStreams <= 0 {
		c.MaxStreams = 256
	}
	if c.WindowSize == 0 {
		c.WindowSize = 256 << 10
	}
	if c.KeepAlive <= 0 {
		c.KeepAlive = 20 * time.Second
	}
	if c.DeadAfter <= c.KeepAlive {
		c.DeadAfter = 3 * c.KeepAlive
	}
	if c.StreamOpenWait <= 0 {
		c.StreamOpenWait = 10 * time.Second
	}
	c.Limits = c.Limits.withDefaults()
	return c
}

func (c SessionConfig) yamux() *yamux.Config {
	y := yamux.DefaultConfig()
	y.AcceptBacklog = 32
	y.EnableKeepAlive = true
	y.KeepAliveInterval = c.KeepAlive
	// A ping must be answered within the write timeout, so a silent peer is
	// declared dead after about KeepAlive + ConnectionWriteTimeout = DeadAfter.
	y.ConnectionWriteTimeout = c.DeadAfter - c.KeepAlive
	y.MaxStreamWindowSize = c.WindowSize
	y.StreamOpenTimeout = c.StreamOpenWait
	y.StreamCloseTimeout = 30 * time.Second
	y.LogOutput = io.Discard
	return y
}

// wsReadLimit bounds a transport message: a yamux frame never exceeds the
// stream window plus its header.
func (c SessionConfig) wsReadLimit() int64 { return int64(c.WindowSize) + 4096 }

// AuthFunc maps a server token to the identity it belongs to. Returning an
// error rejects the connection; the client only ever sees "unauthorized", except
// for ErrRetryLater which it sees as a temporary refusal.
type AuthFunc func(ctx context.Context, token string) (identity any, err error)

// Session is the hub's end of a tunnel.
type Session struct {
	ys        *yamux.Session
	cfg       SessionConfig
	cancel    context.CancelFunc
	identity  any
	version   int
	coralVer  string
	localMode Mode
	active    atomic.Int32
	closeOnce sync.Once
}

// Accept performs the server handshake on an upgraded WebSocket and returns a
// Session. Nothing but reading the first message happens before auth succeeds
// (the message is size and time limited).
func Accept(ctx context.Context, c *websocket.Conn, auth AuthFunc, cfg SessionConfig) (*Session, error) {
	cfg = cfg.withDefaults()
	c.SetReadLimit(maxHandshakeBytes)
	hctx, hcancel := context.WithTimeout(ctx, handshakeTimeout)
	defer hcancel()

	reject := func(code string, werr error) (*Session, error) {
		b, _ := json.Marshal(Welcome{Error: code})
		_ = c.Write(hctx, websocket.MessageBinary, b)
		_ = c.Close(websocket.StatusPolicyViolation, code)
		return nil, werr
	}
	typ, msg, err := c.Read(hctx)
	if err != nil {
		_ = c.Close(websocket.StatusPolicyViolation, CodeBadHandshake)
		return nil, ErrBadHandshake
	}
	var hello Hello
	if typ != websocket.MessageBinary || json.Unmarshal(msg, &hello) != nil || len(hello.ServerToken) == 0 || len(hello.ServerToken) > maxTokenLen || len(hello.CoralVersion) > 64 {
		return reject(CodeBadHandshake, ErrBadHandshake)
	}
	// Auth comes before anything else, including version negotiation, so an
	// unauthenticated peer learns nothing about the hub.
	identity, err := auth(hctx, hello.ServerToken)
	if errors.Is(err, ErrRetryLater) {
		return reject(CodeRetryLater, ErrRetryLater)
	}
	if err != nil {
		return reject(CodeUnauthorized, ErrUnauthorized)
	}
	v := hello.ProtocolVersion
	if v > ProtocolVersion {
		v = ProtocolVersion
	}
	if v < MinProtocolVersion {
		return reject(CodeUnsupportedVersion, ErrUnsupportedVersion)
	}
	w, _ := json.Marshal(Welcome{AcceptedVersion: v, MaxStreams: cfg.MaxStreams})
	if err := c.Write(hctx, websocket.MessageBinary, w); err != nil {
		_ = c.Close(websocket.StatusInternalError, "")
		return nil, err
	}

	c.SetReadLimit(cfg.wsReadLimit())
	sctx, cancel := context.WithCancel(context.Background())
	nc := websocket.NetConn(sctx, c, websocket.MessageBinary)
	ys, err := yamux.Server(nc, cfg.yamux())
	if err != nil {
		cancel()
		_ = nc.Close()
		return nil, err
	}
	s := &Session{ys: ys, cfg: cfg, cancel: cancel, identity: identity, version: v, coralVer: hello.CoralVersion, localMode: parseLocalMode(hello.LocalMode)}
	go s.rejectInbound()
	go func() { <-ys.CloseChan(); s.Close() }()
	return s, nil
}

// AcceptHTTP upgrades an HTTP request to a WebSocket and runs Accept. Browser
// requests (any Origin header) are refused: only a native connector may open
// a tunnel, so a malicious page cannot start one with ambient credentials.
func AcceptHTTP(w http.ResponseWriter, r *http.Request, auth AuthFunc, cfg SessionConfig) (*Session, error) {
	if r.Header.Get("Origin") != "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, ErrUnauthorized
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		return nil, err
	}
	return Accept(r.Context(), c, auth, cfg)
}

// rejectInbound closes any stream the connector opens: only the hub opens
// streams.
func (s *Session) rejectInbound() {
	for {
		st, err := s.ys.AcceptStream()
		if err != nil {
			return
		}
		_ = st.Close()
	}
}

// Identity is the value returned by the AuthFunc.
func (s *Session) Identity() any { return s.identity }

// Version is the negotiated protocol version.
func (s *Session) Version() int { return s.version }

// LocalMode is the maximum access mode the connector reported. It is only a display
// hint: an unknown or missing value is view, and the hub must never treat it as
// permission (the connector enforces its own limit).
func (s *Session) LocalMode() Mode { return s.localMode }

func parseLocalMode(v string) Mode {
	if m, ok := ParseMode(v); ok {
		return m
	}
	return TierView
}

// CoralVersion is what the connector reported in its Hello.
func (s *Session) CoralVersion() string { return s.coralVer }

// NumStreams is the number of streams currently open.
func (s *Session) NumStreams() int { return int(s.active.Load()) }

// Done is closed when the session ends.
func (s *Session) Done() <-chan struct{} { return s.ys.CloseChan() }

// Close ends the session and every stream on it.
func (s *Session) Close() error {
	var err error
	s.closeOnce.Do(func() {
		err = s.ys.Close()
		s.cancel()
	})
	return err
}

// HubStream is a stream opened by the hub. Read and Write move raw stream
// bytes; Close releases it (and, before the response has completed, tells the
// connector to cancel).
type HubStream struct {
	s    *Session
	st   *yamux.Stream
	once sync.Once
}

func (h *HubStream) Read(p []byte) (int, error)  { return h.st.Read(p) }
func (h *HubStream) Write(p []byte) (int, error) { return h.st.Write(p) }
func (h *HubStream) SetDeadline(t time.Time) error {
	return h.st.SetDeadline(t)
}

// Close is idempotent.
func (h *HubStream) Close() error {
	var err error
	h.once.Do(func() {
		err = h.st.Close()
		h.s.active.Add(-1)
	})
	return err
}

// OpenStream opens a stream toward the connector and sends hdr. It fails with
// ErrTooManyStreams at the per-session cap instead of queueing.
func (s *Session) OpenStream(ctx context.Context, hdr Header) (*HubStream, error) {
	if err := hdr.validate(s.cfg.Limits); err != nil {
		return nil, err
	}
	if int(s.active.Add(1)) > s.cfg.MaxStreams {
		s.active.Add(-1)
		return nil, ErrTooManyStreams
	}
	if err := ctx.Err(); err != nil {
		s.active.Add(-1)
		return nil, err
	}
	st, err := s.ys.OpenStream()
	if err != nil {
		s.active.Add(-1)
		if s.ys.IsClosed() {
			return nil, ErrSessionClosed
		}
		return nil, err
	}
	hs := &HubStream{s: s, st: st}
	stop := context.AfterFunc(ctx, func() { _ = hs.Close() })
	defer stop()
	if err := writeFrame(st, hdr); err != nil {
		_ = hs.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return hs, nil
}

func (s *Session) readResponse(hs *HubStream) (ResponseHeader, error) {
	var rh ResponseHeader
	b, err := readFrame(hs.st, s.cfg.Limits.MaxHeaderBytes)
	if err != nil {
		return rh, err
	}
	if err := json.Unmarshal(b, &rh); err != nil {
		return rh, ErrBadHeader
	}
	if err := validHeaders(rh.Headers, s.cfg.Limits); err != nil {
		return rh, err
	}
	// The connector is not trusted: its header names arrive as written. Canonical keys
	// let the hub's filters (h.Del, h.Get) see every header; a lowercase "set-cookie"
	// would otherwise pass them and be written as Set-Cookie by the reverse proxy (#3410).
	rh.Headers = CanonicalHeader(rh.Headers)
	if rh.Error != "" {
		return rh, &StreamError{Code: rh.ErrorCode, Message: rh.Error, Status: rh.Status}
	}
	return rh, nil
}

// RoundTrip sends req through the tunnel as an HTTP stream. req.URL's escaped
// path and raw query are sent as is; the connector decides scheme, host and
// credentials. The response body streams. Session implements http.RoundTripper.
func (s *Session) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	hdr := Header{
		Kind: KindHTTP, Method: req.Method, Path: req.URL.EscapedPath(), Query: req.URL.RawQuery,
		Headers: req.Header.Clone(), ContentLength: req.ContentLength,
	}
	hs, err := s.OpenStream(ctx, hdr)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = hs.Close() })
	werr := make(chan error, 1)
	go func() {
		cw := &chunkWriter{w: hs}
		if req.Body != nil {
			if _, err := io.Copy(cw, req.Body); err != nil {
				werr <- err
				return
			}
			req.Body.Close()
		}
		werr <- cw.Close()
	}()
	rh, err := s.readResponse(hs)
	if err != nil {
		stop()
		_ = hs.Close()
		select {
		case e := <-werr:
			_ = e
		default:
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if rh.Status < 100 || rh.Status > 999 {
		stop()
		_ = hs.Close()
		return nil, ErrBadHeader
	}
	body := &hubBody{r: &chunkReader{r: hs.st}, hs: hs, stop: stop}
	return &http.Response{
		Status:        http.StatusText(rh.Status),
		StatusCode:    rh.Status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        rh.Headers,
		Body:          body,
		ContentLength: -1,
		Request:       req,
	}, nil
}

type hubBody struct {
	r    io.Reader
	hs   *HubStream
	stop func() bool
}

func (b *hubBody) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *hubBody) Close() error {
	b.stop()
	return b.hs.Close()
}

// DialWS opens a WebSocket to the connector's local API. path is an escaped
// path under /ws/ or /api/. A failed handshake returns *StreamError (with the
// upstream status in Status when there was one).
func (s *Session) DialWS(ctx context.Context, path, rawQuery string, protocols []string) (*WSConn, error) {
	hs, err := s.OpenStream(ctx, Header{Kind: KindWS, Method: http.MethodGet, Path: path, Query: rawQuery, Protocols: protocols})
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = hs.Close() })
	rh, err := s.readResponse(hs)
	stop()
	if err != nil {
		_ = hs.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var se *StreamError
		if !errors.As(err, &se) {
			return nil, err
		}
		return nil, err
	}
	return newWSConn(hs, rh.Subprotocol, nil), nil
}
