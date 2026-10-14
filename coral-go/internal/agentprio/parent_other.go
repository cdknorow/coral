//go:build !windows && !linux && !darwin

package agentprio

import (
	"os/exec"
	"strconv"
	"strings"
)

// parentOf returns pid's current parent, from ps.
func parentOf(pid int) (int, bool) {
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, false
	}
	ppid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	return ppid, err == nil
}
