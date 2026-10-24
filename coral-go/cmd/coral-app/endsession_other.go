//go:build webview && !windows

package main

// watchEndSession is Windows-only (endsession_windows.go).
func watchEndSession(terminate func()) {}
