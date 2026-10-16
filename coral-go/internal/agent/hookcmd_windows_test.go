//go:build windows

package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// cmdExe runs a hook command line through cmd.exe exactly as written (Go's own argument
// quoting would escape its quotes in a way cmd.exe doesn't read).
func cmdExe(line string) *exec.Cmd {
	c := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"))
	c.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /d /s /c "` + line + `"`}
	return c
}
