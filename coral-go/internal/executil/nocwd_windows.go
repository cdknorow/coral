//go:build windows

package executil

import (
	"os"
	"os/exec"
	"strings"
)

// NoCwdSearchEnv is the variable that makes cmd.exe (and the programs that start commands the way it does) stop looking in the current folder for a program. Without it, a Claude
// Code or npm shim (claude.cmd, which runs `node`) started with a repository as its working folder runs a planted node.exe or node.cmd of that repository in place of the real one
// (#25 G1B for the agents themselves; #183 for every other place Coral starts a CLI itself).
const NoCwdSearchEnv = "NoDefaultCurrentDirectoryInExePath=1"

// WithNoCwdSearch is env with NoCwdSearchEnv set (once, replacing any other value of the name, whatever its case).
func WithNoCwdSearch(env []string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); strings.EqualFold(name, "NoDefaultCurrentDirectoryInExePath") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, NoCwdSearchEnv)
}

// NoCwdSearch sets NoCwdSearchEnv in cmd's environment (cmd.Env, or this process's when it has none). Call it after cmd.Env is set and before the command runs.
func NoCwdSearch(cmd *exec.Cmd) {
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = WithNoCwdSearch(env)
}
