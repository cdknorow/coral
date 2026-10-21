//go:build !darwin

package main

// installReopenHandler is macOS's "the app was opened again while it runs"; nothing elsewhere.
func installReopenHandler(show func()) {}

// loginItemLaunch is whether macOS started the app at sign-in; false elsewhere.
func loginItemLaunch() bool { return false }
