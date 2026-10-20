//go:build windows

package apply

import (
	"errors"
	"os"
)

// The apply is built for macOS first; on Windows nothing is running to wait for as far as this package knows.
func pidAlive(int) bool { return false }

func ownerOf(os.FileInfo) (int, bool) { return 0, false }

func spawnDetached([]string, []string, string) error { return errors.New("not built for Windows") }
