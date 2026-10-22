//go:build webview && !windows

package main

// setTitlebarColors is a no-op where the title bar is not drawn from the page's colours (macOS draws its own, transparent one).
func setTitlebarColors(bg, border string) {}
