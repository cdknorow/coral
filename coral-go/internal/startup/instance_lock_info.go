package startup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/subgentic/coral/internal/config"
)

// errAlreadyRunning: the data folder's instance lock is held, so another Coral server runs for it.
var errAlreadyRunning = errors.New("another Coral server is already running for this data directory")

// recordAddress writes the address this server listens on after its PID ("<pid>\n<url>\n"), once it listens, so a command finds
// the server of its data folder on whatever port it took (config.RecordedServer), and a second start on the same folder can say
// which server runs and where (#235). The lock itself is a byte far past this text, so the file stays readable while it is held.
func (l *instanceLock) recordAddress(url string) {
	if l == nil || l.file == nil {
		return
	}
	if err := l.file.Truncate(0); err != nil {
		return
	}
	if _, err := l.file.Seek(0, 0); err != nil {
		return
	}
	_, _ = fmt.Fprintf(l.file, "%d\n%s\n", os.Getpid(), url)
	_ = l.file.Sync()
}

// runningServer is the PID and address the server holding the lock recorded in its lock file (0 and "" when it can't be read).
func runningServer(lockPath string) (pid int, url string) {
	return config.RecordedServer(filepath.Dir(lockPath))
}

// forgetAddress empties the lock file before the lock is released, so a command looking for this data folder's server does not
// find the address of one that stopped (#235). Only the holder writes it, so a server started meanwhile is never touched.
func (l *instanceLock) forgetAddress() {
	if l == nil || l.file == nil {
		return
	}
	_ = l.file.Truncate(0)
}

// alreadyRunningError is the message for a second start on a data folder another server holds: which one, where, and what to do.
func alreadyRunningError(coralDir, lockPath string) error {
	pid, url := runningServer(lockPath)
	which := ""
	switch {
	case pid > 0 && url != "":
		which = fmt.Sprintf(" (PID %d, %s)", pid, url)
	case pid > 0:
		which = fmt.Sprintf(" (PID %d)", pid)
	}
	return fmt.Errorf("Coral is already running for the data folder %s%s. Open it with 'coral open', or stop it first "+
		"(Ctrl+C in its window, or Quit in the Coral tray), or start this one with another data folder (--home or CORAL_DATA_DIR)", coralDir, which)
}
