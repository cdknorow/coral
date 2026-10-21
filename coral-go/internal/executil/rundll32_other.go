//go:build !windows

package executil

// systemRundll32 names the Windows protocol handler; only browserCommand("windows", ...) reads it.
func systemRundll32() string { return "rundll32" }
