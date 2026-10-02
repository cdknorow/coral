//go:build windows

package background

import "os/exec"

// Windows has no POSIX process groups or syscall.Kill. CommandContext and
// Process.Kill provide the platform-supported fail-closed termination path.
func configureChildProcess(_ *exec.Cmd) {}

func killChildProcess(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
