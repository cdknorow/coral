package routes

import (
	"fmt"
	"github.com/subgentic/coral/internal/secfile/secfiletest"
	"github.com/subgentic/coral/internal/testleak"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/subgentic/coral/internal/tmux"
)

func TestMain(m *testing.M) {
	// The terminal tests drive shells, which need no 300ms between a command
	// and its Enter (production keeps it for agent TUIs).
	tmux.SetSendKeysSettleForTest(20 * time.Millisecond)
	// The tests' tmux servers start their panes with $SHELL. A developer's interactive shell and its
	// rc files make typed commands wait on an unknown startup; a plain sh has none.
	os.Setenv("SHELL", "/bin/sh")
	// CI sets CORAL_TELEMETRY_DISABLED=1 for every job (the telemetry census requires it), and with it set the analytics tests
	// (launch, lifecycle, prerequisite) see no events: they check what is sent, to a local sink. The tracking package's TestMain
	// does the same; the tests that check the variable set it themselves (#4997).
	os.Unsetenv("CORAL_TELEMETRY_DISABLED")
	// A team's later agent starts are typed after their slot (#3613); the spacer is process-wide,
	// so tests reusing a board name within seconds would see a start deferred. Here a deferred
	// start is typed at once; a test about the spacing replaces afterTeamSlot itself.
	afterTeamSlot = func(_ time.Duration, f func()) { f() }
	// The package's temp files go to a folder of its own, private to this user, not the developer's %TEMP% (Windows): agent.CleanupTempFiles
	// (every kill) globs the system temp folder, and on a machine whose %TEMP% holds hundreds of thousands of files that was a sixth of the
	// package's time (#83); and what is in %TEMP% (a foreign ACL entry added by a Codex sandbox, leftovers) can't reach the tests (#28).
	cleanupTemp := secfiletest.UsePrivateTemp()
	before := packageArtifactFiles()
	code := m.Run()
	cleanupTemp()
	// A test whose Config has no data folder writes artifacts and diffs under "artifacts" here,
	// in the package folder, shared by every test in the run (#3566: the hourly upload cap then
	// failed under -count). Such a test must use its own folder (cfg.UseCoralDir or config.Load).
	var added []string
	for p := range packageArtifactFiles() {
		if !before[p] {
			added = append(added, p)
		}
	}
	if len(added) > 0 {
		fmt.Fprintf(os.Stderr, "FAIL: tests wrote %d file(s) under internal/server/routes/artifacts (a Config without a data folder; give it cfg.UseCoralDir(t.TempDir())), e.g. %s\n", len(added), added[0])
		if len(before) == 0 {
			_ = os.RemoveAll("artifacts") // it held nothing before this run
		} else {
			for _, p := range added {
				_ = os.Remove(p)
			}
		}
		if code == 0 {
			code = 1
		}
	}
	os.Exit(testleak.Check(code)) // no goroutine left running (#4167)
}

// packageArtifactFiles lists the files under ./artifacts (the package folder), if any.
func packageArtifactFiles() map[string]bool {
	out := map[string]bool{}
	_ = filepath.WalkDir("artifacts", func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out[p] = true
		}
		return nil
	})
	return out
}
