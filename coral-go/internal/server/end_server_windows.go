//go:build windows

package server

// endThisServer: Windows has no apply yet, so a relaunch never reaches it.
func endThisServer() {}
