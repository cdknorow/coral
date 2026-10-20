package stage

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/cdknorow/coral/internal/updater/verify"
)

// One check at a time, and none while an apply runs (#5231 section 2): an exclusive-create lock file holding the owner's pid and when it was taken.
// A lock whose owner is gone, or older than staleAfter (a check does not take longer), is stale and taken over.
const (
	CheckLockFile = "check.lock"
	ApplyLockFile = "apply.lock"
	staleAfter    = 2 * time.Hour
)

// ErrBusy means another check (or an apply) holds its lock.
var ErrBusy = errors.New("another update step is running")

type lockBody struct {
	PID   int   `json:"pid"`
	Taken int64 `json:"taken"`
}

// pidAlive is set per platform (lock_unix.go, lock_windows.go).

// applyRunning reports whether dir holds a live apply lock.
func applyRunning(dir string, now time.Time) bool {
	path := filepath.Join(dir, ApplyLockFile)
	fi, err := os.Lstat(path)
	if err != nil {
		return false
	}
	b, _ := os.ReadFile(path)
	return live(b, fi.ModTime(), now)
}

// newLockGrace is how long a lock file whose body is not there yet (its owner created it a moment ago and has not written it) counts as held.
const newLockGrace = 30 * time.Second

func live(b []byte, modified, now time.Time) bool {
	var l lockBody
	if json.Unmarshal(b, &l) != nil || l.PID <= 0 {
		return time.Since(modified) < newLockGrace // the real clock: the file was written just now or it was not
	}
	if now.Sub(time.Unix(l.Taken, 0)) > staleAfter {
		return false
	}
	return pidAlive(l.PID)
}

// takeLock creates dir/name exclusively. It returns the release function.
func takeLock(dir, name string, now time.Time) (func(), error) {
	path := filepath.Join(dir, name)
	body, _ := json.Marshal(lockBody{PID: os.Getpid(), Taken: now.Unix()})
	for attempt := 0; attempt < 2; attempt++ {
		err := createLock(dir, path, body)
		if err == nil {
			return func() { os.Remove(path) }, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		fi, lerr := os.Lstat(path)
		if lerr != nil || !fi.Mode().IsRegular() {
			return nil, ErrBusy // a lock that is not a plain file is not ours to remove
		}
		b, rerr := os.ReadFile(path)
		if rerr == nil && live(b, fi.ModTime(), now) {
			return nil, ErrBusy
		}
		// Stale: its owner is gone or it is too old. Take it over once.
		if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			return nil, ErrBusy
		}
	}
	return nil, ErrBusy
}

// createLock makes path with body in it or fails with fs.ErrExist: the body is written to a temp file first and hard-linked into place, so no other
// check ever sees a lock with nothing in it. Where hard links are not available it creates the lock exclusively and writes into it (readers then
// give a lock that is still empty the newLockGrace).
func createLock(dir, path string, body []byte) error {
	tmp, err := os.CreateTemp(dir, ".lock-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	lerr := os.Link(name, path)
	if lerr == nil || errors.Is(lerr, fs.ErrExist) {
		return lerr
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, verify.PrivateMode)
	if err != nil {
		return err
	}
	_, werr := f.Write(body)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(path)
	}
	return werr
}
