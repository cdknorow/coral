package background

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"nhooyr.io/websocket"
)

// pingConn is a feed socket whose pings fail as the test says.
type pingConn struct {
	fails  func(n int) bool // whether ping number n (from 1) fails
	pings  atomic.Int32
	closed atomic.Bool
}

func (c *pingConn) Ping(context.Context) error {
	if c.fails(int(c.pings.Add(1))) {
		return errors.New("ping timed out")
	}
	return nil
}
func (c *pingConn) Read(context.Context) (websocket.MessageType, []byte, error) { return 0, nil, nil }
func (c *pingConn) Write(context.Context, websocket.MessageType, []byte) error  { return nil }
func (c *pingConn) Close(websocket.StatusCode, string) error                    { return nil }
func (c *pingConn) CloseNow() error                                             { c.closed.Store(true); return nil }
func (c *pingConn) Subprotocol() string                                         { return "" }
func (c *pingConn) SetReadLimit(int64)                                          {}

// #3649: one slow ping over a port forward doesn't drop the feed; two in a row do.
func TestFeedPingToleratesOneMissedPing(t *testing.T) {
	h := NewRemoteAgentHub(newFakeRegistry(), nil, RemoteAgentHubConfig{FeedPingInterval: 10 * time.Millisecond, RequestTimeout: 50 * time.Millisecond})
	run := func(fails func(int) bool, d time.Duration) *pingConn {
		c := &pingConn{fails: fails}
		ctx, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()
		h.pingFeed(ctx, c, c)
		return c
	}

	// Every other ping fails: never two in a row, so the feed stays.
	c := run(func(n int) bool { return n%2 == 0 }, 200*time.Millisecond)
	assert.False(t, c.closed.Load(), "a single missed ping is tolerated")
	assert.Greater(t, c.pings.Load(), int32(4))

	// Two in a row: dropped at the second.
	c = run(func(n int) bool { return n >= 3 }, 2*time.Second)
	assert.True(t, c.closed.Load(), "two missed pings in a row drop the feed")
	assert.Equal(t, int32(4), c.pings.Load(), "dropped at the second miss, no sooner")
}
