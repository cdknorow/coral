//go:build !windows

package startup

import (
	"fmt"
	"os"
	"syscall"

	"github.com/subgentic/coral/internal/secfile"
)

type instanceLock struct {
	file *os.File
}

func acquireInstanceLock(path string) (*instanceLock, error) {
	f, err := secfile.OpenNoFollow(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return nil, errAlreadyRunning
		}
		return nil, err
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
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	_ = l.file.Close()
	// The file stays, as on Windows: the next server reuses it (#4151). Removing it after the
	// unlock could unlink the file a server started meanwhile has locked, and a third server
	// could then lock a new file of the same name and run beside it.
}
