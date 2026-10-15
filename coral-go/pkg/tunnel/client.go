package tunnel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hashicorp/yamux"
	"nhooyr.io/websocket"
)

// Handler executes streams opened by the hub. It must finish every stream it
// is given by responding (Respond, AcceptWS or Reject); the framework closes
// the stream when ServeStream returns.
type Handler interface {
	ServeStream(ctx context.Context, s *Stream)
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, s *Stream)

func (f HandlerFunc) ServeStream(ctx context.Context, s *Stream) { f(ctx, s) }

// Backoff controls reconnects: delay doubles from Min up to Max with ±25%
// jitter. Defaults 1s and 60s.
type Backoff struct{ Min, Max time.Duration }

func (b Backoff) delay(attempt int) time.Duration {
	if b.Min <= 0 {
		b.Min = time.Second
	}
	if b.Max <= 0 {
		b.Max = 60 * time.Second
	}
	d := b.Min
	for i := 0; i < attempt && d < b.Max; i++ {
		d *= 2
	}
	if d > b.Max {
		d = b.Max
	}
	return time.Duration(float64(d) * (0.75 + rand.Float64()*0.5))
}

// ClientOptions configures the connector side.
type ClientOptions struct {
	SessionConfig
	Handler      Handler
	CoralVersion string
	// AllowInsecure permits ws:// URLs to loopback hosts (127.0.0.0/8, ::1,
	// localhost) only. For tests and local development; production connectors
	// must use wss:// and must never expose this as a flag or environment
	// setting. A cleartext URL to any other host is refused even when set.
	AllowInsecure bool
	// HTTPClient is used for the WebSocket handshake (custom TLS roots, tests).
	// Redirects are never followed whatever its CheckRedirect says.
	HTTPClient *http.Client
	Backoff    Backoff
	// LocalMode reports the connector's maximum access mode in the handshake. It is
	// read on every (re)connect. Nil reports view.
	LocalMode func() Mode
	// OnState is called from Run when the tunnel connects (nil error) or a
	// connection attempt or session ends (with the reason).
	OnState func(connected bool, err error)
	// OnSession is called with the live session after each connect, and with nil when it
	// ends, so the owner can send relay requests over it (#3489). Optional.
	OnSession func(*ClientSession)
	Logger    *slog.Logger

	uplink uplinkConfig // the uplink scheduler's tuning (tests)
}

// ClientSession is the connector's end of one established tunnel.
type ClientSession struct {
	ys         *yamux.Session
	cfg        SessionConfig
	h          Handler
	cancel     context.CancelFunc
	version    int
	maxStreams int
	active     atomic.Int32
	log        *slog.Logger
	closeOnce  sync.Once
	started    time.Time
	up         *uplink // nil: writes go straight to yamux
	// capabilities the hub advertised in Welcome (version 2), e.g. "relay" (#3489).
	capabilities []string
}

func isLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// noRedirectClient returns a copy of c (or a fresh client) that refuses to
// follow redirects: the token travels in the first frame to wherever the
// handshake lands, so a redirecting hub must not be able to move it.
func noRedirectClient(c *http.Client) *http.Client {
	var out http.Client
	if c != nil {
		out = *c
	}
	out.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &out
}

// lowLatencyClient makes the tunnel's own connection keep a short kernel send queue
// (TCP_NOTSENT_LOWAT, #3440 v2), whoever built the HTTP client: the uplink scheduler's
// order is lost if the OS already holds hundreds of KiB ahead of a small reply. A
// client whose transport is an *http.Transport (or none) gets a copy whose dialer sets
// it on every connection it opens; a transport wrapped in another RoundTripper must
// set it in its own dialer (Coral's NewHubHTTPClient does).
func lowLatencyClient(c *http.Client) *http.Client {
	tr := c.Transport
	if tr == nil {
		tr = http.DefaultTransport
	}
	t, ok := tr.(*http.Transport)
	if !ok {
		return c
	}
	t = t.Clone()
	dial := t.DialContext
	if dial == nil {
		dial = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		if sc, ok := conn.(syscall.Conn); ok {
			if raw, err := sc.SyscallConn(); err == nil {
				_ = LowLatencyControl(network, addr, raw)
			}
		}
		if onTunnelConn != nil {
			onTunnelConn(conn)
		}
		return conn, nil
	}
	out := *c
	out.Transport = t
	return &out
}

// Diagnostic switches (#3440 v3), for comparing the layers on a real link:
// CORAL_TUNNEL_SCHEDULER=off sends writes straight to yamux as before #3440, and
// CORAL_TUNNEL_LOWAT=off leaves the socket's send queue at the OS default.
const (
	schedulerEnv = "CORAL_TUNNEL_SCHEDULER"
	lowWaterEnv  = "CORAL_TUNNEL_LOWAT"
)

func envOff(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "off", "0", "false", "no":
		return true
	}
	return false
}

// onTunnelConn, when set (tests only), sees each connection the tunnel dials.
var onTunnelConn func(net.Conn)

func checkURL(raw string, insecure bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("tunnel: invalid hub url")
	}
	switch u.Scheme {
	case "wss", "https":
	case "ws", "http":
		if !insecure {
			return fmt.Errorf("tunnel: hub url must be wss://")
		}
		if !isLoopbackHost(u.Hostname()) {
			return fmt.Errorf("tunnel: cleartext hub url is only allowed for loopback hosts")
		}
	default:
		return fmt.Errorf("tunnel: unsupported hub url scheme %q", u.Scheme)
	}
	return nil
}

// Dial connects, authenticates and negotiates a version. Streams the hub opens
// are served by o.Handler until the session ends.
func Dial(ctx context.Context, rawURL, token string, o ClientOptions) (*ClientSession, error) {
	if o.Handler == nil {
		return nil, errors.New("tunnel: nil Handler")
	}
	if err := checkURL(rawURL, o.AllowInsecure); err != nil {
		return nil, err
	}
	cfg := o.SessionConfig.withDefaults()
	hctx, hcancel := context.WithTimeout(ctx, handshakeTimeout)
	defer hcancel()
	c, _, err := websocket.Dial(hctx, rawURL, &websocket.DialOptions{HTTPClient: lowLatencyClient(noRedirectClient(o.HTTPClient))})
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(maxHandshakeBytes)
	hello, _ := json.Marshal(Hello{ProtocolVersion: ProtocolVersion, ServerToken: token, CoralVersion: o.CoralVersion, LocalMode: currentMode(o.LocalMode).String()})
	if err := c.Write(hctx, websocket.MessageBinary, hello); err != nil {
		_ = c.Close(websocket.StatusInternalError, "")
		return nil, err
	}
	_, msg, err := c.Read(hctx)
	if err != nil {
		_ = c.Close(websocket.StatusInternalError, "")
		return nil, fmt.Errorf("tunnel: handshake: %w", err)
	}
	var w Welcome
	if json.Unmarshal(msg, &w) != nil {
		_ = c.Close(websocket.StatusProtocolError, "")
		return nil, ErrBadHandshake
	}
	if w.Error != "" {
		// Answer the server's close frame so it does not hold the socket open
		// waiting for us.
		_ = c.Close(websocket.StatusNormalClosure, "")
	}
	switch {
	case w.Error == CodeUnauthorized:
		return nil, ErrUnauthorized
	case w.Error == CodeUnsupportedVersion:
		return nil, ErrUnsupportedVersion
	case w.Error == CodeRetryLater:
		return nil, ErrRetryLater
	case w.Error != "":
		return nil, fmt.Errorf("%w: %s", ErrBadHandshake, w.Error)
	case w.AcceptedVersion < MinProtocolVersion || w.AcceptedVersion > ProtocolVersion:
		_ = c.Close(websocket.StatusProtocolError, "")
		return nil, ErrUnsupportedVersion
	}
	if w.MaxStreams > 0 && cfg.MaxStreams > w.MaxStreams {
		cfg.MaxStreams = w.MaxStreams
	}

	c.SetReadLimit(cfg.wsReadLimit())
	sctx, cancel := context.WithCancel(context.Background())
	nc := websocket.NetConn(sctx, c, websocket.MessageBinary)
	ys, err := yamux.Client(nc, cfg.yamux())
	if err != nil {
		cancel()
		_ = nc.Close()
		return nil, err
	}
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	cs := &ClientSession{ys: ys, cfg: cfg, h: o.Handler, cancel: cancel, version: w.AcceptedVersion, maxStreams: cfg.MaxStreams, log: log, started: time.Now(), capabilities: w.Capabilities}
	if !o.uplink.Off && !envOff(schedulerEnv) {
		cs.up = newUplink(o.uplink)
	}
	go cs.acceptLoop()
	go func() { <-ys.CloseChan(); cs.Close() }()
	return cs, nil
}

// Version is the negotiated protocol version.
func (c *ClientSession) Version() int { return c.version }

// Done is closed when the session ends.
func (c *ClientSession) Done() <-chan struct{} { return c.ys.CloseChan() }

// Close ends the session.
func (c *ClientSession) Close() error {
	var err error
	c.closeOnce.Do(func() {
		err = c.ys.Close()
		c.cancel()
	})
	return err
}

func (c *ClientSession) acceptLoop() {
	for {
		st, err := c.ys.AcceptStream()
		if err != nil {
			return
		}
		if int(c.active.Add(1)) > c.maxStreams {
			c.active.Add(-1)
			go func() {
				_ = writeFrame(st, ResponseHeader{Error: "too many streams", ErrorCode: CodeTooManyStreams})
				_ = st.Close()
			}()
			continue
		}
		go func() {
			defer c.active.Add(-1)
			c.serve(st)
		}()
	}
}

func (c *ClientSession) serve(st *yamux.Stream) {
	defer st.Close()
	_ = st.SetReadDeadline(time.Now().Add(handshakeTimeout))
	b, err := readFrame(st, c.cfg.Limits.MaxHeaderBytes)
	if err == nil {
		_ = st.SetReadDeadline(time.Time{})
	}
	var hdr Header
	if err == nil {
		hdr, err = decodeHeader(b, c.cfg.Limits)
	}
	if err != nil {
		_ = writeFrame(st, ResponseHeader{Error: "bad request", ErrorCode: CodeBadRequest})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-c.ys.CloseChan():
			cancel()
		case <-ctx.Done():
		}
	}()
	s := &Stream{Header: hdr, st: st, cancel: cancel, ctx: ctx, limits: c.cfg.Limits,
		w: &upWriter{st: st, u: c.up, lane: laneFor(hdr), done: ctx.Done()}}
	defer func() {
		if r := recover(); r != nil {
			c.log.Error("tunnel: handler panic", "panic", r)
		}
	}()
	c.h.ServeStream(ctx, s)
}

// Stream is one hub-initiated stream as seen by a Handler.
type Stream struct {
	Header Header

	st     *yamux.Stream
	w      io.Writer // st's writes, through the uplink scheduler
	ctx    context.Context
	cancel context.CancelFunc
	limits Limits

	watchOnce sync.Once
	mu        sync.Mutex
	responded bool
}

// Context is cancelled when the hub aborts the stream or the session ends.
func (s *Stream) Context() context.Context { return s.ctx }

// RequestBody returns the HTTP request body (hub to connector). maxBytes > 0
// caps its size.
func (s *Stream) RequestBody(maxBytes int64) io.Reader {
	return &bodyWatcher{s: s, r: &chunkReader{r: s.st, limit: maxBytes}}
}

// bodyWatcher starts watching for a hub abort once the request body has been
// fully read (the hub sends nothing more after the terminator, and half-closes
// only to cancel).
type bodyWatcher struct {
	s *Stream
	r io.Reader
}

func (b *bodyWatcher) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err == io.EOF {
		b.s.watch()
	}
	return n, err
}

func (s *Stream) watch() {
	s.watchOnce.Do(func() {
		go func() {
			var one [1]byte
			_, _ = s.st.Read(one[:]) // returns on hub half-close or session end
			s.cancel()
		}()
	})
}

// Respond sends the response header and returns the body writer: every Write
// is delivered immediately, and Close marks the body complete.
func (s *Stream) Respond(rh ResponseHeader) (io.WriteCloser, error) {
	if err := s.send(rh); err != nil {
		return nil, err
	}
	return &chunkWriter{w: s.w}, nil
}

// RespondWhole sends the response header, the whole body and its end in one write, so a
// small reply takes one turn on the link instead of three (#3440). Same bytes on the
// wire as Respond, Write, Close.
func (s *Stream) RespondWhole(rh ResponseHeader, body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.responded {
		return errors.New("tunnel: response already sent")
	}
	s.responded = true
	var buf bytes.Buffer
	if err := writeFrame(&buf, rh); err != nil {
		return err
	}
	cw := &chunkWriter{w: &buf}
	if _, err := cw.Write(body); err != nil {
		return err
	}
	if err := cw.Close(); err != nil {
		return err
	}
	_, err := s.w.Write(buf.Bytes())
	return err
}

func (s *Stream) send(rh ResponseHeader) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.responded {
		return errors.New("tunnel: response already sent")
	}
	s.responded = true
	return writeFrame(s.w, rh)
}

// Reject answers the stream with an error. status is the upstream status for
// a failed WebSocket handshake, else 0.
func (s *Stream) Reject(code, msg string, status int) {
	_ = s.send(ResponseHeader{Error: msg, ErrorCode: code, Status: status})
}

// AcceptWS completes a WebSocket stream, returning the message conn.
func (s *Stream) AcceptWS(subprotocol string) (*WSConn, error) {
	if err := s.send(ResponseHeader{Status: http.StatusSwitchingProtocols, Subprotocol: subprotocol}); err != nil {
		return nil, err
	}
	return newWSConn(streamRW{s.st, s.w}, subprotocol, s.cancel), nil
}

// Run keeps a tunnel up until ctx ends: it dials, serves, and reconnects with
// jittered exponential backoff. It returns early only for permanent errors
// (ErrUnauthorized, ErrUnsupportedVersion) so a revoked token does not retry
// forever.
func Run(ctx context.Context, rawURL, token string, o ClientOptions) error {
	attempt := 0
	note := func(connected bool, err error) {
		if o.OnState != nil {
			o.OnState(connected, err)
		}
	}
	for {
		cs, err := Dial(ctx, rawURL, token, o)
		if err == nil {
			if o.OnSession != nil {
				o.OnSession(cs)
			}
			note(true, nil)
			start := time.Now()
			select {
			case <-ctx.Done():
				_ = cs.Close()
				if o.OnSession != nil {
					o.OnSession(nil)
				}
				note(false, ctx.Err())
				return ctx.Err()
			case <-cs.Done():
			}
			if o.OnSession != nil {
				o.OnSession(nil)
			}
			note(false, ErrSessionClosed)
			if time.Since(start) > 30*time.Second {
				attempt = 0
			} else {
				attempt++
			}
		} else {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			note(false, err)
			if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrUnsupportedVersion) {
				return err
			}
			attempt++
		}
		t := time.NewTimer(o.Backoff.delay(attempt - 1))
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// streamRW reads and closes the stream and writes through the scheduler.
type streamRW struct {
	*yamux.Stream
	w io.Writer
}

func (s streamRW) Write(p []byte) (int, error) { return s.w.Write(p) }
