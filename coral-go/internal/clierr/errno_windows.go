//go:build windows

package clierr

import "syscall"

// Winsock errors, which is what connect() reports on Windows (Go's syscall.ECONNREFUSED there is a made-up number, not 10061).
const (
	wsaeNetDown     syscall.Errno = 10050
	wsaeNetUnreach  syscall.Errno = 10051
	wsaeConnRefused syscall.Errno = 10061
	wsaeHostUnreach syscall.Errno = 10065
)

func connRefused(e syscall.Errno) bool {
	return e == wsaeConnRefused || e == syscall.ECONNREFUSED
}

func netUnreachable(e syscall.Errno) bool {
	return e == wsaeNetUnreach || e == wsaeHostUnreach || e == wsaeNetDown || e == syscall.ENETUNREACH || e == syscall.EHOSTUNREACH
}
