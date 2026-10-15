package ptymanager

import "os"

// oNoFollowLog: Windows has no O_NOFOLLOW; the log folder is the user's own data folder.
const oNoFollowLog = 0

// linkCount: Windows's FileInfo doesn't carry a link count.
func linkCount(os.FileInfo) uint64 { return 1 }

// ownedByMe: Windows's temp dir is per user.
func ownedByMe(os.FileInfo) bool { return true }
