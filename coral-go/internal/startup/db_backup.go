package startup

import (
	"os"

	"github.com/subgentic/coral/internal/secfile"
)

// newPrivateBackup creates path empty and readable by this user only (mode 0600; a protected access list on Windows), for VACUUM INTO to
// fill: SQLite writes into an existing empty file and leaves its mode alone, while a file it creates itself gets the process's umask (0644
// in most shells), so a backup of every session's text would be readable by other users of the computer wherever the data folder is open
// to them. It fails if path exists, as VACUUM INTO would.
func newPrivateBackup(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return err
	}
	if err := secfile.Protect(path); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}
