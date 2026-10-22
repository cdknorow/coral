//go:build windows

package background

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

var (
	shellOnce sync.Once
	shellPath string
)

// workflowShell resolves the shell once, to an absolute path: Git for Windows' usr\bin\sh.exe (found from
// git on PATH or its usual install folders), else an sh on PATH. A bare "sh" would run whatever the
// working folder or a PATH entry put first.
func workflowShell() (string, error) {
	shellOnce.Do(func() { shellPath = findWorkflowShell() })
	if shellPath == "" {
		return "", errNoWorkflowShell
	}
	return shellPath, nil
}

// gitInstallRoot is the Git for Windows folder a git.exe on PATH belongs to, from the layouts Git ships and nothing
// further up: <root>\cmd\git.exe and <root>\bin\git.exe (one up), <root>\mingw64\bin\git.exe (two up). Any other
// folder is no Git install, and neither is a drive root: C:\ is writable by every signed-in user, who could put a
// usr\bin\sh.exe there for Coral to run as the person.
func gitInstallRoot(gitExe string) string {
	dir := filepath.Dir(gitExe)
	var root string
	switch {
	case strings.EqualFold(filepath.Base(dir), "bin") && strings.EqualFold(filepath.Base(filepath.Dir(dir)), "mingw64"):
		root = filepath.Dir(filepath.Dir(dir))
	case strings.EqualFold(filepath.Base(dir), "cmd"), strings.EqualFold(filepath.Base(dir), "bin"):
		root = filepath.Dir(dir)
	default:
		return ""
	}
	if filepath.Dir(root) == root {
		return ""
	}
	return root
}

func findWorkflowShell() string {
	var roots []string
	if git, err := exec.LookPath("git"); err == nil {
		if root := gitInstallRoot(git); root != "" {
			roots = append(roots, root)
		}
	}
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432"} {
		if v := os.Getenv(env); v != "" {
			roots = append(roots, filepath.Join(v, "Git"))
		}
	}
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		roots = append(roots, filepath.Join(v, "Programs", "Git"))
	}
	for _, root := range roots {
		p := filepath.Join(root, "usr", "bin", "sh.exe")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath("sh"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	return ""
}
