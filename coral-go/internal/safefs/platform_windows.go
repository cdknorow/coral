//go:build windows

package safefs

import "os"

// Windows has no O_NOFOLLOW or FIFOs in the file system: the last name is checked with Lstat
// (a symlink or any other reparse point is refused) and the open handle with SameFile.
const (
	oNoFollow = 0
	oNonBlock = 0
)

// fileLinks: Windows's FileInfo doesn't carry a link count.
func fileLinks(os.FileInfo) uint64 { return 1 }
