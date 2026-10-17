package tunnel

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// stallEnd is one side of a bridged WebSocket: it sends frames when it is the source, and as
// the destination it either never accepts a write (a viewer that stopped reading) or never
// sends anything.
type stallEnd struct{ frames bool }

func (e stallEnd) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	if e.frames {
		return websocket.MessageBinary, []byte("output"), nil
	}
	<-ctx.Done()
	return 0, nil, ctx.Err()
}

func (e stallEnd) Write(ctx context.Context, _ websocket.MessageType, _ []byte) error {
	<-ctx.Done() // never accepted
	return ctx.Err()
}

func (stallEnd) Close(websocket.StatusCode, string) error { return nil }

// #4161: when the hub side stops reading, the bridge ends after wsWriteTimeout instead of
// holding its goroutines and stream slot until the tunnel ends.
func TestPumpWSEndsWhenTheHubSideStopsReading(t *testing.T) {
	prev := wsWriteTimeout
	wsWriteTimeout = 100 * time.Millisecond
	t.Cleanup(func() { wsWriteTimeout = prev })

	done := make(chan struct{})
	go func() {
		pumpWS(context.Background(), stallEnd{frames: true}, stallEnd{}, nil, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the bridge stayed up with a side that never reads")
	}
}
