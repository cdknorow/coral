// Package cmdhandoff removes the launch scripts a start of Coral can find left behind.
//
// A launch command too long for a fresh shell's input buffer is handed over as a private script
// (internal/tmux and internal/ptymanager, commandInvocation) that deletes itself when the shell
// sources it. One the shell never sourced (the pane died first) stays in the temp folder; this
// removes those a day later. A script holds a launch line, not message text (#4361), but the line
// names paths and flags.
package cmdhandoff

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// scriptName is the exact name a handoff script has: os.CreateTemp's random digits between the
// prefix and the extension (.sh on unix, .ps1 on Windows). Nothing else is ever removed.
var scriptName = regexp.MustCompile(`^coral-command-[0-9]+\.(sh|ps1)$`)

// MaxAge is how old a script must be before Sweep removes it.
const MaxAge = 24 * time.Hour

// Sweep removes this user's handoff scripts older than MaxAge from the per-user temp folder
// (os.TempDir), and returns how many it removed. It matches by exact name, looks at regular files
// only (never follows or removes a link or a folder), and, where the system says who owns a file,
// only removes the current user's: a shared temp folder is never globbed.
func Sweep() int { return sweepDir(os.TempDir(), time.Now(), MaxAge) }

// SweepIn is Sweep for dir: the launch temp folder, where Windows handoff scripts are made.
func SweepIn(dir string) int { return sweepDir(dir, time.Now(), MaxAge) }

func sweepDir(dir string, now time.Time, maxAge time.Duration) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		if !scriptName.MatchString(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || now.Sub(info.ModTime()) < maxAge || !ownedByCurrentUser(info) {
			continue
		}
		if err := os.Remove(path); err == nil {
			removed++
		} else {
			slog.Warn("could not remove an old launch script", "error", err)
		}
	}
	return removed
}
