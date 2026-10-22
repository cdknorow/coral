//go:build windows

package startup

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// soExclusiveAddrUse is SO_EXCLUSIVEADDRUSE (~SO_REUSEADDR): no other socket may bind this port, on any address of this
// computer, while this one is open, not even with SO_REUSEADDR. Re-listening right after Coral closes still works (measured on
// Windows 11 with the old listener's connections in TIME_WAIT), so a restart is not held up.
const soExclusiveAddrUse = ^int(4)

// listenControl sets SO_EXCLUSIVEADDRUSE on the socket before it is bound.
func listenControl(_, _ string, c syscall.RawConn) error {
	var serr error
	if err := c.Control(func(fd uintptr) {
		serr = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, soExclusiveAddrUse, 1)
	}); err != nil {
		return err
	}
	return serr
}
