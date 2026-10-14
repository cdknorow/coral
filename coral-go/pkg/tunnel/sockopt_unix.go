//go:build linux || darwin

package tunnel

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// LowLatencyControl is a net.Dialer Control function for the hub connection: it sets
// TCP_NOTSENT_LOWAT on TCP sockets. Best effort: an error setting it is ignored.
func LowLatencyControl(network, _ string, c syscall.RawConn) error {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil
	}
	_ = c.Control(func(fd uintptr) {
		_ = unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_NOTSENT_LOWAT, sendLowWater)
	})
	return nil
}
