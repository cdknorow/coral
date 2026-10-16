//go:build !windows

package secfile

import (
	"os"
	"syscall"
)

func fcntlFlags(f *os.File) (int, error) {
	raw, err := f.SyscallConn()
	if err != nil {
		return 0, err
	}
	var fl int
	var ferr error
	if err := raw.Control(func(fd uintptr) {
		r, _, e := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFL, 0)
		fl = int(r)
		if e != 0 {
			ferr = e
		}
	}); err != nil {
		return 0, err
	}
	return fl, ferr
}
