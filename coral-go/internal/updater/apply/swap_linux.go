//go:build linux

package apply

import (
	"os"

	"golang.org/x/sys/unix"
)

// swapPaths exchanges two folders atomically: renameat2 with RENAME_EXCHANGE. There is no moment without a folder at either name.
func swapPaths(a, b string) error {
	return unix.Renameat2(unix.AT_FDCWD, a, unix.AT_FDCWD, b, unix.RENAME_EXCHANGE)
}

// swapSupported probes parent's volume with two throwaway folders.
func swapSupported(parent string) bool {
	x, err := os.MkdirTemp(parent, ".coral-update-swap-*")
	if err != nil {
		return false
	}
	defer os.RemoveAll(x)
	y, err := os.MkdirTemp(parent, ".coral-update-swap-*")
	if err != nil {
		return false
	}
	defer os.RemoveAll(y)
	return swapPaths(x, y) == nil
}
