//go:build windows

package cliauth

import "os"

// privateMode: Windows has no permission bits (FileInfo reports 0666, or 0444 when read-only),
// so the mode can't say; the data folder's ACL is the protection, as for ownedByMe. Checking
// the bits refused every token, so a Windows command line could never sign (#3631).
func privateMode(os.FileInfo) bool { return true }

// ownedByMe: the data folder's ACL is the protection on Windows.
func ownedByMe(os.FileInfo) bool { return true }
