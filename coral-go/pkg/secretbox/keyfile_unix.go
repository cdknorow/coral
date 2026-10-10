//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package secretbox

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

func readExistingKeyFile(name string) ([]byte, error) {
	// O_NOFOLLOW makes the path check and open one operation, so a concurrent
	// replacement cannot turn a checked regular file into a link.
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat remote secret key file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: must be a regular file", ErrKeyPermissions)
	}
	if info.Mode().Perm() != 0600 {
		return nil, fmt.Errorf("%w: permissions must be 0600 (got %o)", ErrKeyPermissions, info.Mode().Perm())
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && uint32(stat.Uid) != uint32(os.Getuid()) {
		return nil, fmt.Errorf("%w: must be owned by the current user", ErrKeyPermissions)
	}
	data, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return nil, fmt.Errorf("read remote secret key file: %w", err)
	}
	return data, nil
}
