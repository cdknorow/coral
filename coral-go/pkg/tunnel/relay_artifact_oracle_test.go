package tunnel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/safego"
)

// The outcomes the RelayArtifact oracle must reach, each counted from the call's outcome, with a floor.
var relayArtifactOutcomes = map[string]int{
	"out of range":                       5,
	"unsupported":                        5,
	"answered after the whole body":      40,
	"a refusal answered":                 20,
	"refused before reading, small body": 10,
	"refused before reading, big body":   5,
	"answered after reading half":        5,
	"an answer the connector can't read": 5,
	"a slow but steady body":             5,
	"a slow answer after the body":       5,
	"body short":                         10,
	"body long":                          10,
	"body blocked after n bytes: idle":   5,
	"a cancelled context":                5,
	"several chunks":                     10,
}

// #4857: ClientSession.RelayArtifact split into startArtifactWatchdog, awaitArtifactReply, artifactBodyReader and
// sendArtifactBody. For random scenarios (sizes around the 64 KiB chunk, bodies exact, short, long or blocking after
// n bytes, a hub that reads all and answers, refuses before reading, answers after half, or answers slowly after the
// body, the capability off, sizes out of range, a cancelled context) the old call and the new one end the same: the
// same answer, the same kind of error (the length and range errors word for word), and the hub received the same.
func TestRelayArtifactAgreesWithBefore4857(t *testing.T) {
	oldIdle := artifactNow().idleFor
	setArtifactTimings(t, func(*artifactTimings) {}) // puts the timings back after the test, whatever a scenario sets
	type hubMode struct {
		kind   string // "all", "early", "half", "slow"
		status int
		retry  int
	}
	var mode atomic.Value
	mode.Store(hubMode{kind: "all", status: 200})
	var mu sync.Mutex
	var got string // what the hub received: length, hash, trailing
	// How many times the hub's hook has started and returned (#5007). The test waits for them instead of sleeping 20 ms: a loaded or -race host
	// stretches the hook past that, so what it recorded was missing from the call's result, or arrived during the NEXT call (which then met the
	// artifact slot still busy and was answered 429 rate_limited).
	var hooksStarted, hooksEnded atomic.Int64
	var hubSession atomic.Pointer[Session] // the hub's session: its in-flight artifact count must be back to 0 before the next call
	h := artifactHub(t, SessionConfig{MaxArtifactBytes: 8 << 20}, func(ctx context.Context, s *Session, req RelayArtifactRequest) RelayResponse {
		hubSession.Store(s)
		hooksStarted.Add(1)
		defer hooksEnded.Add(1)
		m := mode.Load().(hubMode)
		record := func(r io.Reader, limit int64) {
			hash := sha256.New()
			k, err := io.Copy(hash, io.LimitReader(r, limit))
			mu.Lock()
			got = fmt.Sprintf("len=%d sum=%x err=%v", k, hash.Sum(nil)[:6], err != nil)
			mu.Unlock()
		}
		resp := RelayResponse{Status: m.status, Body: []byte(fmt.Sprintf(`{"status":%d}`, m.status)), RetryAfter: m.retry}
		switch m.kind {
		case "early":
			mu.Lock()
			got = "not read"
			mu.Unlock()
			return resp
		case "half":
			record(req.Body, req.ContentLength/2)
			return resp
		case "bad": // an answer the connector refuses to read: a status outside 200..599, or a body over the reply cap
			record(req.Body, req.ContentLength+1)
			if m.status == 200 {
				return RelayResponse{Status: 200, Body: bytes.Repeat([]byte("x"), 70<<10)}
			}
			return RelayResponse{Status: 150, Body: okBody()}
		case "slow":
			record(req.Body, req.ContentLength+1)
			select {
			case <-time.After(3 * artifactNow().idleFor):
			case <-ctx.Done():
			}
			return resp
		}
		record(req.Body, req.ContentLength+1)
		return resp
	})
	_, cs := connect(t, h, noopLocal(), nil)
	off := artifactHub(t, SessionConfig{Capabilities: func(any) []string { return []string{CapabilityRelay} }}, nil)
	_, csOff := connect(t, off, noopLocal(), nil)

	class := func(err error) string {
		switch {
		case err == nil:
			return "nil"
		case errors.Is(err, ErrRelayArtifactUnsupported):
			return "unsupported"
		case errors.Is(err, ErrArtifactLength):
			return "length: " + err.Error()
		case strings.Contains(err.Error(), "outside 1 to"):
			return err.Error()
		case errors.Is(err, context.Canceled):
			return "cancelled"
		case errors.Is(err, errArtifactIdle), strings.Contains(err.Error(), "no progress"):
			return "idle"
		case errors.Is(err, ErrRelayArtifactClosed):
			return "closed"
		}
		return "other"
	}
	waitHub := func(done func() bool) {
		deadline := time.Now().Add(15 * time.Second)
		for !done() && time.Now().Before(deadline) {
			time.Sleep(2 * time.Millisecond)
		}
	}
	sizes := []int64{1, 1000, 64 << 10, 64<<10 + 1, 200 << 10, 6 << 20}
	counts := map[string]int{}
	for seed := int64(1); seed <= 220; seed++ {
		rng := rand.New(rand.NewSource(seed))
		n := sizes[rng.Intn(len(sizes)-1)] // the 6 MiB size only where a scenario asks for it
		m := hubMode{kind: "all", status: []int{200, 202, 429, 500}[rng.Intn(4)]}
		if m.status == 429 {
			m.retry = 1 + rng.Intn(3)
		}
		setArtifactIdle(oldIdle)
		bodyKind := "exact"
		client := cs
		cancelled := false
		switch rng.Intn(18) {
		case 0:
			n = []int64{0, -1, RelayMaxArtifact + 1}[rng.Intn(3)]
		case 1:
			client = csOff
		case 2, 3:
			bodyKind = "short"
		case 4, 5:
			bodyKind = "long"
		case 6:
			bodyKind = "block"
			setArtifactIdle(300 * time.Millisecond)
		case 7:
			m.kind = "early"
		case 11:
			m.kind, n = "early", 6<<20 // far past the stream window: the writer is blocked when the hub answers
		case 12:
			m.kind = "bad"
		case 13:
			// a body that gives each chunk 100 ms apart: progress keeps a 300 ms idle time from firing
			bodyKind, n = "trickle", 4*64<<10
			setArtifactIdle(300 * time.Millisecond)
		case 8:
			m.kind, n = "half", 200<<10
		case 9:
			m.kind = "slow"
			setArtifactIdle(200 * time.Millisecond)
		case 10:
			cancelled = true
		}
		mode.Store(m)
		shortBy := 1 + rng.Intn(5)
		body := func() io.Reader {
			size := n
			if size < 0 || size > 8<<20 {
				size = 10
			}
			payload := bytes.Repeat([]byte{byte(seed)}, int(size))
			for i := range payload {
				payload[i] = byte((int64(i)*7 + seed) % 251)
			}
			switch bodyKind {
			case "short":
				return bytes.NewReader(payload[:max(0, len(payload)-shortBy)])
			case "long":
				return bytes.NewReader(append(payload, 'x', 'y'))
			case "trickle":
				return &trickleReader{r: bytes.NewReader(payload), every: 100 * time.Millisecond}
			case "block":
				pr, pw := io.Pipe()
				go func() {
					defer safego.Recover("test")
					pw.Write(payload) // exactly n, then blocks
				}()
				t.Cleanup(func() { pw.Close() })
				return pr
			}
			return bytes.NewReader(payload)
		}
		// A frame reaches the hub's hook when the capability is on, the size is in range and the context is live.
		// A body that is too short or too long, or stops after n bytes, ends in the CLIENT's error (length, idle): the call resets the stream
		// as soon as it sees it, and whether the hub's hook had started, and how much it had read when the reset reached it, is a race in the
		// old call and the new one alike (seeds 77, 95, 168 under load, #5007). What the hub received is compared only where the call ends
		// without its own abort.
		clientAborts := bodyKind == "short" || bodyKind == "long" || bodyKind == "block"
		expectHook := !cancelled && client == cs && n >= 1 && n <= RelayMaxArtifact && !clientAborts
		var outs [2]string
		for i := range outs {
			ctx, cancel := context.WithCancel(context.Background())
			if cancelled {
				cancel()
			}
			mu.Lock()
			got = "nothing"
			mu.Unlock()
			// The last call's hook has returned and the session has released its in-flight slot (a call that began while it was held was answered
			// 429 rate_limited, seeds 28, 156, 218 under load): nothing of the last call is still running.
			waitHub(func() bool {
				hs := hubSession.Load()
				return hooksStarted.Load() == hooksEnded.Load() && (hs == nil || hs.artifactInflight.Load() == 0)
			})
			endedBefore := hooksEnded.Load()
			b := body() // the same body for both calls
			var resp RelayResponse
			var err error
			if i == 0 {
				resp, err = client.relayArtifactBefore4857(ctx, n, b)
			} else {
				resp, err = client.RelayArtifact(ctx, n, b)
			}
			cancel()
			if expectHook {
				// The hub's hook returns when it has read the body, which a loaded or -race host can put well after the call returned (seed 198
				// failed with "nothing" against 204800 bytes, #5007). Wait for it; a hook that never runs (it should always) leaves "nothing", as
				// before, after the deadline.
				waitHub(func() bool { return hooksEnded.Load() > endedBefore })
			} else {
				time.Sleep(20 * time.Millisecond) // no frame reaches the hub (range, capability off, cancelled): nothing to wait for
			}
			mu.Lock()
			hub := got
			if cancelled || (clientAborts && client == cs && n >= 1 && n <= RelayMaxArtifact) {
				hub = "-" // whether the header reached the hub before the stream closed is a race, in both
			}
			outs[i] = fmt.Sprintf("status=%d retry=%d body=%s err=%s hub=%s", resp.Status, resp.RetryAfter, resp.Body, class(err), hub)
			mu.Unlock()
		}
		label := fmt.Sprintf("seed %d n=%d body=%s hub=%+v cancelled=%v off=%v", seed, n, bodyKind, m, cancelled, client == csOff)
		require.Equal(t, outs[0], outs[1], label)

		o := outs[1]
		switch {
		case strings.Contains(o, "outside 1 to"):
			counts["out of range"]++
		case strings.Contains(o, "err=unsupported"):
			counts["unsupported"]++
		case strings.Contains(o, "err=cancelled"):
			counts["a cancelled context"]++
		case strings.Contains(o, "ended") && strings.Contains(o, "short"):
			counts["body short"]++
		case strings.Contains(o, "longer than the declared"):
			counts["body long"]++
		case bodyKind == "block" && strings.Contains(o, "err=idle"):
			counts["body blocked after n bytes: idle"]++
		case m.kind == "early" && strings.Contains(o, "err=nil") && n == 6<<20:
			counts["refused before reading, big body"]++
		case m.kind == "early" && strings.Contains(o, "err=nil"):
			counts["refused before reading, small body"]++
		case m.kind == "bad" && !strings.Contains(o, "err=nil"):
			counts["an answer the connector can't read"]++
		case bodyKind == "trickle" && strings.Contains(o, "err=nil"):
			counts["a slow but steady body"]++
		case m.kind == "half" && strings.Contains(o, "err=nil"):
			counts["answered after reading half"]++
		case m.kind == "slow" && strings.Contains(o, "err=nil"):
			counts["a slow answer after the body"]++
		case m.kind == "all" && strings.Contains(o, "err=nil"):
			counts["answered after the whole body"]++
			if n > 64<<10 {
				counts["several chunks"]++
			}
		}
		if strings.Contains(o, "err=nil") && m.status >= 400 {
			counts["a refusal answered"]++
		}
	}
	t.Logf("compared, per outcome: %v", counts)
	for name, floor := range relayArtifactOutcomes {
		require.GreaterOrEqual(t, counts[name], floor, "%q went (almost) unexercised", name)
	}
}

// trickleReader gives one chunk's worth at a time, every so often: a slow body that keeps making progress.
type trickleReader struct {
	r     io.Reader
	every time.Duration
}

func (t *trickleReader) Read(p []byte) (int, error) {
	time.Sleep(t.every)
	return t.r.Read(p[:min(len(p), 64<<10)])
}

// relayArtifactBefore4857 is ClientSession.RelayArtifact as it was before #4857.
func (c *ClientSession) relayArtifactBefore4857(ctx context.Context, n int64, body io.Reader) (RelayResponse, error) {
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

	// Idle watchdog: progress (a chunk read from the body and written) pushes it back; none for
	// the idle time closes the stream and ends the call, also when it is the BODY that went quiet
	// (a read that never returns is abandoned, not waited for).
	var progress atomic.Int64
	progress.Store(time.Now().UnixNano())
	wdDone := make(chan struct{})
	var stopWatchdog sync.Once
	stopIdle := func() { stopWatchdog.Do(func() { close(wdDone) }) }
	defer stopIdle()
	idle := make(chan struct{})
	go func() {
		defer safego.Recover("tunnel/relay_artifact:watchdog")
		t := time.NewTicker(idleFor / 4)
		defer t.Stop()
		for {
			select {
			case <-wdDone:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if time.Since(time.Unix(0, progress.Load())) >= idleFor {
					close(idle)
					_ = st.Close()
					return
				}
			}
		}
	}()

	w := io.Writer(st)
	if c.up != nil {
		w = &upWriter{st: st, u: c.up, lane: lane, done: ctx.Done()}
	}
	if err := writeFrame(w, Header{Kind: KindRelay, Method: http.MethodPost, Path: RelayArtifactPath, ContentLength: n}); err != nil {
		return fail(err)
	}
	// The hub's answer is read on its own goroutine from the start: a hub that refuses before it reads the
	// body (over its budget, an unknown address, a switch off) answers and closes, and the answer is then
	// the call's result however much of the body was still unsent; the upload is cut short by closing the
	// stream, so the sender gets the hub's reason, not a stream error.
	type reply struct {
		resp RelayResponse
		err  error
	}
	replyCh := make(chan reply, 1)
	go func() {
		defer safego.Recover("tunnel/relay_artifact:reply")
		b, err := readFrame(st, c.cfg.Limits.MaxHeaderBytes)
		if err != nil {
			replyCh <- reply{err: err}
			return
		}
		resp, err := c.readInboundReply(st, b, relayKind)
		replyCh <- reply{resp, err}
		if err == nil {
			_ = st.Close() // answered: any write still blocked ends
		}
	}()
	// answered: a write failed because the hub already answered (or never will): wait a moment for the
	// answer that is, or is not, already there.
	answered := func(werr error) (RelayResponse, error, bool) {
		select {
		case r := <-replyCh:
			if r.err == nil {
				return r.resp, nil, true
			}
		case <-time.After(2 * time.Second):
		}
		return RelayResponse{}, werr, false
	}
	buf := make([]byte, artifactChunk)
	type chunk struct {
		n   int
		err error
	}
	// readChunk reads from the body in a goroutine so that a body that goes quiet is abandoned on the idle
	// or total deadline, not waited for. full reads exactly p (a chunk); otherwise one Read (the check
	// for a byte past n).
	readChunk := func(p []byte, full bool) (int, error) {
		res := make(chan chunk, 1)
		go func() {
			defer safego.Recover("tunnel/relay_artifact:read")
			var m int
			var err error
			if full {
				m, err = io.ReadFull(body, p)
			} else {
				m, err = body.Read(p)
			}
			res <- chunk{m, err}
		}()
		select {
		case r := <-res:
			return r.n, r.err
		case <-idle:
			return 0, errArtifactIdle
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	for left := n; left > 0; {
		want := int64(len(buf))
		if left < want {
			want = left
		}
		m, rerr := readChunk(buf[:want], true)
		if errors.Is(rerr, errArtifactIdle) || (rerr != nil && ctx.Err() != nil) {
			return fail(rerr)
		}
		if m > 0 {
			if _, err := w.Write(buf[:m]); err != nil {
				if ctx.Err() == nil {
					if resp, _, ok := answered(err); ok {
						return resp, nil
					}
				}
				return fail(err)
			}
			progress.Store(time.Now().UnixNano())
			left -= int64(m)
		}
		if rerr != nil {
			_ = st.Close() // short: the hub sees a stream that ends before its declared length
			return RelayResponse{}, fmt.Errorf("%w: the body ended %d bytes short: %v", ErrArtifactLength, left, rerr)
		}
	}
	// A byte past n fails the call: read through the same abandonable goroutine, so a body that gives n
	// bytes and then blocks ends at the idle deadline instead of hanging the call.
	var extra [1]byte
	k, xerr := readChunk(extra[:], false)
	if errors.Is(xerr, errArtifactIdle) || (xerr != nil && ctx.Err() != nil) {
		return fail(xerr)
	}
	if k > 0 {
		_ = st.Close()
		return RelayResponse{}, fmt.Errorf("%w: the body is longer than the declared %d bytes", ErrArtifactLength, n)
	}
	// The whole body is sent: from here the hub is working (it may be waiting on the target), which the
	// 120 s total bounds; the idle deadline is about bytes of the body.
	stopIdle()

	select {
	case r := <-replyCh:
		if r.err != nil {
			return fail(r.err)
		}
		return r.resp, nil
	case <-idle:
		return fail(errArtifactIdle)
	case <-ctx.Done():
		return fail(ctx.Err())
	}
}
