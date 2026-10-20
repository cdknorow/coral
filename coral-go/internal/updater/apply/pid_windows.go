//go:build windows

package apply

// The apply is built for macOS first; on Windows nothing is running to wait for as far as this package knows.
func pidAlive(int) bool { return false }
