//go:build !windows

package secfile

import (
	"os"
	"path/filepath"
)

// Writers names who besides its owner and root can change what is in the folder at path: its
// group or everyone, when the folder's mode lets them write, and the same through its parent when
// the parent's mode lets them write without the sticky bit (they could move the folder away and put
// another at its path). A folder this user owns is not reported: outside Windows that is the normal
// shape of an app bundle or an unpacked tarball.
func Writers(path string) ([]string, error) {
	clean := filepath.Clean(path)
	fi, err := os.Stat(clean)
	if err != nil {
		return nil, err
	}
	who := modeWriters(fi.Mode(), "")
	if parent := filepath.Dir(clean); parent != clean {
		if pi, err := os.Stat(parent); err == nil && pi.Mode()&os.ModeSticky == 0 {
			who = append(who, modeWriters(pi.Mode(), " (through the parent folder)")...)
		}
	}
	return who, nil
}

func modeWriters(mode os.FileMode, via string) []string {
	var who []string
	if mode.Perm()&0o020 != 0 {
		who = append(who, "its group"+via)
	}
	if mode.Perm()&0o002 != 0 {
		who = append(who, "everyone"+via)
	}
	return who
}
