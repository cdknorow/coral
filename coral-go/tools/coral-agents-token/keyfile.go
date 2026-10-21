package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	keyFileName = "private-key.pem"
	maxKeyBytes = 64 << 10
)

// keyStore is the folder that holds the App key and, in its cache subfolder, the cached tokens.
// uid is the user the folder and files must belong to.
type keyStore struct {
	dir string
	uid int
}

func (k keyStore) keyPath() string              { return filepath.Join(k.dir, keyFileName) }
func (k keyStore) cacheDir() string             { return filepath.Join(k.dir, "cache") }
func (k keyStore) cachePath(name string) string { return filepath.Join(k.cacheDir(), name) }

// checkDir refuses a folder that is a symlink, not a directory, not mode 0700 or not owned by the user.
func checkDir(path string, uid int) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("folder %s: %w", path, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("folder %s is a symlink: refused", path)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a folder", path)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		return fmt.Errorf("folder %s has mode %04o: it must be 0700", path, perm)
	}
	return checkOwner(path, fi, uid)
}

// checkFile refuses a file that is a symlink, not a regular file, not mode 0600 or not owned by the user.
func checkFile(path string, fi os.FileInfo, uid int) error {
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("file %s is a symlink: refused", path)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		return fmt.Errorf("file %s has mode %04o: it must be 0600", path, perm)
	}
	return checkOwner(path, fi, uid)
}

// readKey returns the key file's bytes after the folder and the file pass their checks. The file is
// opened without following a symlink and checked again through the open descriptor, so a swap after
// the first look changes nothing.
func (k keyStore) readKey() ([]byte, error) {
	if err := checkDir(k.dir, k.uid); err != nil {
		return nil, err
	}
	path := k.keyPath()
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("key file: %w", err)
	}
	if err := checkFile(path, fi, k.uid); err != nil {
		return nil, err
	}
	f, err := openNoFollow(path)
	if err != nil {
		return nil, fmt.Errorf("key file %s: could not open without following a link", path)
	}
	defer f.Close()
	fi2, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("key file %s: %w", path, err)
	}
	if err := checkFile(path, fi2, k.uid); err != nil {
		return nil, err
	}
	if !os.SameFile(fi, fi2) {
		return nil, errors.New("key file changed while it was being opened: refused")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxKeyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("key file %s: could not be read", path)
	}
	if len(b) > maxKeyBytes {
		return nil, fmt.Errorf("key file %s is larger than %d bytes: refused", path, maxKeyBytes)
	}
	return b, nil
}
