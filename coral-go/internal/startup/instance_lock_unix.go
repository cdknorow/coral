//go:build !windows

package startup

import (
	"fmt"
	"os"
	"syscall"
)

type instanceLock struct {
	file *os.File
}

func acquireInstanceLock(path string) (*instanceLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return nil, fmt.Errorf("another Coral server is already running for this data directory")
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
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	_ = l.file.Close()
	_ = os.Remove(path)
}
