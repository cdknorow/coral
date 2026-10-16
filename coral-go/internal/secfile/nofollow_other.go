//go:build !windows

package secfile

import (
	"errors"
	"os"
	"syscall"
)

// OpenNoFollow is os.OpenFile that never goes through a symbolic link at the path (O_NOFOLLOW:
// a link there makes the open fail with ErrLink), never opens a folder, and for a write refuses
// a file with a second name (a hard link to a file elsewhere) or anything that isn't a regular
// file (a FIFO or a device). O_TRUNC is applied only after those checks. The open itself is
// non-blocking, so a FIFO planted at the path can't hang it (#3952, Security's #3920 note);
// the file is then switched back to blocking unless the caller asked for O_NONBLOCK. See the
// Windows version for the folders an agent can write.
func OpenNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	f, err := os.OpenFile(path, (flag&^os.O_TRUNC)|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, perm)
	if err != nil {
		// ENXIO: a FIFO (or device) with nobody reading, opened to write without blocking.
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EISDIR) || errors.Is(err, syscall.ENXIO) {
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
	if fi.IsDir() || (write && (!fi.Mode().IsRegular() || links(fi) != 1)) {
		f.Close()
		return nil, &os.PathError{Op: "open", Path: path, Err: ErrLink}
	}
	if flag&syscall.O_NONBLOCK == 0 {
		if err := syscall.SetNonblock(int(f.Fd()), false); err != nil {
			f.Close()
			return nil, err
		}
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
