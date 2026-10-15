package ptymanager

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/cdknorow/coral/internal/safefs"
)

// Agent pipe logs (#3601, weak site W17): everything an agent's terminal shows. Readable by this
// user only (0600, in the data folder's 0700 logs folder), and never written through a symlink
// or a hard link: a name planted in the folder is refused, not followed.

// ResetAgentLog creates an agent's pipe log, or empties an existing one (a session spawned or
// restarted again): 0600, never through a symlink or a hard link.
func ResetAgentLog(path string) error { return resetLog(path) }

// resetLog creates the log, or empties an existing one (a session spawned again).
func resetLog(path string) error {
	if path == "" {
		return errNoLogPath
	}
	root, err := safefs.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	return root.WriteFile(filepath.Base(path), nil, 0o600)
}

// createLog makes a missing log (the self-heal after it was deleted): only if nothing is there.
func createLog(path string) error {
	if path == "" {
		return errNoLogPath
	}
	root, err := safefs.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := root.CreateNew(filepath.Base(path), 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

// openLogAppend opens the log for the pty backend to append to: created 0600 if missing,
// never through a symlink, and a regular file with one name.
func openLogAppend(path string) (*os.File, error) {
	if path == "" {
		return nil, errNoLogPath
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND|oNoFollowLog, 0o600)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || linkCount(fi) > 1 {
		f.Close()
		return nil, errNotRegularLog
	}
	return f, nil
}

var errNotRegularLog = errors.New("the agent log is not a regular file with one name")

// errNoLogPath: no log path (naming.LogFile refuses an unsafe agent type or session id).
var errNoLogPath = errors.New("no agent log path")

// maxMigratedLog caps how much of an old log is carried over on upgrade: the end of it, which
// is what the terminal replay shows.
const maxMigratedLog = 16 << 20

// migrateTempLog moves a running agent's log from the shared temp dir, where installs before
// #3601 kept it (readable by other users), into newPath's folder: when newPath doesn't exist yet
// and the temp dir holds a log of the same name, its last maxMigratedLog bytes become the new
// log (0600, through the safe writer), and the old file is removed if it is still the one read.
// It reports whether a log was moved.
func migrateTempLog(newPath string) bool {
	if newPath == "" {
		return false
	}
	dir, name := filepath.Dir(newPath), filepath.Base(newPath)
	tmp := os.TempDir()
	if sameFolder(dir, tmp) {
		return false
	}
	if _, err := os.Lstat(newPath); err == nil {
		return false
	}
	old, err := safefs.OpenRoot(tmp)
	if err != nil {
		return false
	}
	defer old.Close()
	f, fi, err := old.Open(name)
	if err != nil {
		return false
	}
	start := fi.Size() - maxMigratedLog
	if start < 0 {
		start = 0
	}
	data := make([]byte, fi.Size()-start)
	n, _ := f.ReadAt(data, start)
	f.Close()
	if err := resetLog(newPath); err != nil {
		return false
	}
	root, err := safefs.OpenRoot(dir)
	if err != nil {
		return false
	}
	defer root.Close()
	if err := root.WriteFile(name, data[:n], 0o600); err != nil {
		return false
	}
	if at, err := old.Lstat(name); err == nil && os.SameFile(fi, at) {
		os.Remove(filepath.Join(tmp, name))
	}
	return true
}

func sameFolder(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}
