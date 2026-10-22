package stage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/secfile/secfiletest"

	"github.com/subgentic/coral/internal/updater/verify"
)

// #5303 (c): several checks that all judged the same lock stale must not each take it. With a remove-then-create takeover, one removes the stale
// lock and creates its own, another (still holding the stale judgement) removes THAT one and creates its own: two holders at once. The takeover
// renames the lock away and checks what it moved, so only one check ever holds it.
func TestChecksOverOneStaleLockNeverHoldItTogether(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	stale, _ := json.Marshal(lockBody{PID: 2147483646, Taken: now.Unix()})
	var violations atomic.Int32
	var won atomic.Int32
	for round := 0; round < 500; round++ {
		require.NoError(t, os.WriteFile(filepath.Join(dir, CheckLockFile), stale, 0o600))
		var holders atomic.Int32
		var ready, start, done sync.WaitGroup
		start.Add(1)
		for i := 0; i < 12; i++ {
			ready.Add(1)
			done.Add(1)
			go func() {
				defer done.Done()
				ready.Done()
				start.Wait()
				release, err := takeLock(dir, CheckLockFile, now)
				if err != nil {
					return
				}
				won.Add(1)
				if holders.Add(1) > 1 {
					violations.Add(1)
				}
				time.Sleep(3 * time.Millisecond)
				holders.Add(-1)
				release()
			}()
		}
		ready.Wait()
		start.Done()
		done.Wait()
		_ = os.Remove(filepath.Join(dir, CheckLockFile))
	}
	assert.Zero(t, violations.Load(), "two checks held the lock at the same time")
	assert.GreaterOrEqual(t, int(won.Load()), 500, "someone took the stale lock every round")
	entries, _ := os.ReadDir(dir)
	assert.Empty(t, entries, "no moved-away lock or temp file is left behind")
}

// The same race, made certain: check B reads the stale lock and stops; check A takes over and holds the lock; then B goes on with what it read.
// B must find A's lock live and back off. (With remove-then-create B removes A's new lock and creates its own: two holders.)
func TestACheckThatReadTheStaleLockCannotTakeANewLockOverFromAnother(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	stale, _ := json.Marshal(lockBody{PID: 2147483646, Taken: now.Unix()})
	require.NoError(t, os.WriteFile(filepath.Join(dir, CheckLockFile), stale, 0o600))
	var calls atomic.Int32
	paused, resume := make(chan struct{}), make(chan struct{})
	afterStaleJudged = func() {
		if calls.Add(1) == 1 { // B, the first to judge
			close(paused)
			<-resume
		}
	}
	t.Cleanup(func() { afterStaleJudged = nil })
	bResult := make(chan error, 1)
	go func() {
		_, err := takeLock(dir, CheckLockFile, now)
		bResult <- err
	}()
	<-paused
	releaseA, err := takeLock(dir, CheckLockFile, now)
	require.NoError(t, err, "A takes the stale lock")
	close(resume)
	assert.ErrorIs(t, <-bResult, ErrBusy, "B must not take A's lock")
	b, rerr := os.ReadFile(filepath.Join(dir, CheckLockFile))
	require.NoError(t, rerr)
	assert.Contains(t, string(b), `"pid"`, "A's lock is still in place")
	releaseA()
}

// A takeover lock left by a check that died in the middle of a takeover is removed, and the next check goes on; a live one makes the check wait.
func TestATakeoverLockLeftByADeadCheckDoesNotBlockForever(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	stale, _ := json.Marshal(lockBody{PID: 2147483646, Taken: now.Unix()})
	require.NoError(t, os.WriteFile(filepath.Join(dir, CheckLockFile), stale, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, CheckLockFile+".takeover"), stale, 0o600))
	_, err := takeLock(dir, CheckLockFile, now)
	assert.ErrorIs(t, err, ErrBusy, "this check backs off; it removes the dead takeover lock")
	_, err = os.Stat(filepath.Join(dir, CheckLockFile+".takeover"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	release, err := takeLock(dir, CheckLockFile, now)
	require.NoError(t, err, "the next check takes over")
	release()

	live, _ := json.Marshal(lockBody{PID: os.Getpid(), Taken: now.Unix()})
	require.NoError(t, os.WriteFile(filepath.Join(dir, CheckLockFile), stale, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, CheckLockFile+".takeover"), live, 0o600))
	_, err = takeLock(dir, CheckLockFile, now)
	assert.ErrorIs(t, err, ErrBusy)
	_, err = os.Stat(filepath.Join(dir, CheckLockFile+".takeover"))
	assert.NoError(t, err, "a live takeover is left alone")
}

// #5303 (d): a staged file is opened without following a link, with no check before the open that a swap could outrun. The seam swaps the
// file for a link to a file elsewhere at the last moment, as a racing process would.
func TestAStagedFileSwappedForALinkAtTheLastMomentIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a symlink needs a privilege on Windows")
	}
	e := newEnv(t, "1.4.125")
	require.Equal(t, ResultStaged, e.check().Result)
	s, err := LoadStaged(e.data)
	require.NoError(t, err)
	secret := filepath.Join(t.TempDir(), "elsewhere")
	require.NoError(t, os.WriteFile(secret, []byte("not what was staged"), 0o600))
	t.Cleanup(func() { beforeOpen = nil })

	for _, name := range []string{RecordFile, SumsFile, ManifestBundleFile, filepath.Base(s.PackagePath)} {
		target := filepath.Join(s.Dir, name)
		orig, err := os.ReadFile(target)
		require.NoError(t, err)
		beforeOpen = func(path string) {
			if path == target {
				os.Remove(path)
				os.Symlink(secret, path)
			}
		}
		_, err = LoadStaged(e.data)
		assert.ErrorIs(t, err, ErrNoStaged, "%s swapped for a link just before it is opened", name)
		beforeOpen = nil
		os.Remove(target)
		require.NoError(t, os.WriteFile(target, orig, 0o600))
	}
	// The apply's open of the package, the same.
	good, err := LoadStaged(e.data)
	require.NoError(t, err)
	beforeOpen = func(path string) {
		if path == good.PackagePath {
			os.Remove(path)
			os.Symlink(secret, path)
		}
	}
	_, err = Reverify(e.signer, e.cfg.Pins, good, asset, verify.State{}, "1.4.125", e.now, DefaultMaxPackage)
	assert.Error(t, err)
	beforeOpen = nil
}

func TestOpenPlainRefusesAFolderAndAFIFO(t *testing.T) {
	dir := t.TempDir()
	secfiletest.OwnerOnlyDir(t, dir) // as the data folder is: %TEMP%'s inherited access list can grant another account
	_, err := OpenPlain(dir)
	assert.Error(t, err, "a folder")
	if runtime.GOOS != "windows" {
		fifo := filepath.Join(dir, "fifo")
		if err := mkfifo(fifo); err == nil {
			_, err := OpenPlain(fifo)
			assert.Error(t, err, "a FIFO is not a plain file (and the open does not block)")
		}
	}
	f, err := OpenPlain(writeFile(t, dir, "ok", "x"))
	require.NoError(t, err)
	f.Close()
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

// Security's #5335 LOW 1: a dead takeover lock is moved aside and what was moved is checked, so a fresh takeover lock that another check made after
// the dead one was judged is put back, not destroyed.
func TestADeadTakeoverLockIsRemovedButAFreshOneThatReplacedItIsNot(t *testing.T) {
	dir := t.TempDir()
	mutex := filepath.Join(dir, CheckLockFile+".takeover")
	dead, _ := json.Marshal(lockBody{PID: 2147483646, Taken: 1})
	fresh, _ := json.Marshal(lockBody{PID: os.Getpid(), Taken: time.Now().Unix()})

	require.NoError(t, os.WriteFile(mutex, dead, 0o600))
	removeDeadMutex(dir, mutex, dead)
	_, err := os.Stat(mutex)
	assert.ErrorIs(t, err, os.ErrNotExist, "the dead one is gone")

	require.NoError(t, os.WriteFile(mutex, fresh, 0o600))
	removeDeadMutex(dir, mutex, dead) // judged dead, but what is there now is another check's
	b, err := os.ReadFile(mutex)
	require.NoError(t, err)
	assert.Equal(t, string(fresh), string(b), "the fresh takeover lock is put back")
	left, _ := os.ReadDir(dir)
	assert.Len(t, left, 1, "nothing is left beside it")

	require.NoError(t, os.Remove(mutex))
	removeDeadMutex(dir, mutex, dead) // already gone: nothing to do
	left, _ = os.ReadDir(dir)
	assert.Empty(t, left)
}
