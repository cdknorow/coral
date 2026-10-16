//go:build windows

package cliauth

import "os"

// privateMode: Windows has no permission bits (FileInfo reports 0666, or 0444 when read-only),
// so the mode can't say; ReadToken checks the file's access list instead (secfile.CheckOpen,
// #3905). Checking the bits refused every token, so a Windows command line could never sign
// (#3631).
func privateMode(os.FileInfo) bool { return true }

// ownedByMe: the owner is part of the access-list check on Windows (secfile.CheckOpen).
func ownedByMe(os.FileInfo) bool { return true }
