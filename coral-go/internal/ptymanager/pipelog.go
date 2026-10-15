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
	root, err := safefs.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	return root.WriteFile(filepath.Base(path), nil, 0o600)
}

// createLog makes a missing log (the self-heal after it was deleted): only if nothing is there.
func createLog(path string) error {
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
