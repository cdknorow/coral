//go:build !windows

package main

// processElevated is false off Windows: the MSI's launch is Windows-only.
func processElevated() bool { return false }
