//go:build windows

package clierr

import "syscall"

const refusedErrnoForTest syscall.Errno = 10061 // WSAECONNREFUSED
