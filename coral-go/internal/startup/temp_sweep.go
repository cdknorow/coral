package startup

import (
	"context"
	"errors"
	"github.com/subgentic/coral/internal/logsafe"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/cmdhandoff"
	"github.com/subgentic/coral/internal/diskguard"
	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/store"
)

// staleTempAge is how old a launch temp file must be before the startup sweep may remove it.
const staleTempAge = 24 * time.Hour

// setupLaunchTempDir moves launch temp files to <data dir>/tmp (#4381) and, in the background,
// removes the stale ones left in it and in the system temp folder (agent.SweepStaleTempFiles has
// the rules: exact names, this user's regular files, a day old, no live session here, no running
// process naming them; in the shared temp folder, only sessions this Coral saw end). The channel
// closes when the sweep is over. It is the only reader of the system temp folder: an agent's stop
// removes the paths its launch recorded and its files in <data dir>/tmp (agent.CleanupTempFiles,
// #90), so the system temp files launches left before a restart (before #4381, or while
// <data dir>/tmp couldn't be made) are this sweep's, under the same guards.
func setupLaunchTempDir(db *store.DB, dataDir string) <-chan struct{} {
	diskguard.Use(dataDir) // the routes that can fill this volume refuse when it is nearly full (#4602)
	if err := agent.SetTempDir(filepath.Join(dataDir, "tmp")); err != nil {
		log.Printf("[startup] %v; launch temp files stay in %s", err, os.TempDir())
	}
	cmdhandoff.SetFallbackDir(agent.LaunchTempDir) // a start command's script goes here when the system temp folder is not usable (#6978)
	dirs := []string{agent.LaunchTempDir()}
	if sys := os.TempDir(); sys != dirs[0] {
		dirs = append(dirs, sys)
	}
	done := make(chan struct{})
	safego.Go("startup: launch temp file sweep", func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		live := map[string]bool{}
		sessions, err := store.NewSessionStore(db).GetAllLiveSessions(ctx)
		if err != nil {
			log.Printf("[startup] launch temp file sweep skipped: live sessions unreadable: %v", err)
			return
		}
		for _, s := range sessions {
			live[s.SessionID] = true
		}
		var endedIDs []string
		if err := db.Reader().SelectContext(ctx, &endedIDs, `SELECT session_id FROM live_sessions WHERE status != 'active'`); err != nil {
			log.Printf("[startup] launch temp file sweep skipped: ended sessions unreadable: %v", err)
			return
		}
		ended := make(map[string]bool, len(endedIDs))
		for _, id := range endedIDs {
			ended[id] = true
		}
		for _, dir := range dirs {
			if launchSweepGuard != nil {
				launchSweepGuard(dir)
			}
			opt := agent.TempSweep{
				MinAge:  staleTempAge,
				Live:    func(id string) bool { return live[id] },
				Running: func() (string, error) { return listRunningCommandLines(ctx) },
			}
			if dir == os.TempDir() {
				// The shared temp folder (also when <data dir>/tmp couldn't be made): only files
				// of sessions this Coral saw end.
				if len(ended) == 0 {
					continue
				}
				opt.Ended = func(id string) bool { return ended[id] }
			}
			if removed := agent.SweepStaleTempFiles(dir, opt); len(removed) > 0 {
				if dir == os.TempDir() {
					log.Printf("[startup] removed %d launch temp file(s) over a day old, of agents that ended, from the system temp folder %s (left before a restart; an agent's stop doesn't read this folder)", len(removed), logsafe.Path(dir))
				} else {
					log.Printf("[startup] removed %d launch temp file(s) over a day old, of agents no longer running, from %s", len(removed), logsafe.Path(dir))
				}
			}
		}
	})
	return done
}

// launchSweepGuard, when set (the package's tests), is told every folder the startup sweep is about
// to sweep, so the tests can fail if one is the machine's real temp folder.
var launchSweepGuard func(dir string)

// listRunningCommandLines is runningCommandLines, replaceable in tests.
var listRunningCommandLines = runningCommandLines

// runningCommandLines is every running process's command line, one per line (ps on macOS and
// Linux). Windows: an error, so the sweep keeps everything.
func runningCommandLines(ctx context.Context) (string, error) {
	var args []string
	switch runtime.GOOS {
	case "windows":
		return "", errors.New("no process list on Windows")
	case "linux":
		args = []string{"-e", "-ww", "-o", "args="} // procps
	default:
		args = []string{"-axww", "-o", "command="} // macOS and the BSDs
	}
	out, err := exec.CommandContext(ctx, "ps", args...).Output()
	return string(out), err
}
