package routes

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// stubCLI is a harmless stand-in for an agent CLI, for a test that launches or restarts an agent but must
// never run a real one: /usr/bin/true, or on Windows (no /usr/bin/true, and exec.LookPath only takes a file
// with a PATHEXT extension) a true.cmd that exits 0 (#28).
func stubCLI(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		return "/usr/bin/true"
	}
	p := filepath.Join(t.TempDir(), "true.cmd")
	if err := os.WriteFile(p, []byte("@exit /b 0\r\n"), 0o755); err != nil {
		t.Fatalf("write the stub CLI: %v", err)
	}
	return p
}

// shortTempBase is where a test makes a folder whose path must stay short (a Unix socket's path is limited):
// /tmp on Unix, the system temp folder on Windows, which has no /tmp (#28).
func shortTempBase() string {
	if runtime.GOOS == "windows" {
		return os.TempDir()
	}
	return "/tmp"
}

// stubScript writes a stand-in program at path and returns the path to use: the given #!/bin/sh script
// on Unix, and on Windows (which runs no sh script and finds a program only with a PATHEXT extension)
// path+".cmd", which exits 0 (#28).
func stubScript(t *testing.T, path, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		path += ".cmd"
		script = "@exit /b 0\r\n"
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write the stub program: %v", err)
	}
	return path
}
