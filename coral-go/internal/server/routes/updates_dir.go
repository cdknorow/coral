package routes

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/subgentic/coral/internal/secfile"
)

// updatesDir is <data dir>/updates, where the updater's and the relaunch routes' state files live: a folder only this user can use, holding plain files only this user can read, each
// replaced atomically.
type updatesDir struct{ Dir string }

func (d updatesDir) ensure() error {
	if d.Dir == "" {
		return errors.New("no data directory")
	}
	fi, err := os.Lstat(d.Dir)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(d.Dir, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(d.Dir, 0o700); err != nil {
			return err
		}
		return secfile.Protect(d.Dir)
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("the updates folder is not a plain folder")
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return errors.New("the updates folder is readable by others")
	}
	return nil
}

// readPrivate reads a state file that must be a plain file only this user can read. A missing file is (nil, ErrNotExist).
func (d updatesDir) read(name string, max int64) ([]byte, error) {
	p := filepath.Join(d.Dir, name)
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New(name + " is not a plain file")
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return nil, errors.New(name + " is readable by others")
	}
	f, err := secfile.OpenNoFollow(p, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, errors.New(name + " is too large")
	}
	return b, nil
}

// writePrivate replaces a state file atomically: a temp file made 0600, written, synced, renamed.
func (d updatesDir) write(name string, data []byte) error {
	f, err := os.CreateTemp(d.Dir, "."+name+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if err := secfile.Protect(tmp); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(d.Dir, name))
}
