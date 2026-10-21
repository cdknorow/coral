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

// privateLaunchTempDir is the folder SetTempDir made, or "" before it (or when it failed, and
// launch temp files go to the system temp folder).
func privateLaunchTempDir() string {
	launchTempDirMu.RLock()
	defer launchTempDirMu.RUnlock()
	return launchTempDirPath
}

// launchTemps are the temp files and folders this process wrote for each session's launches,
// by session id: a session's teardown removes them by their exact paths, wherever they are, so it
// never has to read the system temp folder (#90: on a long-used Windows machine that holds
// hundreds of thousands of entries, and a stop read them all).
var launchTemps = struct {
	sync.Mutex
	m map[string][]string
}{m: map[string][]string{}}

// rememberLaunchTemp records path as one of sessionID's launch temp files or folders. Only a
// canonical UUID's: CleanupTempFiles removes no other id's, so the list would only grow.
func rememberLaunchTemp(sessionID, path string) {
	if !isCanonicalUUID(sessionID) {
		return
	}
	launchTemps.Lock()
	launchTemps.m[sessionID] = append(launchTemps.m[sessionID], path)
	launchTemps.Unlock()
}

// takeLaunchTemps returns, and forgets, the paths recorded for sessionID.
func takeLaunchTemps(sessionID string) []string {
	launchTemps.Lock()
	defer launchTemps.Unlock()
	paths := launchTemps.m[sessionID]
	delete(launchTemps.m, sessionID)
	return paths
}
