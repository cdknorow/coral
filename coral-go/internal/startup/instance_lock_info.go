package startup

import (
	"fmt"
	"os"
)

// recordAddress writes the address this server listens on after its PID ("<pid>\n<url>\n"), once it listens, so a command finds
// the server of its data folder on whatever port it took (config.RecordedServer, #235). The lock itself is a byte far past this
// text, so the file stays readable by others while it is held.
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

// forgetAddress empties the lock file before the lock is released, so a command looking for this data folder's server does not
// find the address of one that stopped (#235). Only the holder writes it, so a server started meanwhile is never touched.
func (l *instanceLock) forgetAddress() {
	if l == nil || l.file == nil {
		return
	}
	_ = l.file.Truncate(0)
}
