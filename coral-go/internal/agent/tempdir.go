package agent

import (
	"fmt"
	"os"
	"sync"

	"github.com/subgentic/coral/internal/secfile"
)

// launchTempDirPath is where a launch's prompt, settings and instructions files go (#4381,
// Security's #4371 SHOULD 1): <data dir>/tmp, private to the user, once startup has called
// SetTempDir; until then (tests, tools) the system temp folder. In the shared temp folder they
// outlived crashes and upgrades by the hundred.
var (
	launchTempDirMu   sync.RWMutex
	launchTempDirPath string
)

// SetTempDir makes dir (created if needed, mode 0700, and on Windows an access list of this user and SYSTEM only)
// the folder launch temp files go to.
func SetTempDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("can't create the launch temp folder %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("can't make the launch temp folder %s private: %w", dir, err)
	}
	if err := secfile.ProtectDir(dir); err != nil { // Windows: a mode means nothing there; only this user and SYSTEM, inherited by the files in it
		return fmt.Errorf("can't make the launch temp folder %s private: %w", dir, err)
	}
	launchTempDirMu.Lock()
	launchTempDirPath = dir
	launchTempDirMu.Unlock()
	return nil
}

// ResetLaunchTempDirForTest puts launch temp files back in the system temp folder. Tests only: SetTempDir takes
// a real folder, so no setting can reach this.
func ResetLaunchTempDirForTest() {
	launchTempDirMu.Lock()
	launchTempDirPath = ""
	launchTempDirMu.Unlock()
}

// LaunchTempDir is the folder launch temp files go to.
func LaunchTempDir() string {
	launchTempDirMu.RLock()
	defer launchTempDirMu.RUnlock()
	if launchTempDirPath == "" {
		return os.TempDir()
	}
	return launchTempDirPath
}

// launchTempDirs are the folders a session's temp files may be in: LaunchTempDir, and the system
// temp folder, where launches before #4381 wrote them.
func launchTempDirs() []string {
	dirs := []string{LaunchTempDir()}
	if sys := os.TempDir(); sys != dirs[0] {
		dirs = append(dirs, sys)
	}
	return dirs
}
