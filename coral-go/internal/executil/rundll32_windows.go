//go:build windows

package executil

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// systemRundll32 is rundll32.exe in the system folder, by its absolute path: the folder Windows reports (GetSystemDirectory), not what the
// SystemRoot variable of this process says; the variable and then the bare name are the last resorts (#5698 review).
func systemRundll32() string {
	if dir, err := windows.GetSystemDirectory(); err == nil && dir != "" {
		return filepath.Join(dir, "rundll32.exe")
	}
	if root := os.Getenv("SystemRoot"); root != "" {
		return filepath.Join(root, "System32", "rundll32.exe")
	}
	return "rundll32"
}
