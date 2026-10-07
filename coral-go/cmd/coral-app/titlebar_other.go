//go:build webview && !darwin && !windows

package main

// setupNativeTitlebar is a no-op on non-macOS platforms.
func setupNativeTitlebar() {}
