//go:build windows

package gitutil

import (
	"os"
	"path/filepath"
	"sync"
)

var (
	noHooksOnce sync.Once
	noHooksDir  string
)

// noHooksPath is git's core.hooksPath: an empty folder in the user's own temp folder.
func noHooksPath() string {
	noHooksOnce.Do(func() {
		noHooksDir = filepath.Join(os.TempDir(), "coral-git-no-hooks")
		_ = os.MkdirAll(noHooksDir, 0o700)
	})
	return noHooksDir
}
