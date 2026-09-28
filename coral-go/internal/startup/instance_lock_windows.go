//go:build windows

package startup

import (
	"fmt"
	"os"
)

type instanceLock struct {
	file *os.File
}

func acquireInstanceLock(path string) (*instanceLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0644)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("another Coral server is already running for this data directory")
		}
		return nil, err
	}
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	return &instanceLock{file: f}, nil
}

func (l *instanceLock) close(path string) {
	if l == nil || l.file == nil {
		return
	}
	_ = l.file.Close()
	_ = os.Remove(path)
}
