package startup

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/subgentic/coral/internal/secfile/secfiletest"
	"github.com/subgentic/coral/internal/testleak"
)

// TestMain fails the package when its tests leave a goroutine running (#4167), and runs every test
// isolated from the machine (#4381): the startup path these tests run sweeps the temp folder and
// writes under HOME and the data folder, so TMPDIR, HOME and CORAL_DATA_DIR point at a folder of
// this run's own, and the package fails if a sweep ever targets the real system temp folder.
func TestMain(m *testing.M) {
	real := resolvedDir(os.TempDir())
	// The isolated folder is made inside one private to this user (Windows): a Codex sandbox adds an entry of its own to the real
	// %TEMP%, everything made under it inherits that, and the private-file checks refuse what it holds (#83, #28).
	cleanupPrivate := secfiletest.UsePrivateTemp()
	iso, err := os.MkdirTemp("", "startup-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "startup tests: no isolated folder:", err)
		os.Exit(1)
	}
	// TMP, TEMP and USERPROFILE too: os.TempDir and the home folder read those on Windows, not
	// TMPDIR and HOME (Security's SHOULD on #4381 v2).
	for k, v := range map[string]string{"TMPDIR": iso, "TMP": iso, "TEMP": iso, "HOME": filepath.Join(iso, "home"), "USERPROFILE": filepath.Join(iso, "home"), "CORAL_DATA_DIR": filepath.Join(iso, "data")} {
		_ = os.MkdirAll(v, 0o700)
		os.Setenv(k, v)
	}
	var mu sync.Mutex
	var hit []string
	launchSweepGuard = func(dir string) {
		if resolvedDir(dir) == real {
			mu.Lock()
			hit = append(hit, dir)
			mu.Unlock()
		}
	}
	code := testleak.Check(m.Run())
	_ = os.RemoveAll(iso)
	cleanupPrivate()
	if len(hit) > 0 {
		fmt.Fprintf(os.Stderr, "startup tests: the launch temp sweep targeted the real temp folder %v (#4381): tests must run isolated\n", hit)
		code = 1
	}
	os.Exit(code)
}

// resolvedDir is dir cleaned and with symlinks resolved (macOS's /var is /private/var).
func resolvedDir(dir string) string {
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Clean(r)
	}
	return filepath.Clean(dir)
}
