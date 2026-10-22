//go:build windows

package routes

import "os/exec"

// detach: Windows has no apply yet, so nothing is detached.
func detach(*exec.Cmd) {}
