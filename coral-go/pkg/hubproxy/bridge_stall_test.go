package hubproxy

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// stallConn is one side of the bridge: as the source it sends frames, as the destination it
// never accepts a write (a browser that stopped reading).
type stallConn struct{ frames bool }

func (c stallConn) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	if c.frames {
		return websocket.MessageBinary, []byte("output"), nil
	}
	<-ctx.Done()
	return 0, nil, ctx.Err()
}

func (stallConn) Write(ctx context.Context, _ websocket.MessageType, _ []byte) error {
	<-ctx.Done()
	return ctx.Err()
}

func (stallConn) Close(websocket.StatusCode, string) error { return nil }
func (stallConn) CloseNow() error                          { return nil }
func (stallConn) Subprotocol() string                      { return "" }
func (stallConn) SetReadLimit(int64)                       {}

// #4161: a browser that stops reading ends the hub's bridge after wsWriteTimeout, instead of
// holding it and the machine's stream until the tunnel ends.
func TestBridgeEndsWhenTheBrowserStopsReading(t *testing.T) {
	prev := wsWriteTimeout
	wsWriteTimeout = 100 * time.Millisecond
	t.Cleanup(func() { wsWriteTimeout = prev })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		pumpFrom(ctx, cancel, stallConn{frames: true}, stallConn{}, "lost", clientGate{}, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the bridge stayed up with a browser that never reads")
	}
}
