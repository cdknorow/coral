//go:build unix

package main

import (
	"fmt"
	"os"
	"syscall"
)

func checkOwner(path string, fi os.FileInfo, uid int) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s: the owner could not be read: refused", path)
	}
	if int(st.Uid) != uid {
		return fmt.Errorf("%s is not owned by this user: refused", path)
	}
	return nil
}

func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}
