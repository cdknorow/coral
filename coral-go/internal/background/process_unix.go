//go:build !windows

package background

import (
	"os/exec"
	"syscall"
	"time"
)

func configureChildProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killChildProcess(cmd *exec.Cmd) {
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		pgid = cmd.Process.Pid
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	go func() {
		time.Sleep(5 * time.Second)
		if err := syscall.Kill(-pgid, 0); err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
	}()
}
