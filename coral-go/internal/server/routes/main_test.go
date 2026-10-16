package routes

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/tmux"
)

func TestMain(m *testing.M) {
	// The terminal tests drive shells, which need no 300ms between a command
	// and its Enter (production keeps it for agent TUIs).
	tmux.SetSendKeysSettleForTest(20 * time.Millisecond)
	// The tests' tmux servers start their panes with $SHELL. A developer's interactive shell and its
	// rc files make typed commands wait on an unknown startup; a plain sh has none.
	os.Setenv("SHELL", "/bin/sh")
	// A team's later agent starts are typed after their slot (#3613); the spacer is process-wide,
	// so tests reusing a board name within seconds would see a start deferred. Here a deferred
	// start is typed at once; a test about the spacing replaces afterTeamSlot itself.
	afterTeamSlot = func(_ time.Duration, f func()) { f() }
	before := packageArtifactFiles()
	code := m.Run()
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
	os.Exit(code)
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
