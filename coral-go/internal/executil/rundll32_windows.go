//go:build windows

package executil

import (
	"os"
	"path/filepath"
)

// systemRundll32 is rundll32.exe in the system folder (%SystemRoot%\System32), by its absolute
// path; "rundll32" when SystemRoot is not set.
func systemRundll32() string {
	if root := os.Getenv("SystemRoot"); root != "" {
		return filepath.Join(root, "System32", "rundll32.exe")
	}
	return "rundll32"
}
