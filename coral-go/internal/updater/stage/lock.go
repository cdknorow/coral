package stage

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/subgentic/coral/internal/updater/verify"
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
	// Nonce is unique to one take of the lock: a check removes a lock only if the file still holds exactly what it wrote (removeOwn), so it can never remove a lock another
	// check made after it (file identities are not enough: Windows reuses a deleted file's id at once).
	Nonce string `json:"nonce,omitempty"`
}

// pidAlive is set per platform (lock_unix.go, lock_windows.go).

// applyRunning reports whether dir holds a live apply lock.
func applyRunning(dir string, now time.Time) bool {
	path := filepath.Join(dir, ApplyLockFile)
	fi, err := os.Lstat(path)
	if err != nil {
		return false
	}
	b, rerr := os.ReadFile(path)
	if rerr != nil { // a lock that cannot be read (Windows: another check is moving or removing it) is not shown to be dead: it counts as held
		return !errors.Is(rerr, fs.ErrNotExist)
	}
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

// afterStaleJudged is a test seam: it runs after a check has read a lock as stale and before it acts on that, where another check can run.
var afterStaleJudged func()

// takeoverStale is how long a takeover lock may stand before it is taken to be left by a check that died in the middle of one.
const takeoverStale = time.Minute

// takeLock creates dir/name exclusively. It returns the release function.
//
// A stale lock (its owner is gone, or it is too old) is taken over under a second lock, name.takeover, that only one check can hold: the check
// that holds it reads the lock again, and when it is still stale replaces it in one rename of its own lock over it (#5303). The path is never
// empty in between, so no other check can create a lock in the gap, and no check ever removes a lock it has not just read as stale while
// holding the takeover lock (a remove-then-create takeover let two checks that had read the same stale lock each remove the other's new one).
func takeLock(dir, name string, now time.Time) (func(), error) {
	path := filepath.Join(dir, name)
	body, _ := json.Marshal(lockBody{PID: os.Getpid(), Taken: now.Unix(), Nonce: newNonce()})
	// The release removes the lock this check took, and only that: the file must still hold exactly the body this check wrote (removeOwn), so a lock another check has put there
	// since is left alone.
	release := func() { removeOwn(path, body) }
	for attempt := 0; attempt < 2; attempt++ {
		err := createLock(dir, path, body)
		if err == nil {
			return release, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		fi, lerr := os.Lstat(path)
		if lerr != nil {
			continue // released meanwhile: try to create it
		}
		if !fi.Mode().IsRegular() {
			return nil, ErrBusy // a lock that is not a plain file is not ours to remove
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			if errors.Is(rerr, fs.ErrNotExist) {
				continue // released meanwhile: try to create it
			}
			return nil, ErrBusy // fail closed: a lock that cannot be read is not shown to be stale (on Windows a live holder's lock can refuse a read while it is moved or removed)
		}
		if live(b, fi.ModTime(), now) {
			return nil, ErrBusy
		}
		if afterStaleJudged != nil {
			afterStaleJudged()
		}
		switch takeOver(dir, path, body, now) {
		case tookOver:
			return release, nil
		case lockReleased:
			continue
		}
		return nil, ErrBusy
	}
	return nil, ErrBusy
}

type takeoverResult int

const (
	tookOver     takeoverResult = iota // the stale lock is replaced by ours
	lockReleased                       // there is no lock any more: create one
	takeoverBusy                       // another check is taking over, or the lock is live
)

// removeDeadMutex removes a takeover lock that was judged dead with the content judged, by renaming it aside (atomic: of several checks that
// judged the same one only one moves it) and checking what was moved: a fresh takeover lock another check made in between is put back, not
// destroyed (Security's #5335 LOW 1: two checks that both removed "the same" dead lock could remove a third's live one).
func removeDeadMutex(dir, mutex string, judged []byte) {
	tmp, err := os.CreateTemp(dir, ".dead-*")
	if err != nil {
		return
	}
	aside := tmp.Name()
	tmp.Close()
	os.Remove(aside)
	if err := os.Rename(mutex, aside); err != nil {
		return // gone already, or not ours to move
	}
	got, _ := os.ReadFile(aside)
	if bytes.Equal(got, judged) {
		os.Remove(aside)
		return
	}
	// It was not the dead one: put it back (a failure means another one was created meanwhile; that holder is the check that made it).
	_ = os.Link(aside, mutex)
	os.Remove(aside)
}

// takeOver replaces the stale lock at path with one holding body, under name.takeover.
func takeOver(dir, path string, body []byte, now time.Time) takeoverResult {
	mutex := path + ".takeover"
	if err := createLock(dir, mutex, body); err != nil {
		if errors.Is(err, fs.ErrExist) {
			// Another check is taking over. One left by a check that died is removed (it guards nothing but this), and the next check goes on.
			if fi, lerr := os.Lstat(mutex); lerr == nil && fi.Mode().IsRegular() {
				if mb, merr := os.ReadFile(mutex); merr == nil && (!live(mb, fi.ModTime(), now) || time.Since(fi.ModTime()) > takeoverStale) {
					removeDeadMutex(dir, mutex, mb) // only a takeover lock that was read, and judged dead on what was read
				}
			}
		}
		return takeoverBusy
	}
	defer removeOwn(mutex, body) // the takeover lock this check just made: only a file that still holds this check's body is removed
	// Read again, now that no other check can be taking over.
	fi, err := os.Lstat(path)
	if err != nil {
		return lockReleased
	}
	if !fi.Mode().IsRegular() {
		return takeoverBusy
	}
	b, rerr := os.ReadFile(path)
	if rerr != nil {
		if errors.Is(rerr, fs.ErrNotExist) {
			return lockReleased
		}
		return takeoverBusy // fail closed: a lock that cannot be read is never replaced
	}
	if live(b, fi.ModTime(), now) {
		return takeoverBusy
	}
	tmp, err := os.CreateTemp(dir, ".lock-*")
	if err != nil {
		return takeoverBusy
	}
	name := tmp.Name()
	_, werr := tmp.Write(body)
	if cerr := tmp.Close(); werr != nil || cerr != nil {
		os.Remove(name)
		return takeoverBusy
	}
	if err := os.Rename(name, path); err != nil { // atomic: the path holds the stale lock, then ours
		os.Remove(name)
		return takeoverBusy
	}
	return tookOver
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

// TakeLock is takeLock for the apply: it takes dir/name (the apply's own lock) the way a check takes its own, with the same stale takeover.
func TakeLock(dir, name string, now time.Time) (release func(), err error) {
	return takeLock(dir, name, now)
}

// CheckRunning reports whether a check holds its lock, so an apply does not start over one.
func CheckRunning(dir string, now time.Time) bool {
	path := filepath.Join(dir, CheckLockFile)
	fi, err := os.Lstat(path)
	if err != nil {
		return false
	}
	b, rerr := os.ReadFile(path)
	if rerr != nil { // a lock that cannot be read (Windows: another check is moving or removing it) is not shown to be dead: it counts as held
		return !errors.Is(rerr, fs.ErrNotExist)
	}
	return live(b, fi.ModTime(), now)
}
