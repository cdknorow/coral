//go:build linux

package agentprio

import (
	"os"
	"strconv"
	"strings"
)

// parentOf returns pid's current parent from /proc/<pid>/stat (field 4, after
// the parenthesised command name, which may itself contain spaces).
func parentOf(pid int) (int, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(f[1])
	return ppid, err == nil
}
