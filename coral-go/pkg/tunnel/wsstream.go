package tunnel

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"sync/atomic"

	"nhooyr.io/websocket"
)

const (
	wsFrameText   byte = 1
	wsFrameBinary byte = 2
	wsFrameClose  byte = 3

	// DefaultWSReadLimit bounds one WebSocket message crossing the tunnel.
	DefaultWSReadLimit = 16 << 20
)

// WSConn is a WebSocket carried over a tunnel stream. It has the same method
// shapes as *websocket.Conn (Read, Write, Close, Subprotocol, SetReadLimit),
// and Read returns websocket.CloseError for a peer close frame, so code
// written against nhooyr's API works unchanged.
type WSConn struct {
	rw   io.ReadWriteCloser
	sub  string
	rlim atomic.Int64

	wmu       sync.Mutex
	closeSent bool
	closeOnce sync.Once
	onClose   func()
}

func newWSConn(rw io.ReadWriteCloser, sub string, onClose func()) *WSConn {
	c := &WSConn{rw: rw, sub: sub, onClose: onClose}
	c.rlim.Store(DefaultWSReadLimit)
	return c
}

// Subprotocol returns the negotiated subprotocol.
func (c *WSConn) Subprotocol() string { return c.sub }

// SetReadLimit caps the size of one received message.
func (c *WSConn) SetReadLimit(n int64) { c.rlim.Store(n) }

func (c *WSConn) shutdown() {
	c.closeOnce.Do(func() {
		_ = c.rw.Close()
		if c.onClose != nil {
			c.onClose()
		}
	})
}

// Read returns the next message. A close frame from the peer is echoed and
// returned as websocket.CloseError. A stream that ends without a close frame
// returns io.ErrUnexpectedEOF.
func (c *WSConn) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	stop := context.AfterFunc(ctx, c.shutdown)
	defer stop()
	var hdr [5]byte
	if _, err := io.ReadFull(c.rw, hdr[:]); err != nil {
		c.shutdown()
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		return 0, nil, io.ErrUnexpectedEOF
	}
	n := int64(binary.BigEndian.Uint32(hdr[1:]))
	if n > c.rlim.Load() {
		_ = c.Close(websocket.StatusMessageTooBig, "message too big")
		return 0, nil, errors.New("tunnel: websocket message too big")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(c.rw, buf); err != nil {
		c.shutdown()
		return 0, nil, io.ErrUnexpectedEOF
	}
	switch hdr[0] {
	case wsFrameText:
		return websocket.MessageText, buf, nil
	case wsFrameBinary:
		return websocket.MessageBinary, buf, nil
	case wsFrameClose:
		ce := websocket.CloseError{Code: websocket.StatusNoStatusRcvd}
		if len(buf) >= 2 {
			ce.Code = websocket.StatusCode(binary.BigEndian.Uint16(buf))
			ce.Reason = string(buf[2:])
		}
		_ = c.writeClose(ce.Code, ce.Reason)
		c.shutdown()
		return 0, nil, ce
	default:
		c.shutdown()
		return 0, nil, ErrBadHeader
	}
}

func (c *WSConn) writeFrame(t byte, payload []byte) error {
	buf := make([]byte, 5+len(payload))
	buf[0] = t
	binary.BigEndian.PutUint32(buf[1:], uint32(len(payload)))
	copy(buf[5:], payload)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closeSent {
		return errWSClosed
	}
	_, err := c.rw.Write(buf)
	return err
}

var errWSClosed = errors.New("tunnel: websocket closed")

// Write sends one message.
func (c *WSConn) Write(ctx context.Context, typ websocket.MessageType, p []byte) error {
	stop := context.AfterFunc(ctx, c.shutdown)
	defer stop()
	t := wsFrameBinary
	if typ == websocket.MessageText {
		t = wsFrameText
	}
	err := c.writeFrame(t, p)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (c *WSConn) writeClose(code websocket.StatusCode, reason string) error {
	if len(reason) > 123 {
		reason = reason[:123]
	}
	payload := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(payload, uint16(code))
	copy(payload[2:], reason)
	buf := make([]byte, 5+len(payload))
	buf[0] = wsFrameClose
	binary.BigEndian.PutUint32(buf[1:], uint32(len(payload)))
	copy(buf[5:], payload)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closeSent {
		return nil
	}
	c.closeSent = true
	_, err := c.rw.Write(buf)
	return err
}

// Close sends a close frame with code and reason and releases the stream.
func (c *WSConn) Close(code websocket.StatusCode, reason string) error {
	err := c.writeClose(code, reason)
	c.shutdown()
	return err
}

// CloseNow drops the stream without a close handshake.
func (c *WSConn) CloseNow() error {
	c.shutdown()
	return nil
}
