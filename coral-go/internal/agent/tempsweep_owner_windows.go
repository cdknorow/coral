//go:build windows

package agent

import "os"

// ownedByCurrentUser: Windows gives each user a temp folder of their own, and the owner can't be
// read from FileInfo; the sweep keeps every file there (it only cleans up, so unsure means keep).
func ownedByCurrentUser(os.FileInfo) bool { return false }
