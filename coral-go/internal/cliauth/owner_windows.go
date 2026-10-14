//go:build windows

package cliauth

import "os"

// ownedByMe: the data folder's ACL is the protection on Windows.
func ownedByMe(os.FileInfo) bool { return true }
