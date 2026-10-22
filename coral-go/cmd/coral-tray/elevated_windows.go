//go:build windows

package main

import "golang.org/x/sys/windows"

// processElevated reports whether this process runs with an elevated token (an administrator's full token, not the
// filtered one User Account Control gives it at a normal prompt). A failure to read the token counts as elevated: the
// installer's launch then starts nothing, which the person can do by hand, instead of Coral as administrator.
func processElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}
