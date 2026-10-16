//go:build !windows

package background

import (
	"os/exec"
	"syscall"
	"time"

	"github.com/cdknorow/coral/internal/safego"
)

// configureChildProcess puts cmd in its own process group and makes a context
// cancel or timeout kill that whole group, not only the direct child: by default
// exec.CommandContext kills just the process it started, so `sh -c 'sleep 600 &'`
// left the sleep running after a step's timeout (audit HIGH-03).
func configureChildProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		killChildProcess(cmd)
		return nil
	}
}

// killChildProcess sends SIGTERM to cmd's process group, then SIGKILL after 5 s
// to whatever is left of it.
func killChildProcess(cmd *exec.Cmd) {
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		pgid = cmd.Process.Pid
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	go func() {
		defer safego.Recover("background/process_unix:31")
		time.Sleep(5 * time.Second)
		if err := syscall.Kill(-pgid, 0); err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
	}()
}

// trackChildProcess and releaseChildProcess are Windows' job object (process_windows.go); the
// process group set by configureChildProcess does their work here.
func trackChildProcess(*exec.Cmd)   {}
func releaseChildProcess(*exec.Cmd) {}
