//go:build !windows

package tunnel

import (
	"errors"
	"syscall"
)

// connRefused: the local server refused the connection (its accept queue was full, #4011).
func connRefused(err error) bool { return errors.Is(err, syscall.ECONNREFUSED) }
