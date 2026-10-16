package tunnel

// Token redaction for the View WebSockets (#3562, #3560 phase 2). What the local server sends
// toward Coral Hub on /ws/coral and /ws/terminal passes through the #3560 scanner before it
// leaves this computer; what the hub sends is untouched (input is vetted by the gate instead).
//
//   - A text frame (the session feed's JSON, the terminal's control messages) is one whole
//     reply: it is redacted on its own, so it stays valid JSON.
//   - The terminal's binary frames are one stream of output: they pass through one redact.Writer,
//     so a token split across frames is caught, and a frame that ends inside what could still
//     be a token holds back only that tail. Frames with nothing held go out at once. When
//     no frame follows, a held tail with no value in it yet (the first letters of a prefix)
//     is shown after wsIdleFlush, kept as context so a token it starts is still caught; one
//     that starts with a whole prefix is held up to wsHoldMax, then replaced by the marker.
//     No character of a token's value is ever shown.
//
// What is caught in a terminal: a token the program wrote as one run of characters, in any
// colour as long as no escape code sits inside it, across any number of frames; a PEM private
// key, from its BEGIN line to its END line. What isn't: a token broken up inside the byte
// stream, by an escape code inside it (a colour change, a cursor move), by a line break a
// program inserted itself to wrap it (a TUI's box drawing a token over two lines), or by a
// screen update that rewrites only some of its characters. A key whose END line is broken up
// the same way keeps the rest of the output hidden until the connection ends (it fails
// closed). A token the terminal itself wraps at the window's edge is one run in the stream and
// is caught.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/cdknorow/coral/pkg/redact"
)

// How long a terminal's held tail may wait for the next frame: the first letters of a prefix
// (typing "g" could start "ghp_") are shown after wsIdleFlush; a whole prefix with a value
// perhaps on its way is redacted after wsHoldMax.
const (
	wsIdleFlush = 100 * time.Millisecond
	wsHoldMax   = time.Second
)

// wsRedactFailed closes a socket whose frame could not be scanned: it never passes unscanned.
const wsRedactFailed = "this stream could not be checked for secrets"

// wsRedact filters the frames one View WebSocket sends to the hub.
type wsRedact struct {
	on     func() bool // the setting, read for every frame
	stream bool        // binary frames are one terminal stream (/ws/terminal)

	mu    sync.Mutex // one writer to dst: the pump or the idle flush
	ctx   context.Context
	dst   wsEnd
	red   *redact.Writer // the terminal stream's scanner (nil while the setting is off)
	out   bytes.Buffer
	timer *time.Timer
	force bool // the timer's next firing redacts a held prefix (wsHoldMax)
	err   error
}

func newWSRedact(ctx context.Context, dst wsEnd, on func() bool, stream bool) *wsRedact {
	return &wsRedact{ctx: ctx, dst: dst, on: on, stream: stream}
}

// write sends one frame from the local server to the hub, redacted.
func (w *wsRedact) write(typ websocket.MessageType, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	on := w.on()
	if w.red != nil && (!on || (typ == websocket.MessageText && terminalClosing(data))) {
		// The setting went off, or the terminal is ending: settle the stream first.
		if err := w.endStream(); err != nil {
			return err
		}
	}
	if !on {
		return w.send(typ, data)
	}
	if typ == websocket.MessageBinary && w.stream {
		if w.red == nil {
			w.red = redact.NewWriter(&w.out)
		}
		if !redactWrite(w.red, data) {
			return w.fail()
		}
		w.armIdle()
		return w.sendOut()
	}
	out, ok := redactWhole(data)
	if !ok {
		return w.fail()
	}
	return w.send(typ, out)
}

// close settles the stream before the socket to the hub closes.
func (w *wsRedact) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.red != nil && w.err == nil {
		_ = w.endStream()
	}
	if w.timer != nil {
		w.timer.Stop()
	}
}

func (w *wsRedact) endStream() error {
	ok := redactClose(w.red)
	w.red = nil
	if !ok {
		return w.fail()
	}
	return w.sendOut()
}

// armIdle schedules a Flush when the scanner holds back something not yet shown.
func (w *wsRedact) armIdle() {
	w.force = false
	if !w.holding() {
		if w.timer != nil {
			w.timer.Stop()
		}
		return
	}
	w.after(wsIdleFlush)
}

func (w *wsRedact) holding() bool {
	return w.red != nil && w.red.Holding()
}

func (w *wsRedact) after(d time.Duration) {
	if w.timer == nil {
		w.timer = time.AfterFunc(d, w.idle)
		return
	}
	w.timer.Reset(d)
}

// idle: no frame came for a while. Show what has no value in it; a held prefix is redacted
// on the second firing, wsHoldMax after the last frame.
func (w *wsRedact) idle() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.red == nil || w.err != nil {
		return
	}
	if !redactFlush(w.red, w.force) {
		_ = w.fail()
		return
	}
	_ = w.sendOut()
	if !w.force && w.holding() {
		w.force = true
		w.after(wsHoldMax - wsIdleFlush)
	}
}

// sendOut sends what the stream's scanner settled, as one binary frame.
func (w *wsRedact) sendOut() error {
	if w.out.Len() == 0 {
		return nil
	}
	err := w.send(websocket.MessageBinary, w.out.Bytes())
	w.out.Reset()
	return err
}

func (w *wsRedact) send(typ websocket.MessageType, data []byte) error {
	if err := w.dst.Write(w.ctx, typ, data); err != nil {
		w.err = err
		return err
	}
	return nil
}

var (
	errRedactFailed = errors.New("tunnel: a frame could not be checked for secrets")
	wsRedactFailLog sync.Once
)

func (w *wsRedact) fail() error {
	wsRedactFailLog.Do(func() {
		slog.Warn("[tunnel] a live stream could not be checked for secrets; it was closed to Coral Hub")
	})
	w.err = errRedactFailed
	w.dst.Close(websocket.StatusInternalError, wsRedactFailed)
	return w.err
}

// terminalClosing: the terminal's last control message (the stream ends after it).
func terminalClosing(data []byte) bool {
	return bytes.Contains(data, []byte(`"terminal_closed"`))
}

func redactFlush(r *redact.Writer, force bool) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return r.Flush(force) == nil
}
