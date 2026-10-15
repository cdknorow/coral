package ptymanager

import "os"

// oNoFollowLog: Windows has no O_NOFOLLOW; the log folder is the user's own data folder.
const oNoFollowLog = 0

// ownedByMe: Windows's temp dir is per user.
func ownedByMe(os.FileInfo) bool { return true }
