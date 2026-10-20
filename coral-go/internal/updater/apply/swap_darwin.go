//go:build darwin

package apply

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// swapPaths exchanges a and b atomically: there is no moment without an app at either name, so a power cut leaves one of the two installs.
func swapPaths(a, b string) error { return unix.RenamexNp(a, b, unix.RENAME_SWAP) }

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
	_ = filepath.Base(x)
	return unix.RenamexNp(x, y, unix.RENAME_SWAP) == nil
}
