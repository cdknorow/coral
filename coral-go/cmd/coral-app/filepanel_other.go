//go:build webview && !darwin

package main

import "unsafe"

// installFilePanel: WebView2 (Windows) and WebKitGTK (Linux) show the file chooser of <input type="file"> themselves.
func installFilePanel(win unsafe.Pointer) {}
