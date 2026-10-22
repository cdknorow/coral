//go:build webview && !darwin

package main

import "unsafe"

// markAppUserAgent: only the macOS web view is marked after it is created; WebView2 is marked before (markAppUserAgentBeforeWindow).
func markAppUserAgent(win unsafe.Pointer) bool { return false }
