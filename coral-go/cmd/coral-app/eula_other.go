//go:build webview && !darwin && !windows

package main

import "github.com/cdknorow/coral/internal/license"

// showEULADialog falls back to terminal prompt on non-macOS platforms.
func showEULADialog(tosText string) bool {
	return license.TerminalEULADialog(tosText)
}
