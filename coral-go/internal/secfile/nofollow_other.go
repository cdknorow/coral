//go:build !windows

package secfile

import (
	"errors"
	"os"
	"syscall"
)

// OpenNoFollow is os.OpenFile that never goes through a symbolic link at the path (O_NOFOLLOW:
// a link there makes the open fail with ErrLink), never opens a folder, and for a write refuses
// a file with a second name (a hard link to a file elsewhere). O_TRUNC is applied only after
// those checks. See the Windows version for the folders an agent can write.
func OpenNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	f, err := os.OpenFile(path, (flag&^os.O_TRUNC)|syscall.O_NOFOLLOW, perm)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EISDIR) {
			return nil, &os.PathError{Op: "open", Path: path, Err: ErrLink}
		}
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	write := flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_TRUNC) != 0
	if fi.IsDir() || (write && fi.Mode().IsRegular() && links(fi) != 1) {
		f.Close()
		return nil, &os.PathError{Op: "open", Path: path, Err: ErrLink}
	}
	if flag&os.O_TRUNC != 0 {
		if err := f.Truncate(0); err != nil {
			f.Close()
			return nil, err
		}
	}
	return f, nil
}

// links is the file's number of names; 1 when the platform doesn't say.
func links(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 1
}
