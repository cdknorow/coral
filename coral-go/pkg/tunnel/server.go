package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"github.com/subgentic/coral/internal/safego"
)

const (
	handshakeTimeout  = 10 * time.Second
	maxHandshakeBytes = 4 << 10
	maxTokenLen       = 512
)

// SessionConfig tunes a tunnel session. Zero fields take the defaults, which
// follow the spec: ping every 20s, dead after 60s.
type SessionConfig struct {
	MaxStreams int    // concurrent streams per session, default 256
	WindowSize uint32 // yamux per-stream window, default 256 KiB
	// BulkTargetDelay is the queueing delay a bulk request (WithBulk) may add to interactive traffic on the session,
	// default 50 ms; BulkMaxRate caps a bulk request's speed in bytes/s (default 16 MiB/s); BulkMaxDuration is how long
	// a bulk push may take before it fails (default 120 s, the connector's artifact limit). See bulk_pacer.go.
	BulkTargetDelay time.Duration
	BulkMaxRate     int64
	BulkMaxDuration time.Duration
	KeepAlive       time.Duration // ping interval, default 20s
	DeadAfter       time.Duration // declare the peer dead after this, default 60s
	StreamOpenWait  time.Duration // default 10s
	Limits          Limits

	// Capabilities decides, per authenticated tunnel, which capabilities the hub
	// advertises in Welcome (version 2 peers only); nil advertises none. Only "relay" (with
	// OnRelay set) and "hub-signin" (with OnSignin set) are known.
	Capabilities func(identity any) []string
	// CheckHello vets an authenticated connector's Hello. It runs only after auth succeeds and before anything else in the
	// handshake (version negotiation, capabilities, route key included), and gets a copy of the Hello as read (CoralVersion
	// already bounded to 64 bytes, ServerToken blanked, Capabilities cloned). Its errors are answered as an AuthFunc's are:
	// ErrRetryLater (or RetryLater(after)) answers "retry_later" with its wait, ErrUnsupportedVersion answers
	// "unsupported_version", any other error "unauthorized"; each ends the handshake. A peer that fails auth never reaches
	// the hook, and its answer is the one it got before the hook existed. Nil: no check.
	CheckHello func(hello Hello, identity any) error
	// RouteKey gives, per authenticated tunnel, the route key the hub sends in Welcome
	// (#3683); nil or a malformed value sends none.
	RouteKey func(identity any) string
	// OnRelay handles a relay request the connector opens (#3489, relay.go). Nil: every
	// connector-opened stream is closed, as in version 1.
	OnRelay RelayFunc
	// OnSignin handles a Hub sign-in request the connector opens (#3966, signin.go). Nil: the
	// capability is never advertised.
	OnSignin SigninFunc
	// OnLocalMode handles the connector's local mode message (#5918, control.go). Nil: "local_mode/1" is never advertised.
	OnLocalMode LocalModeFunc
	// MinLocalModeGap is the least time between two local mode messages from one tunnel (default LocalModeMinGap, a second): a faster one is answered 429.
	MinLocalModeGap time.Duration
	// MaxSigninInFlight caps concurrent OnSignin calls per tunnel (default 2), on their own budget
	// beside the relay's; more get 429.
	MaxSigninInFlight int
	// MaxRelayInFlight caps concurrent OnRelay calls per tunnel (default 4); more
	// get 429.
	MaxRelayInFlight int
	// OnRelayArtifact handles a streamed artifact request the connector opens (relay_artifact.go,
	// #4378). Nil: the "relay_artifact" capability is never advertised and the path is closed.
	OnRelayArtifact RelayArtifactFunc
	// MaxArtifactBytes caps one artifact request's body, the design's frame included (default
	// RelayDefaultArtifact, 8 MiB plus the frame; at most RelayMaxArtifact, 16 MiB plus the frame); a
	// request declaring more is closed unanswered before a byte is read.
	MaxArtifactBytes int64
	// MaxArtifactInFlight caps concurrent OnRelayArtifact calls per tunnel (default 1); more get
	// 429. The account and hub budgets are the hub's own.
	MaxArtifactInFlight int
	// MaxRelayStreams caps connector-opened streams being read per tunnel (default 8). It is
	// taken before any goroutine or read; a stream over it is closed at once.
	MaxRelayStreams int
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
	if c.MaxRelayInFlight <= 0 {
		c.MaxRelayInFlight = 4
	}
	if c.MaxSigninInFlight <= 0 {
		c.MaxSigninInFlight = 2
	}
	if c.MaxRelayStreams <= 0 {
		c.MaxRelayStreams = 8
	}
	if c.MaxArtifactBytes <= 0 {
		c.MaxArtifactBytes = RelayDefaultArtifact
	}
	if c.MaxArtifactBytes > RelayMaxArtifact {
		c.MaxArtifactBytes = RelayMaxArtifact
	}
	if c.MaxArtifactInFlight <= 0 {
		c.MaxArtifactInFlight = 1
	}
	if c.BulkMaxDuration <= 0 {
		c.BulkMaxDuration = bulkDefDuration
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
	ws        *websocket.Conn // the tunnel's WebSocket, for CloseNow
	cfg       SessionConfig
	cancel    context.CancelFunc
	identity  any
	version   int
	coralVer  string
	bootID    string       // the connector's Hello.BootID when well formed (#5855), else ""
	startedAt time.Time    // the connector process's Hello.StartedAt when it parses, else the zero time
	localMode atomic.Int32 // the connector's maximum mode (a Mode): its Hello, then each local_mode message
	peerCaps  []string     // the connector's Hello.Capabilities when the whole list is well formed, else nil
	active    atomic.Int32
	closeOnce sync.Once
	hooks     map[Kind]inboundHook // the connector-opened kinds advertised to this tunnel
	inflight  atomic.Int32         // relay hook calls in progress
	// signinInflight: sign-in hook calls in progress (their own budget, #3966).
	signinInflight atomic.Int32
	// control requests (#5918): their own in-flight budget, and when the last local mode message was taken (at most one a second).
	controlInflight atomic.Int32
	controlMu       sync.Mutex
	lastControl     time.Time
	relaySlot       chan struct{} // connector-opened streams being handled (MaxRelayStreams)
	// artifactHook is set only on a tunnel the hub advertised relay_artifact to; artifactInflight
	// counts its calls in progress.
	artifactHook     RelayArtifactFunc
	artifactInflight atomic.Int32
	bulkOnce         sync.Once
	bulkPace         *bulkPacer // paces the requests the hub marks as bulk (WithBulk)
}

// bulk is the session's bulk pacer, made on first use.
func (s *Session) bulk() *bulkPacer {
	s.bulkOnce.Do(func() {
		s.bulkPace = newBulkPacer(s.ys.Ping, s.cfg.BulkTargetDelay, float64(s.cfg.BulkMaxRate))
	})
	return s.bulkPace
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
		w := Welcome{Error: code}
		if code == CodeRetryLater {
			w.RetryAfter = int(RetryAfterOf(werr) / time.Second) // the wait the AuthFunc asked for, if it did (RetryLater), already at most MaxRetryAfter
		}
		b, _ := json.Marshal(w)
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
		return reject(CodeRetryLater, err)
	}
	if err != nil {
		return reject(CodeUnauthorized, ErrUnauthorized)
	}
	if cfg.CheckHello != nil {
		seen := hello
		seen.ServerToken = ""                                // the hook never sees the token
		seen.Capabilities = slices.Clone(hello.Capabilities) // nor can it change what the handshake goes on to read
		if err := cfg.CheckHello(seen, identity); err != nil {
			switch {
			case errors.Is(err, ErrRetryLater): // as an AuthFunc's refusal is answered, with the wait the hook asked for
				return reject(CodeRetryLater, err)
			case errors.Is(err, ErrUnsupportedVersion):
				return reject(CodeUnsupportedVersion, ErrUnsupportedVersion)
			}
			return reject(CodeUnauthorized, ErrUnauthorized)
		}
	}
	v := hello.ProtocolVersion
	if v > ProtocolVersion {
		v = ProtocolVersion
	}
	if v < MinProtocolVersion {
		return reject(CodeUnsupportedVersion, ErrUnsupportedVersion)
	}
	// Capabilities only for a version 2 peer, only the ones this package knows, and each
	// only when the hub has its hook.
	var caps []string
	var hooks map[Kind]inboundHook
	var artifactHook RelayArtifactFunc
	if v >= 2 && cfg.Capabilities != nil {
		chosen := cfg.Capabilities(identity)
		caps, hooks = cfg.advertisedHooks(chosen)
		if cfg.OnRelayArtifact != nil {
			for _, name := range chosen {
				if name == CapabilityRelayArtifact {
					caps = append(caps, name)
					artifactHook = cfg.OnRelayArtifact
					break
				}
			}
		}
	}
	var routeKey string
	if cfg.RouteKey != nil {
		if k := cfg.RouteKey(identity); ValidRouteKey(k) {
			routeKey = k
		}
	}
	w, _ := json.Marshal(Welcome{AcceptedVersion: v, MaxStreams: cfg.MaxStreams, Capabilities: caps, RouteKey: routeKey})
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
	s := &Session{ys: ys, ws: c, cfg: cfg, cancel: cancel, identity: identity, version: v, coralVer: hello.CoralVersion, peerCaps: wellFormedCapabilities(hello.Capabilities),
		hooks: hooks, artifactHook: artifactHook, relaySlot: make(chan struct{}, cfg.MaxRelayStreams)}
	s.localMode.Store(int32(parseLocalMode(hello.LocalMode)))
	s.bootID, s.startedAt = parsePeerBoot(hello.BootID, hello.StartedAt) // informational (#5855)
	go func() { defer safego.Recover("tunnel/server:s.rejectInbound"); s.rejectInbound() }()
	go func() { defer safego.Recover("tunnel/server:191"); <-ys.CloseChan(); s.Close() }()
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

// rejectInbound closes any stream the connector opens: only the hub opens streams.
// The exceptions are the connector-opened kinds (inbound.go: the relay #3489, Hub sign-in
// #3966), and only on a tunnel the hub advertised one to: serveInbound checks its header,
// cap and deadline before the hook, and closes anything else unanswered.
func (s *Session) rejectInbound() {
	for {
		st, err := s.ys.AcceptStream()
		if err != nil {
			return
		}
		if len(s.hooks) == 0 && s.artifactHook == nil {
			_ = st.Close()
			continue
		}
		// The per-tunnel stream limit is taken here, before a goroutine or any read: a
		// flood of streams costs one close each.
		select {
		case s.relaySlot <- struct{}{}:
		default:
			_ = st.Close()
			continue
		}
		go func() {
			defer safego.Recover("tunnel/server:232")
			defer func() { <-s.relaySlot }()
			s.serveInbound(st, st.SetDeadline)
		}()
	}
}

// Identity is the value returned by the AuthFunc.
func (s *Session) Identity() any { return s.identity }

// Version is the negotiated protocol version.
func (s *Session) Version() int { return s.version }

// LocalMode is the maximum access mode the connector reported. It is only a display
// hint: an unknown or missing value is view, and the hub must never treat it as
// permission (the connector enforces its own limit).
func (s *Session) LocalMode() Mode { return Mode(s.localMode.Load()) }

func parseLocalMode(v string) Mode {
	if m, ok := ParseMode(v); ok {
		return m
	}
	return TierView
}

// PeerCapabilities are the capabilities the connector reported in its Hello (#5572), each "name/version". A list that is too long or holds any malformed string reads as none (never a partial
// read), as HasCapability does. The hub sends a feature only to a connector that says it has it.
func (s *Session) PeerCapabilities() []string { return append([]string(nil), s.peerCaps...) }

// HasPeerCapability reports whether the connector's Hello named want exactly (HasCapability on its Hello list).
func (s *Session) HasPeerCapability(want string) bool { return HasCapability(s.peerCaps, want) }

// CoralVersion is what the connector reported in its Hello.
func (s *Session) CoralVersion() string { return s.coralVer }

// PeerBootID is the connector's boot id from its Hello (#5855): random per Coral process, so a different one on a later connection means the process restarted and what it kept in memory
// is gone. "" when the Hello had none or a malformed one.
func (s *Session) PeerBootID() string { return s.bootID }

// PeerStartedAt is when the connector's process started, from its Hello (#5855); the zero time when absent or malformed.
func (s *Session) PeerStartedAt() time.Time { return s.startedAt }

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

// CloseNow ends the session at once: the WebSocket is closed without the close handshake (Close waits up to five seconds for a peer that is gone or never answers), then the session is shut down as
// Close does, so every stream ends and Done fires. It is safe to call concurrently with Close, from several goroutines and after Close: a Close stuck on an unanswering peer is released by it, and a second
// call is a no-op that returns nil. No goroutine is left waiting on the peer.
func (s *Session) CloseNow() error {
	if s.ws != nil {
		_ = s.ws.CloseNow() // already closed is not an error here
	}
	s.cancel()
	return s.Close()
}

// HubStream is a stream opened by the hub. Read and Write move raw stream
// bytes; Close releases it (and, before the response has completed, tells the
// connector to cancel).
type HubStream struct {
	s    *Session
	st   *yamux.Stream
	once sync.Once
	// pace is set on a stream opened with WithBulk: its writes are rate-limited (bulk_pacer.go). done ends a wait for it.
	pace *bulkPacer
	done chan struct{}
	// left is the bytes a bulk body still has to write (0: not known) and deadline when the push must be done: a push
	// that cannot finish by then at the lowest paced rate fails at once (ErrBulkTooSlow). Only the writer reads them.
	left     int64
	deadline time.Time
	paceOff  atomic.Bool
	paceOnce sync.Once
}

// releasePace ends the pacing of this stream (idempotent): the pings stop when its body is written, not when the
// machine has finished answering.
func (h *HubStream) releasePace() {
	if h.pace == nil {
		return
	}
	h.paceOnce.Do(func() {
		h.paceOff.Store(true)
		h.pace.release()
	})
}

func (h *HubStream) Read(p []byte) (int, error) { return h.st.Read(p) }

// Write sends p on the stream; on a bulk stream, in paced slices.
func (h *HubStream) Write(p []byte) (int, error) {
	if h.pace == nil || h.paceOff.Load() {
		return h.st.Write(p)
	}
	written := 0
	for len(p) > 0 {
		n := min(bulkSlice, len(p))
		if h.pace.tooSlow(h.left, h.deadline) {
			return written, ErrBulkTooSlow
		}
		h.pace.take(n, h.done)
		select {
		case <-h.done:
			return written, net.ErrClosed
		default:
		}
		k, err := h.st.Write(p[:n])
		written += k
		if h.left > 0 {
			h.left -= int64(k)
		}
		if err != nil {
			return written, err
		}
		p = p[n:]
	}
	return written, nil
}
func (h *HubStream) SetDeadline(t time.Time) error {
	return h.st.SetDeadline(t)
}

// Close is idempotent.
func (h *HubStream) Close() error {
	var err error
	h.once.Do(func() {
		if h.pace != nil {
			close(h.done)
			h.releasePace()
		}
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
	if isBulk(ctx) { // paced: yields to interactive traffic on this session (bulk_pacer.go)
		hs.pace, hs.done = s.bulk(), make(chan struct{})
		hs.deadline = time.Now().Add(s.cfg.BulkMaxDuration)
		hs.pace.acquire(ctx.Done())
	}
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
		return rh, &StreamError{Code: rh.ErrorCode, Message: rh.Error, Status: rh.Status, RetryAfter: CleanRetryAfter(rh.Headers.Get("Retry-After"))}
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
		Headers: req.Header.Clone(), ContentLength: req.ContentLength, HubUser: hubUserFrom(ctx),
		HubTier: hubTierFrom(ctx), HubRole: hubRoleFrom(ctx),
	}
	hs, err := s.OpenStream(ctx, hdr)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = hs.Close() })
	if hs.pace != nil && req.ContentLength > 0 {
		hs.left = req.ContentLength
	}
	werr := make(chan error, 1)
	go func() {
		defer safego.Recover("tunnel/server:374")
		cw := &chunkWriter{w: hs}
		if req.Body != nil {
			if _, err := io.Copy(cw, req.Body); err != nil {
				werr <- err
				if errors.Is(err, ErrBulkTooSlow) {
					_ = hs.Close() // end the transfer now, not at the deadline
				}
				return
			}
			req.Body.Close()
		}
		err := cw.Close()
		hs.releasePace() // the body is written: no more pings while the machine answers
		werr <- err
	}()
	rh, err := s.readResponse(hs)
	if err != nil {
		stop()
		_ = hs.Close()
		select {
		case e := <-werr:
			if errors.Is(e, ErrBulkTooSlow) {
				return nil, e
			}
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
	hs, err := s.OpenStream(ctx, Header{Kind: KindWS, Method: http.MethodGet, Path: path, Query: rawQuery, Protocols: protocols, HubUser: hubUserFrom(ctx), HubTier: hubTierFrom(ctx)})
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
