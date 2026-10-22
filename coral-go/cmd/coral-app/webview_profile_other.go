//go:build webview && !windows

package main

// useLocalWebViewProfile is a Windows change: WebView2's profile folder is only a Windows question (macOS keeps the web view's data in the
// app's own Library folder).
func useLocalWebViewProfile() string { return "" }
