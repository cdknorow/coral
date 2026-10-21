//go:build !windows

package agent

// shortPathName: 8.3 short names are Windows only.
func shortPathName(string) string { return "" }
