//go:build windows

package startup

import "os"

// ownedByThisUser: FileInfo can't tell the owner on Windows, where the data folder is under the
// user's own profile; a sweep there keeps everything (unsure means keep).
func ownedByThisUser(os.FileInfo) bool { return false }
