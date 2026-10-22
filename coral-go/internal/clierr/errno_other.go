//go:build !windows

package clierr

import "syscall"

func connRefused(e syscall.Errno) bool { return e == syscall.ECONNREFUSED }

func netUnreachable(e syscall.Errno) bool {
	return e == syscall.ENETUNREACH || e == syscall.EHOSTUNREACH || e == syscall.ENETDOWN
}
