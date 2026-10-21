//go:build !windows

package routes

// shortPathOf: only Windows has 8.3 names.
func shortPathOf(string) string { return "" }
