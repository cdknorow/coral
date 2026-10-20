package stage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5441: the release of a lock removes the lock this check took and only that. If the file at the path holds another check's body (a lock made after the one this check
// held was taken over, the 132 gate's two holders on Windows), releasing leaves it alone; releasing one's own lock still removes it.
func TestReleaseNeverRemovesALockAnotherCheckPutThere(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	release, err := takeLock(dir, CheckLockFile, now)
	require.NoError(t, err)
	path := filepath.Join(dir, CheckLockFile)
	mine, err := os.ReadFile(path)
	require.NoError(t, err)

	// Another check's lock is now at the path (this one was taken over): the release does not touch it.
	other := []byte(`{"pid":1,"taken":1,"nonce":"another-check"}`)
	require.NoError(t, os.WriteFile(path, other, 0o600))
	release()
	got, err := os.ReadFile(path)
	require.NoError(t, err, "the other check's lock is still there")
	assert.Equal(t, other, got)
	assert.NotEqual(t, mine, got)

	// Its own lock is removed, and a second release of it (a deferred one after a panic, say) is harmless.
	require.NoError(t, os.Remove(path))
	release2, err := takeLock(dir, CheckLockFile, now)
	require.NoError(t, err)
	release2()
	_, err = os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)
	release2()
}
