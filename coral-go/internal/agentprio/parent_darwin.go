//go:build darwin

package agentprio

import "golang.org/x/sys/unix"

// parentOf returns pid's current parent.
func parentOf(pid int) (int, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(kp.Proc.P_pid) != pid {
		return 0, false
	}
	return int(kp.Eproc.Ppid), true
}
