package tunnel

// Token redaction for the View WebSockets (#3562, #3560 phase 2). What the local server sends
// toward Coral Hub on /ws/coral and /ws/terminal passes through the secret scanner before it
// leaves this computer, for a viewer who can only look (redactFor: Interact and Launch viewers
// get it as it is, #3958); what the hub sends is untouched (input is vetted by the gate instead).
//
//   - A text frame (the session feed's JSON, the terminal's control messages) is one whole
//     reply: it is redacted on its own, so it stays valid JSON.
//   - The terminal's binary frames go through one live stream (redact.NewLiveStream): each
//     frame is scanned with the text already shown before it and sent at once. Nothing is held,
//     so typing echoes without delay (the operator's decision on #3958).
//
// What that covers, and the accepted risk: a secret that arrives whole in one frame is
// replaced, also when it ends the frame, and the part of a secret that completes it in a later
// frame is found with what was shown before. A secret split across frames (typed key by key,
// or cut where the program's output was read in chunks) shows its first part, and a secret
// typed key by key shows in full as it is typed. TestWSRedactSplitFrameSecretIsTheAcceptedRisk
// records it. Replies, logs and what Coral stores are not affected.
//
// What is never caught in a terminal: a token broken up inside the byte stream by an escape
// code inside it (a colour change, a cursor move), by a line break a program inserted itself,
// or by a screen update that rewrites only some of its characters.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/coder/websocket"

	"github.com/subgentic/coral/pkg/redact"
)

// wsRedactFailed closes a socket whose frame could not be scanned: it never passes unscanned.
const wsRedactFailed = "this stream could not be checked for secrets"

// wsRedact filters the frames one View WebSocket sends to the hub.
type wsRedact struct {
	on     func() bool // the setting, read for every frame
	stream bool        // binary frames are one terminal stream (/ws/terminal)
	// viewOnly, when set, is read for every text frame: while it says the viewer can only look, the fields in viewDroppedFields are nulled in the frame (viewdrop.go), whatever on says.
	viewOnly func() bool

	mu  sync.Mutex // one writer to dst
	ctx context.Context
	dst wsEnd
	rd  redact.Redactor
	red redact.Stream // the terminal's live stream (nil while nothing is redacted)
	out bytes.Buffer
	err error
}

func newWSRedact(ctx context.Context, dst wsEnd, rd redact.Redactor, on func() bool, stream bool) *wsRedact {
	return &wsRedact{ctx: ctx, dst: dst, rd: rd, on: on, stream: stream}
}

// write sends one frame from the local server to the hub, redacted when on says so (read for
// every frame: the owner's switch and the viewer's tier capped at the computer's mode).
func (w *wsRedact) write(typ websocket.MessageType, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	if w.viewOnly != nil && typ == websocket.MessageText && !w.stream && w.viewOnly() {
		var ok bool
		if data, ok = stripViewDropped(data); !ok {
			return w.fail()
		}
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
			w.red = w.rd.NewLiveStream(&w.out)
		}
		if !redactWrite(w.red, data) {
			return w.fail()
		}
		return w.sendOut()
	}
	out, ok := redactFrame(w.rd, data)
	if !ok {
		return w.fail()
	}
	return w.send(typ, out)
}

// close ends the stream before the socket to the hub closes.
func (w *wsRedact) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.red != nil && w.err == nil {
		_ = w.endStream()
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

// sendOut sends what the stream's scanner wrote, as one binary frame.
func (w *wsRedact) sendOut() error {
	if w.out.Len() == 0 {
		return nil
	}
	err := w.send(websocket.MessageBinary, w.out.Bytes())
	w.out.Reset()
	return err
}

func (w *wsRedact) send(typ websocket.MessageType, data []byte) error {
	if err := writeTimed(w.ctx, w.dst, typ, data); err != nil {
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

// redactFrame redacts a text frame on its own: by its structure when it is JSON (the feed), as
// text otherwise. A panic is a failure.
func redactFrame(rd redact.Redactor, data []byte) (out []byte, ok bool) {
	defer func() {
		if recover() != nil {
			out, ok = nil, false
		}
	}()
	out, err := rd.Reply(data, true)
	return out, err == nil
}
