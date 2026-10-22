// Package windowmark remembers whether Coral's native window was open (#5827): a file in the data folder that exists from when the tray starts coral-app until the person closes
// the window. The tray leaves it when it ends the window itself (an update, Quit), so the update's relaunch can open the window again if the person had it open, and stay in the
// background if they had only the tray.
package windowmark

import (
	"os"
	"path/filepath"

	"github.com/subgentic/coral/internal/secfile"
)

// FileName is the marker's name in the data folder.
const FileName = "window_open"

// Set records that the window is open.
func Set(dataDir string) {
	if dataDir == "" {
		return
	}
	// Created without following a link (a symbolic link, a junction or a hard link at that name is refused, and its target is never truncated): the marker is a plain file or nothing (#183).
	f, err := secfile.OpenNoFollow(filepath.Join(dataDir, FileName), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err == nil {
		_ = f.Close()
	}
}

// Clear records that it is not.
func Clear(dataDir string) {
	if dataDir == "" {
		return
	}
	if fi, err := os.Lstat(filepath.Join(dataDir, FileName)); err == nil && fi.Mode().IsRegular() {
		_ = os.Remove(filepath.Join(dataDir, FileName))
	}
}

// Present is whether the window was open when it was last recorded: a plain file, never a link or a folder somebody put there.
func Present(dataDir string) bool {
	if dataDir == "" {
		return false
	}
	fi, err := os.Lstat(filepath.Join(dataDir, FileName))
	return err == nil && fi.Mode().IsRegular()
}
