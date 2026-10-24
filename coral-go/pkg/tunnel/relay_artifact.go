package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/subgentic/coral/internal/safego"
)

// Streaming relay requests (cross-team artifacts, coral-hub design section 3.3 and 10, C0 and H0).
//
// A relay MESSAGE is at most 64 KiB, read whole and answered within seconds. A file is not: a
// connector-opened relay request on its own path, RelayArtifactPath, carries a BODY STREAMED in
// chunks that the hub reads through an io.Reader and never holds whole. It has its own capability
// ("relay_artifact", advertised by a hub only when it has OnRelayArtifact, default-deny on both ends),
// its own larger cap and its own deadlines (15 s without a byte, 120 s in all); a message keeps its
// 64 KiB and 5 s. On the connector its writes go through the uplink scheduler on the BULK lane, so a
// running transfer does not delay a terminal keystroke or a UI reply.
//
// Wire, on a connector-opened yamux stream (one request per stream):
//
//	connector -> hub   frame: Header{Kind: "relay", Method: "POST", Path: RelayArtifactPath, ContentLength: n}
//	connector -> hub   n raw bytes, no length prefix (written in chunks)
//	hub -> connector   frame: ResponseHeader{Status, Headers (only Retry-After)}
//	hub -> connector   frame: the JSON response body (at most RelayMaxResponse)
//
// The n bytes are opaque here: the hub's handler parses the design's frame inside them (a 4-byte
// metadata length, the metadata, exactly size bytes), and name and media type travel in it, never in
// a header.

// CapabilityRelayArtifact is the capability a hub advertises when it takes artifact streams from this
// tunnel.
const CapabilityRelayArtifact = "relay_artifact"

// RelayArtifactPath is the only path of a streamed relay request.
const RelayArtifactPath = "/relay/v1/artifact"

const (
	// RelayMaxArtifact is the largest body of one artifact request, the design's frame included: a
	// 4-byte length, up to 4 KiB of metadata and the file (the tunnel's ceiling; the hub's own
	// per-artifact cap, 8 MiB by default, is its handler's).
	RelayMaxArtifact = 16<<20 + 4<<10 + 4

	// RelayDefaultArtifact is the default cap (the design's 8 MiB per artifact plus its frame: a
	// 4-byte length, up to 4 KiB of metadata and a little more); MaxArtifactBytes raises it up to
	// RelayMaxArtifact, never past.
	RelayDefaultArtifact = 8<<20 + 4 + 4<<10 + 256

	// artifactIdle: no byte for this long ends the request, on both sides.
	artifactIdle = 15 * time.Second
	// artifactTotal bounds the whole request.
	artifactTotal = 120 * time.Second
	// artifactChunk is how much of the body the connector reads and writes at a time.
	artifactChunk = 64 << 10
)

// artifactTimings are the deadlines of an artifact request and the uplink lane it is written on. Tests change them (a var, so a test can
// also show the lane is what keeps interactive traffic fast), while a request started by an earlier test can still be running and
// reading them: they are held behind an atomic pointer and read once per request (#5148: the race detector caught a bare variable).
type artifactTimings struct {
	idleFor, totalFor time.Duration
	lane              int
}

var artifactCfg atomic.Pointer[artifactTimings]

func init() {
	artifactCfg.Store(&artifactTimings{idleFor: artifactIdle, totalFor: artifactTotal, lane: laneBulk})
}

// artifactNow is the timings a request that starts now runs with.
func artifactNow() artifactTimings { return *artifactCfg.Load() }

// ErrRelayArtifactUnsupported: the hub didn't advertise artifact streams for this tunnel (an older hub,
// the artifact relay off for the account, or no link for this server).
var ErrRelayArtifactUnsupported = errors.New("tunnel: Coral Hub doesn't relay files for this server")

// ErrRelayArtifactClosed: the hub closed the stream before answering, after the connector had begun to
// send (a refusal it could not answer in time, an abort on its side, a stalled target). Not "unsupported":
// the capability was advertised.
var ErrRelayArtifactClosed = errors.New("tunnel: Coral Hub closed the file transfer without an answer")

// errArtifactIdle: no progress for the idle time.
var errArtifactIdle = errors.New("tunnel: the artifact request made no progress")

// ErrArtifactLength: the body was not exactly the declared length.
var ErrArtifactLength = errors.New("tunnel: the artifact body is not the declared length")

// RelayArtifactRequest is what the hub's OnRelayArtifact hook gets: the path (always
// RelayArtifactPath), the declared length n (within 1..the cap) and the body, a reader that yields at
// most n bytes and then io.EOF; a body that stops short gives io.ErrUnexpectedEOF, and one that goes
// quiet for 15 s gives a timeout error. The source server is the Session's identity, never anything in
// the request. The hook must read the body to EOF; Trailing then says whether more bytes arrived than
// were declared (the hub aborts then).
type RelayArtifactRequest struct {
	Path          string
	ContentLength int64
	Body          io.Reader
	trailing      func() bool
}

// Trailing reports whether the connector sent a byte past the declared length. Call it after the body
// has been read to EOF. It is DIAGNOSTICS, not a control: it looks for 10 ms at what has already arrived,
// so a byte still in flight is missed. What bounds the sender is the exact-n reader (the hub never reads
// past n) and the closing of the stream when the call ends.
func (r RelayArtifactRequest) Trailing() bool { return r.trailing != nil && r.trailing() }

// RelayArtifactFunc handles one streamed artifact request on the hub, like RelayFunc: it must
// re-check the account, the link and the target on EVERY call. The response goes back like a
// message's.
type RelayArtifactFunc func(ctx context.Context, s *Session, req RelayArtifactRequest) RelayResponse

// CanRelayArtifact reports whether the hub advertised artifact streams for this tunnel.
func (c *ClientSession) CanRelayArtifact() bool { return c.advertised(CapabilityRelayArtifact) }

// RelayArtifact sends one streamed artifact request: n bytes read from body (exactly n: fewer or more
// fails the call and closes the stream, so the hub sees a short body or none, never an overrun),
// written in chunks through the uplink scheduler on the bulk lane. It fails with
// ErrRelayArtifactUnsupported when the hub didn't advertise the capability, and with an error when n
// is outside 1..RelayMaxArtifact. 15 s without progress or 120 s in all ends it, also when it is the BODY
// that stopped giving bytes: that read is abandoned (its goroutine ends when the reader returns or is
// closed), so the caller closes body when the call returns.
func (c *ClientSession) RelayArtifact(ctx context.Context, n int64, body io.Reader) (RelayResponse, error) {
	if n < 1 || n > RelayMaxArtifact {
		return RelayResponse{}, fmt.Errorf("tunnel: artifact request of %d bytes: outside 1 to %d", n, RelayMaxArtifact)
	}
	if !c.CanRelayArtifact() {
		return RelayResponse{}, ErrRelayArtifactUnsupported
	}
	tm := artifactNow() // read once: tests change them
	idleFor, totalFor, lane := tm.idleFor, tm.totalFor, tm.lane
	ctx, cancel := context.WithTimeout(ctx, totalFor)
	defer cancel()
	st, err := c.ys.OpenStream()
	if err != nil {
		return RelayResponse{}, err
	}
	defer st.Close()
	stop := context.AfterFunc(ctx, func() { _ = st.Close() })
	defer stop()
	fail := func(err error) (RelayResponse, error) {
		return RelayResponse{}, c.inboundErr(ctx, err, ErrRelayArtifactClosed)
	}
	wd := startArtifactWatchdog(ctx, st, idleFor)
	defer wd.stop()
	w := io.Writer(st)
	if c.up != nil {
		w = &upWriter{st: st, u: c.up, lane: lane, done: ctx.Done()}
	}
	if err := writeFrame(w, Header{Kind: KindRelay, Method: http.MethodPost, Path: RelayArtifactPath, ContentLength: n}); err != nil {
		return fail(err)
	}
	replies := c.awaitArtifactReply(st)
	rd := artifactBodyReader{body: body, idle: wd.idle, done: ctx.Done(), ctxErr: ctx.Err}
	if resp, err, done := sendArtifactBody(ctx, w, st, rd, replies, wd, n, fail); done {
		return resp, err
	}
	// The whole body is sent: from here the hub is working (it may be waiting on the target), which the
	// 120 s total bounds; the idle deadline is about bytes of the body.
	wd.stop()
	select {
	case r := <-replies:
		if r.err != nil {
			return fail(r.err)
		}
		return r.resp, nil
	case <-wd.idle:
		return fail(errArtifactIdle)
	case <-ctx.Done():
		return fail(ctx.Err())
	}
}

// artifactWatchdog ends an artifact request that makes no progress: progress (a chunk read from the body and
// written) pushes it back; none for the idle time closes the stream and closes idle, also when it is the BODY
// that went quiet (a read that never returns is abandoned, not waited for).
type artifactWatchdog struct {
	progress atomic.Int64
	idle     chan struct{} // closed when the idle time passed with no progress
	done     chan struct{} // closed by stop
	once     sync.Once
}

func startArtifactWatchdog(ctx context.Context, st io.Closer, idleFor time.Duration) *artifactWatchdog {
	wd := &artifactWatchdog{idle: make(chan struct{}), done: make(chan struct{})}
	wd.touch()
	go func() {
		defer safego.Recover("tunnel/relay_artifact:watchdog")
		t := time.NewTicker(idleFor / 4)
		defer t.Stop()
		for {
			select {
			case <-wd.done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if time.Since(time.Unix(0, wd.progress.Load())) >= idleFor {
					close(wd.idle)
					_ = st.Close()
					return
				}
			}
		}
	}()
	return wd
}

// touch records progress now.
func (wd *artifactWatchdog) touch() { wd.progress.Store(time.Now().UnixNano()) }

// stop ends the watchdog (more than once is fine).
func (wd *artifactWatchdog) stop() { wd.once.Do(func() { close(wd.done) }) }

// artifactReply is the hub's answer to an artifact request, or why there is none.
type artifactReply struct {
	resp RelayResponse
	err  error
}

// awaitArtifactReply reads the hub's answer on its own goroutine from the start: a hub that refuses before it reads
// the body (over its budget, an unknown address, a switch off) answers and closes, and the answer is then the call's
// result however much of the body was still unsent; the upload is cut short by closing the stream, so the sender
// gets the hub's reason, not a stream error.
func (c *ClientSession) awaitArtifactReply(st io.ReadCloser) <-chan artifactReply {
	replies := make(chan artifactReply, 1)
	go func() {
		defer safego.Recover("tunnel/relay_artifact:reply")
		b, err := readFrame(st, c.cfg.Limits.MaxHeaderBytes)
		if err != nil {
			replies <- artifactReply{err: err}
			return
		}
		resp, err := c.readInboundReply(st, b, relayKind)
		replies <- artifactReply{resp, err}
		if err == nil {
			_ = st.Close() // answered: any write still blocked ends
		}
	}()
	return replies
}

// artifactBodyReader reads the request's body on a goroutine, so that a body that goes quiet is abandoned on the
// idle or total deadline, not waited for.
type artifactBodyReader struct {
	body   io.Reader
	idle   <-chan struct{}
	done   <-chan struct{}
	ctxErr func() error
}

// read reads exactly p when full (a chunk), otherwise one Read (the check for a byte past n).
func (rd artifactBodyReader) read(p []byte, full bool) (int, error) {
	type chunk struct {
		n   int
		err error
	}
	res := make(chan chunk, 1)
	go func() {
		defer safego.Recover("tunnel/relay_artifact:read")
		var m int
		var err error
		if full {
			m, err = io.ReadFull(rd.body, p)
		} else {
			m, err = rd.body.Read(p)
		}
		res <- chunk{m, err}
	}()
	select {
	case r := <-res:
		return r.n, r.err
	case <-rd.idle:
		return 0, errArtifactIdle
	case <-rd.done:
		return 0, rd.ctxErr()
	}
}

// sendArtifactBody writes exactly n bytes of the body in chunks. done is set when the call ends here: the hub
// answered while the body was being written (its answer is the result), a deadline, a write that failed, or a body
// shorter or longer than n (the stream is closed, so the hub sees a short body or none, never an overrun).
func sendArtifactBody(ctx context.Context, w io.Writer, st io.Closer, rd artifactBodyReader, replies <-chan artifactReply,
	wd *artifactWatchdog, n int64, fail func(error) (RelayResponse, error)) (RelayResponse, error, bool) {
	// answered: a write failed because the hub already answered (or never will): wait a moment for the
	// answer that is, or is not, already there.
	answered := func() (RelayResponse, bool) {
		select {
		case r := <-replies:
			if r.err == nil {
				return r.resp, true
			}
		case <-time.After(2 * time.Second):
		}
		return RelayResponse{}, false
	}
	buf := make([]byte, artifactChunk)
	for left := n; left > 0; {
		want := min(int64(len(buf)), left)
		m, rerr := rd.read(buf[:want], true)
		if errors.Is(rerr, errArtifactIdle) || (rerr != nil && ctx.Err() != nil) {
			resp, err := fail(rerr)
			return resp, err, true
		}
		if m > 0 {
			if _, err := w.Write(buf[:m]); err != nil {
				if ctx.Err() == nil {
					if resp, ok := answered(); ok {
						return resp, nil, true
					}
				}
				resp, err := fail(err)
				return resp, err, true
			}
			wd.touch()
			left -= int64(m)
		}
		if rerr != nil {
			_ = st.Close() // short: the hub sees a stream that ends before its declared length
			return RelayResponse{}, fmt.Errorf("%w: the body ended %d bytes short: %v", ErrArtifactLength, left, rerr), true
		}
	}
	// A byte past n fails the call: read through the same abandonable goroutine, so a body that gives n
	// bytes and then blocks ends at the idle deadline instead of hanging the call.
	var extra [1]byte
	k, xerr := rd.read(extra[:], false)
	if errors.Is(xerr, errArtifactIdle) || (xerr != nil && ctx.Err() != nil) {
		resp, err := fail(xerr)
		return resp, err, true
	}
	if k > 0 {
		_ = st.Close()
		return RelayResponse{}, fmt.Errorf("%w: the body is longer than the declared %d bytes", ErrArtifactLength, n), true
	}
	return RelayResponse{}, nil, false
}

// artifactIdleReader gives the body of a streamed request to the hub's hook: at most n bytes, each Read
// under its own idle deadline, and every Read (the call and its return) counted as activity: the
// serveArtifact watchdog aborts the request when there is none for the idle time, which is a
// SENDER that went quiet or a HOOK that stopped reading (a stalled target it is writing to).
type artifactIdleReader struct {
	st          io.Reader
	setDeadline func(time.Time) error
	left        int64
	idle        time.Duration
	mu          sync.Mutex
	active      atomic.Int64 // UnixNano of the last Read call or return
	done        atomic.Bool  // the whole body was read: the idle deadline no longer applies (the hook is waiting on its target; the total bounds that)
}

func (r *artifactIdleReader) touch() { r.active.Store(time.Now().UnixNano()) }

func (r *artifactIdleReader) Read(p []byte) (int, error) {
	r.touch()
	defer r.touch()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.left <= 0 {
		r.done.Store(true)
		return 0, io.EOF
	}
	if int64(len(p)) > r.left {
		p = p[:r.left]
	}
	_ = r.setDeadline(time.Now().Add(r.idle))
	n, err := r.st.Read(p)
	r.left -= int64(n)
	if err == io.EOF && r.left > 0 {
		err = io.ErrUnexpectedEOF
	}
	if err == nil && r.left <= 0 {
		err = io.EOF // exactly n bytes, then EOF: the byte count is the framing
		r.done.Store(true)
	}
	if n > 0 && err == io.EOF {
		return n, nil // deliver the last bytes first; the next Read reports EOF
	}
	return n, err
}

// serveArtifact handles one streamed artifact request on the hub: a valid header within the cap and
// the tunnel's artifact budget, then the hook with a reader over the body. Anything invalid is closed
// unanswered, as for every connector-opened stream.
func (s *Session) serveArtifact(st io.ReadWriteCloser, h Header, setDeadline func(time.Time) error) {
	if s.artifactHook == nil || !validArtifactHeader(h, s.cfg.MaxArtifactBytes) {
		return
	}
	// A panic in the hook is the fixed 502 and never the request (as for messages).
	defer func() {
		if p := recover(); p != nil {
			logInboundPanic()
			_ = setDeadline(time.Now().Add(inboundReadDeadline))
			writeInboundResponse(st, RelayResponse{Status: http.StatusBadGateway, Body: relayKind.failed})
		}
	}()
	if int(s.artifactInflight.Add(1)) > s.cfg.MaxArtifactInFlight {
		s.artifactInflight.Add(-1)
		_ = setDeadline(time.Now().Add(inboundReadDeadline))
		writeInboundResponse(st, RelayResponse{Status: http.StatusTooManyRequests, Body: relayKind.busy, RetryAfter: 1})
		return
	}
	defer s.artifactInflight.Add(-1)
	tm := artifactNow() // read once: tests change them
	idleFor, totalFor := tm.idleFor, tm.totalFor
	ctx, cancel := context.WithTimeout(context.Background(), totalFor)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = st.Close() })
	defer stop()
	go func() {
		defer safego.Recover("tunnel/relay_artifact:session")
		select {
		case <-s.ys.CloseChan():
			cancel()
		case <-ctx.Done():
		}
	}()
	body := &artifactIdleReader{st: st, setDeadline: setDeadline, left: h.ContentLength, idle: idleFor}
	body.touch()
	// The idle watchdog: any abort closes BOTH streams (this one, and the target's through the hook's
	// context), whoever stopped: the sender, or the hook that stopped reading.
	go func() {
		defer safego.Recover("tunnel/relay_artifact:idle")
		t := time.NewTicker(idleFor / 4)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if body.done.Load() {
					return
				}
				if time.Since(time.Unix(0, body.active.Load())) >= idleFor {
					cancel()
					_ = st.Close()
					return
				}
			}
		}
	}()
	req := RelayArtifactRequest{Path: h.Path, ContentLength: h.ContentLength, Body: body, trailing: func() bool {
		// A byte that arrived past the declared length: look without waiting.
		_ = setDeadline(time.Now().Add(10 * time.Millisecond))
		var one [1]byte
		n, _ := st.Read(one[:])
		return n > 0
	}}
	resp := s.artifactHook(ctx, s, req)
	if len(resp.Body) > relayKind.maxReply {
		resp = RelayResponse{Status: http.StatusBadGateway, Body: relayKind.failed}
	}
	_ = setDeadline(time.Now().Add(inboundReadDeadline))
	writeInboundResponse(st, resp)
}

// validArtifactHeader reports whether h is exactly a streamed artifact request: POST, the artifact path,
// nothing else, and a length of 1 to max.
func validArtifactHeader(h Header, max int64) bool {
	return h.Kind == KindRelay && h.Method == http.MethodPost && h.Path == RelayArtifactPath &&
		h.Query == "" && len(h.Headers) == 0 && len(h.Protocols) == 0 && h.HubUser == "" && h.HubTier == "" && h.HubRole == "" && h.HubOrg == "" && h.HubTeamRole == "" && h.HubUserID == "" &&
		h.ContentLength >= 1 && h.ContentLength <= max
}
