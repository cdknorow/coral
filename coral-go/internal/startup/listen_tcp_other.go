//go:build !windows

package startup

import "syscall"

// listenControl is nothing here: #224's exclusive port is a Windows change (SO_EXCLUSIVEADDRUSE has no Unix equivalent).
// Linux refuses a second listener on a port in use; BSD and macOS allow a more specific bind with SO_REUSEADDR, which is not
// addressed here.
func listenControl(_, _ string, _ syscall.RawConn) error { return nil }
