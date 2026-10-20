package stage

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRemoveOwnRemovesTheFileThisCheckMade(t *testing.T) {
	dir := t.TempDir()
	removeOwn(filepath.Join(dir, "gone"), []byte("x")) // nothing there: no panic, no retry
	p := filepath.Join(dir, "a.lock")
	require.NoError(t, os.WriteFile(p, []byte("mine"), 0o600))
	removeOwn(p, []byte("mine"))
	_, err := os.Stat(p)
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.False(t, retryable(nil))
	assert.False(t, retryable(os.ErrNotExist))
	assert.Equal(t, runtime.GOOS == "windows", retryable(os.ErrPermission), "only Windows retries a refusal (an open handle)")
	assert.NotEqual(t, newNonce(), newNonce())
}

// The removal is of the lock this check wrote: a lock another check put at the path since (other content) is never removed (a retried remove that did was how two checks came
// to hold the lock together, #5403 on the 132 gate).
func TestRemoveOwnNeverRemovesAnotherChecksLock(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "check.lock")
	require.NoError(t, os.WriteFile(p, []byte("mine"), 0o600))
	require.NoError(t, os.Remove(p)) // released (or taken over), then another check makes its own
	require.NoError(t, os.WriteFile(p, []byte("theirs"), 0o600))
	removeOwn(p, []byte("mine"))
	b, err := os.ReadFile(p)
	require.NoError(t, err, "the other check's lock is still there")
	assert.Equal(t, "theirs", string(b))
}

// A lock another check has open (reading it to judge it) is removed once that check lets go, not left behind (#5403). On Windows the open file refuses the removal and it is
// retried; elsewhere an open file is removable at once.
func TestALockFileAnotherCheckHasOpenIsRemovedOnceItIsClosed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "takeover.lock")
	require.NoError(t, os.WriteFile(p, []byte("mine"), 0o600))
	f, err := os.Open(p) // Go opens without FILE_SHARE_DELETE
	require.NoError(t, err)
	go func() { time.Sleep(150 * time.Millisecond); f.Close() }()
	start := time.Now()
	removeOwn(p, []byte("mine"))
	if runtime.GOOS == "windows" {
		assert.GreaterOrEqual(t, time.Since(start), 100*time.Millisecond, "it waited for the reader (the sharing violation was retried)")
	}
	_, err = os.Stat(p)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// A lock that cannot be read is never taken to be stale: the check backs off (fail closed). A folder where the lock should be is "not a plain file" and refused the same way;
// a lock that is gone is released.
func TestALockThatCannotBeReadIsNeverReplaced(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, CheckLockFile)
	require.NoError(t, os.Mkdir(p, 0o700))
	_, err := takeLock(dir, CheckLockFile, time.Now())
	assert.ErrorIs(t, err, ErrBusy)
	assert.True(t, CheckRunning(dir, time.Now()), "a lock that cannot be read counts as held")
	assert.True(t, applyRunning2(t, dir), "the same for the apply lock")
}

func applyRunning2(t *testing.T, dir string) bool {
	t.Helper()
	require.NoError(t, os.Mkdir(filepath.Join(dir, ApplyLockFile), 0o700))
	return applyRunning(dir, time.Now())
}

// The case that was fail-open: a lock file that cannot be read (here: no read permission; on Windows, a live holder's lock while it is moved or removed) was taken for a stale
// one and replaced, so two checks held the lock together. It is now left alone and the check backs off.
func TestAnUnreadableLockFileIsNeverTakenOver(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a file the user cannot read")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, CheckLockFile)
	stale := []byte(`{"pid":2147483646,"taken":1}`)
	require.NoError(t, os.WriteFile(p, stale, 0o600))
	old := time.Now().Add(-time.Hour) // past the grace a new lock gets, so a lock with no readable body would be judged stale
	require.NoError(t, os.Chtimes(p, old, old))
	require.NoError(t, os.Chmod(p, 0))
	t.Cleanup(func() { os.Chmod(p, 0o600) })
	_, err := takeLock(dir, CheckLockFile, time.Now())
	assert.ErrorIs(t, err, ErrBusy, "an unreadable lock is not shown to be stale")
	require.NoError(t, os.Chmod(p, 0o600))
	b, _ := os.ReadFile(p)
	assert.Equal(t, stale, b, "and it was not replaced")
}
