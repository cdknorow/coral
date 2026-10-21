//go:build !windows

package secfile

import "os"

// Protect does nothing outside Windows: the file was created with mode 0600, which is the
// protection there, and the readers check the mode and the owner.
func Protect(string) error { return nil }

// ProtectDir does nothing outside Windows: the folder was made with mode 0700.
func ProtectDir(string) error { return nil }

// CheckOpen does nothing outside Windows: the callers check the mode and the owner of what they
// opened (see the Windows CheckOpen).
func CheckOpen(*os.File) error { return nil }
