//go:build !windows && !linux && !darwin

package agentprio

import (
	"github.com/subgentic/coral/internal/executil"
	"strconv"
	"strings"
)

// parentOf returns pid's current parent, from ps.
func parentOf(pid int) (int, bool) {
	out, err := executil.Output(psTimeout, "ps", "-o", "ppid=", "-p", strconv.Itoa(pid))
	if err != nil {
		return 0, false
	}
	ppid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	return ppid, err == nil
}
