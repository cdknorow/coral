// Package logrotate is a log file that rotates by size (#3628, at-rest audit F5): coral.log
// grew without bound (457 MB on the operator's computer), and it records launch commands,
// paths and board activity. When a write would take the file past its size, it is renamed to
// <path>.1 (the older copies move up to .2 ... .keep, the oldest is removed) and a new file is
// started. Every file is 0600 and opened without following a symlink planted at the path. A log
// from before rotation, over twice the size, keeps only its last trimTo bytes when it rotates
// (the operator's choice: 5 MB for coral.log); the rest is removed.
package logrotate

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"

	"github.com/cdknorow/coral/internal/secfile"
)

// File is an append-only log file that rotates at MaxSize, keeping Keep older copies.
type File struct {
	path    string
	maxSize int64
	keep    int
	trimTo  int64 // an oversized file (over 2×maxSize) keeps only its last trimTo bytes; 0: all

	mu   sync.Mutex
	f    *os.File
	size int64
}

// Open opens (or creates) path for appending. A file already over maxSize rotates on the next
// write; one over twice maxSize keeps only its last trimTo bytes then (0 keeps it whole). Copies
// beyond keep, from an earlier setting, are removed.
func Open(path string, maxSize int64, keep int, trimTo int64) (*File, error) {
	if maxSize <= 0 || keep < 1 || trimTo < 0 {
		return nil, fmt.Errorf("logrotate: size %d, keep %d and trim %d must be positive", maxSize, keep, trimTo)
	}
	r := &File{path: path, maxSize: maxSize, keep: keep, trimTo: trimTo}
	if err := r.open(); err != nil {
		return nil, err
	}
	for i := keep + 1; i <= keep+20; i++ {
		os.Remove(r.copyName(i))
	}
	return r, nil
}

func (r *File) copyName(i int) string { return r.path + "." + strconv.Itoa(i) }

func (r *File) open() error {
	f, err := secfile.OpenNoFollow(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil { // a file an older Coral left looser
		f.Close()
		return err
	}
	r.f, r.size = f, 0
	if st, err := f.Stat(); err == nil {
		r.size = st.Size()
	}
	return nil
}

// Write appends p, rotating first when p would take the file past its size. A line is never
// split across two files. When the new file can't be opened, the write fails and the next one
// tries again.
func (r *File) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f != nil && r.size > 0 && r.size+int64(len(p)) > r.maxSize {
		r.rotate()
	}
	if r.f == nil {
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// rotate closes the file and shifts the copies: path.keep is removed, path.i becomes path.i+1,
// path becomes path.1. The file is closed first, so this works on Windows too.
func (r *File) rotate() {
	oversized := r.trimTo > 0 && r.size > 2*r.maxSize
	r.f.Close()
	r.f = nil
	os.Remove(r.copyName(r.keep))
	for i := r.keep - 1; i >= 1; i-- {
		os.Rename(r.copyName(i), r.copyName(i+1))
	}
	if !oversized || r.copyTail(r.copyName(1)) != nil {
		os.Rename(r.path, r.copyName(1)) // whole (or the tail couldn't be written: keep it all)
	} else {
		os.Remove(r.path)
	}
	_ = r.open()
}

// copyTail writes the file's last trimTo bytes to dst, from the first whole line in them.
func (r *File) copyTail(dst string) error {
	src, err := secfile.OpenNoFollow(r.path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return err
	}
	in := bufio.NewReader(src)
	if start := st.Size() - r.trimTo; start > 0 {
		// From the byte before the cut: up to and including its newline is skipped, so the
		// tail starts on a whole line (and a cut on a line's start keeps that line).
		if _, err := src.Seek(start-1, io.SeekStart); err != nil {
			return err
		}
		if _, err := in.ReadString('\n'); err != nil {
			return err
		}
	}
	out, err := secfile.OpenNoFollow(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

// Close closes the file.
func (r *File) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
