package ptymanager

import "os"

// oNonBlockLog: no FIFOs to wait on here (a link at the path is refused by secfile.OpenNoFollow).
const oNonBlockLog = 0

// ownedByMe: Windows's temp dir is per user.
func ownedByMe(os.FileInfo) bool { return true }
