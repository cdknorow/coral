//go:build !windows

package ptymanager

import (
	"io"
	"os/exec"
	"syscall"
)

// startGuardianProcess runs exe in guardian mode: in a session of its own (a signal to Coral's group or a closed terminal does not reach it), with only the marker in its environment, no output,
// and a pipe from us as its stdin.
func startGuardianProcess(exe string) (*exec.Cmd, io.WriteCloser, error) {
	c := exec.Command(exe, guardianArg)
	c.Env = []string{guardianEnv + "=1"}
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	in, err := c.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := c.Start(); err != nil {
		return nil, nil, err
	}
	return c, in, nil
}
