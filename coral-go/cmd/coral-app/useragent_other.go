//go:build webview && !windows && !darwin

package main

// markAppUserAgentBeforeWindow: WebView2 (Windows) takes the marker from an environment variable before its window exists; the other
// platforms mark the web view after it (markAppUserAgent), or not at all (WebKitGTK on Linux).
func markAppUserAgentBeforeWindow() bool { return false }
