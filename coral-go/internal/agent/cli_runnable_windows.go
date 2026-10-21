//go:build windows

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CheckRunnable refuses a file Windows would not run as a program. exec.LookPath accepts any existing file whose name has an
// extension, so a configured "claude.txt" would pass; only the kinds PATHEXT names (.exe, .cmd, .bat, .com by default) run.
func CheckRunnable(path string) error {
	ext := strings.ToLower(filepath.Ext(path))
	exts := strings.Split(strings.ToLower(os.Getenv("PATHEXT")), ";")
	if os.Getenv("PATHEXT") == "" {
		exts = []string{".com", ".exe", ".bat", ".cmd"}
	}
	for _, e := range exts {
		if e != "" && e == ext {
			return nil
		}
	}
	return fmt.Errorf("%q is not a program Windows runs (%s)", filepath.Base(path), strings.Join(exts, " "))
}
