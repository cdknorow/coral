//go:build !windows

package routes

import (
	"os/exec"
	"syscall"
)

// detach makes the updater program a session of its own, so it keeps running when this server ends.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
