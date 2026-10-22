package agent

import (
	"github.com/subgentic/coral/internal/logsafe"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// staleTempFileName is a launch temp file's name, exactly: coral_<kind>_<canonical uuid>_<the
// random digits os.CreateTemp adds>.<ext>, for the kinds and extensions writeTempFile is given.
var staleTempFileName = regexp.MustCompile(`^coral_(prompt|settings|agy_prompt|codex_prompt|codex_instructions|pi_prompt|pi_sys)_([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})_[0-9]+\.(txt|json|md)$`)

// TempSweep says which launch temp files SweepStaleTempFiles may remove.
type TempSweep struct {
	// MinAge: only files last written longer ago than this.
	MinAge time.Duration
	// Live reports whether a session id is a live session in this Coral's database.
	Live func(sessionID string) bool
	// Ended, when set, must also report true for a file to go: the session is one this Coral's
	// database has, and it has ended. For the shared temp folder, where other Corals (a test
	// server, another data folder) keep files of sessions this one never heard of: those are
	// never removed.
	Ended func(sessionID string) bool
	// Running returns the command lines of every running process (one per line), read once per
	// sweep: a file whose session id appears in one (as --session-id, or in its --settings path)
	// belongs to a running agent, of this Coral or another one sharing the temp folder (a test
	// server, an older install). An error stops the sweep.
	Running func() (string, error)
	// Now is the clock (tests).
	Now func() time.Time
}

// SweepStaleTempFiles removes, from dir, the launch temp files left by agents that are gone
// (#4381, Security's #4371 SHOULD 1): crashes and upgrades left hundreds in the shared temp folder.
// A file goes only when ALL hold: its name is exactly a launch temp file's (staleTempFileName); it
// is a regular file (never a symlink or folder) owned by this user; it is older than MinAge; its
// session is not live in this Coral's database (and, with Ended, is one it has seen end); and no
// running process names its session id (in its path or on its own). Each is removed by its exact path, never by a pattern. Anything unsure keeps the file:
// the process list can't be read, a session check is missing, the owner can't be told. It returns
// the paths it removed.
func SweepStaleTempFiles(dir string, opt TempSweep) []string {
	if opt.Live == nil || opt.Running == nil || opt.MinAge <= 0 {
		return nil
	}
	now := time.Now
	if opt.Now != nil {
		now = opt.Now
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type candidate struct{ path, sessionID string }
	var cands []candidate
	for _, e := range entries {
		m := staleTempFileName.FindStringSubmatch(e.Name())
		if m == nil || !isCanonicalUUID(m[2]) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || !ownedByCurrentUser(info) || now().Sub(info.ModTime()) <= opt.MinAge {
			continue
		}
		cands = append(cands, candidate{path, m[2]})
	}
	if len(cands) == 0 {
		return nil
	}
	procs, err := opt.Running()
	if err != nil || strings.TrimSpace(procs) == "" {
		slog.Warn("launch temp file sweep skipped: the process list could not be read", "dir", logsafe.Dir(dir), "error", err)
		return nil
	}
	var removed []string
	for _, c := range cands {
		// A file's name carries its session id, so a process naming the file names the id too.
		if opt.Live(c.sessionID) || strings.Contains(procs, c.sessionID) || (opt.Ended != nil && !opt.Ended(c.sessionID)) {
			continue
		}
		// Checked again just before removing it: the same file, still a regular one of ours.
		info, err := os.Lstat(c.path)
		if err != nil || !info.Mode().IsRegular() || !ownedByCurrentUser(info) {
			continue
		}
		if err := os.Remove(c.path); err != nil {
			slog.Debug("launch temp file sweep: could not remove", "path", logsafe.Dir(c.path), "error", err)
			continue
		}
		removed = append(removed, c.path)
	}
	return removed
}
