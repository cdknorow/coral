//go:build windows

package cmdhandoff

import "os"

// ownedByCurrentUser: Windows' temp folder is per user and the scripts are protected to this user
// and SYSTEM (secfile.Protect), so a file there that matches the exact name is this user's.
func ownedByCurrentUser(os.FileInfo) bool { return true }
