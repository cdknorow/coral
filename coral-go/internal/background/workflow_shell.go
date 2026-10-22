package background

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// errNoWorkflowShell is the step's error when no POSIX shell can be found (Windows without Git for Windows).
var errNoWorkflowShell = errors.New("workflow shell steps need Git for Windows (sh was not found; if Git was just installed, restart Coral)")

// withShellDirs puts the shell's own folders at the front of the PATH of what it runs, in Git Bash's order (mingw64\bin, usr\bin, then
// what was there): Git for Windows' sh finds grep, sed, cat and sleep in its usr\bin, which a standard install leaves off the Windows PATH
// (only Git\cmd is on it), and System32's find, sort and timeout must not shadow the POSIX ones a step means. A bare "sh" (Unix) adds nothing.
func withShellDirs(env []string, shell string) []string {
	if !filepath.IsAbs(shell) {
		return env
	}
	var dirs []string
	if root := filepath.Dir(filepath.Dir(filepath.Dir(shell))); strings.EqualFold(filepath.Base(filepath.Dir(shell)), "bin") {
		if st, err := os.Stat(filepath.Join(root, "mingw64", "bin")); err == nil && st.IsDir() {
			dirs = append(dirs, filepath.Join(root, "mingw64", "bin"))
		}
	}
	dirs = append(dirs, filepath.Dir(shell))
	path := ""
	for _, kv := range env {
		if len(kv) > 5 && strings.EqualFold(kv[:5], "PATH=") {
			path = kv[5:]
		}
	}
	front := strings.Join(dirs, string(os.PathListSeparator))
	if path != "" {
		front += string(os.PathListSeparator) + path
	}
	return append(env[:len(env):len(env)], "PATH="+front)
}
