//go:build windows

package startup

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"

	"github.com/subgentic/coral/internal/secfile"
)

type instanceLock struct {
	file *os.File
}

// lockOffsetHigh places the byte the instance lock holds: far past the pid the file records, so
// another process can still read that (a locked range can't be read by other handles).
const lockOffsetHigh = 0x40000000

// acquireInstanceLock takes a LockFileEx lock on the lock file, as Unix takes flock: the
// kernel releases it when the process ends, however it ends. The file itself was the lock
// before (created with O_EXCL), so a crash or a power cut left it behind and Coral refused
// to start until it was deleted by hand (#3906, sweep item 6). A leftover file is now just
// reused. It is opened through secfile.OpenNoFollow, so a link or junction planted at its name
// is refused instead of locked and truncated (#3920).
func acquireInstanceLock(path string) (*instanceLock, error) {
	f, err := secfile.OpenNoFollow(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	ol := &windows.Overlapped{OffsetHigh: lockOffsetHigh}
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if err != nil {
		f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, errAlreadyRunning
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	if err := f.Truncate(0); err == nil {
		_, _ = f.Seek(0, 0)
		_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
		_ = f.Sync()
	}
	return &instanceLock{file: f}, nil
}

func (l *instanceLock) close(path string) {
	if l == nil || l.file == nil {
		return
	}
	l.forgetAddress()
	ol := &windows.Overlapped{OffsetHigh: lockOffsetHigh}
	_ = windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, ol)
	_ = l.file.Close()
	// The file stays: the next server reuses it. OpenNoFollow shares delete, so removing it here
	// could unlink the file a server started meanwhile has locked, and a third could then lock a
	// new file of the same name.
}
