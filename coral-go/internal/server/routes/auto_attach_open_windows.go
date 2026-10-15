//go:build windows

package routes

import "os"

// attachOpenFlags: Windows has no O_NOFOLLOW; readAttachable compares the opened file
// with a fresh Lstat of the resolved path (os.SameFile), which a symlink fails.
const attachOpenFlags = 0

// linkCount: not available from os.FileInfo on Windows; the other bounds apply.
func linkCount(os.FileInfo) (uint64, bool) { return 0, false }
