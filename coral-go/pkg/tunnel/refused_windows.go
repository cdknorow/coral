//go:build windows

package tunnel

import (
	"errors"

	"golang.org/x/sys/windows"
)

// connRefused: the local server refused the connection (WSAECONNREFUSED: its accept queue was
// full, #4011). syscall's Errno doesn't match ECONNREFUSED on Windows, so the code is compared.
func connRefused(err error) bool { return errors.Is(err, windows.WSAECONNREFUSED) }
