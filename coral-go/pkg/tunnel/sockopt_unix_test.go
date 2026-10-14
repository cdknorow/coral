//go:build linux || darwin

package tunnel

import (
	"context"
	"net"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// (5) The hub connection's socket keeps a short unsent queue (TCP_NOTSENT_LOWAT).
func TestLowLatencyControlSetsNotSentLowWater(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			c.Close()
		}
	}()
	c, err := (&net.Dialer{Control: LowLatencyControl}).DialContext(context.Background(), "tcp", ln.Addr().String())
	require.NoError(t, err)
	defer c.Close()
	raw, err := c.(*net.TCPConn).SyscallConn()
	require.NoError(t, err)
	var got int
	var gerr error
	require.NoError(t, raw.Control(func(fd uintptr) {
		got, gerr = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_NOTSENT_LOWAT)
	}))
	require.NoError(t, gerr)
	assert.Equal(t, sendLowWater, got)
}

// #3440 v2: the tunnel's OWN connection keeps the short send queue, whoever built the
// HTTP client (coral-hub's tunnelperf dials with its own, or none).
func TestTunnelDialSetsNotSentLowWaterOnItsConnection(t *testing.T) {
	for name, hc := range map[string]*http.Client{"no client": nil, "a plain transport": {Transport: &http.Transport{}}} {
		t.Run(name, func(t *testing.T) {
			conns := make(chan net.Conn, 4)
			onTunnelConn = func(c net.Conn) { conns <- c }
			defer func() { onTunnelConn = nil }()
			h := newHub(t, SessionConfig{})
			cs, err := Dial(context.Background(), h.URL(), testToken, ClientOptions{
				SessionConfig: h.Cfg, Handler: HandlerFunc(func(context.Context, *Stream) {}), AllowInsecure: true, HTTPClient: hc,
			})
			require.NoError(t, err)
			defer cs.Close()
			h.next(t)
			var c net.Conn
			select {
			case c = <-conns:
			default:
				t.Fatal("the tunnel's connection wasn't dialed through the low-latency dialer")
			}
			raw, err := c.(*net.TCPConn).SyscallConn()
			require.NoError(t, err)
			var got int
			var gerr error
			require.NoError(t, raw.Control(func(fd uintptr) {
				got, gerr = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_NOTSENT_LOWAT)
			}))
			require.NoError(t, gerr)
			assert.Equal(t, sendLowWater, got)
		})
	}
}
