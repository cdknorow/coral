//go:build windows

package background

import (
	"os"
	"os/exec"
	"path/filepath"
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

func findWorkflowShell() string {
	var roots []string
	if git, err := exec.LookPath("git"); err == nil {
		// <root>\cmd\git.exe, <root>\bin\git.exe or <root>\mingw64\bin\git.exe
		dir := filepath.Dir(git)
		for i := 0; i < 3; i++ {
			roots = append(roots, dir)
			dir = filepath.Dir(dir)
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
