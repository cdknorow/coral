//go:build windows

package background

import "os"

// oNoFollow: Windows has no O_NOFOLLOW; callers check with Lstat first.
const oNoFollow = 0

// oNonBlock: Windows has no FIFOs in the file system.
const oNonBlock = 0

// fileLinks: Windows's FileInfo doesn't carry a link count.
func fileLinks(os.FileInfo) uint64 { return 1 }
