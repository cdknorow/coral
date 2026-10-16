//go:build windows

package startup

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3906 (sweep item 6): a lock file left by a crash doesn't block the next start, the lock
// itself still keeps a second server out, and the recorded pid stays readable.
func TestInstanceLockSurvivesALeftoverFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coral-server.lock")
	require.NoError(t, os.WriteFile(path, []byte("12345\n"), 0o600)) // a crashed server's file

	first, err := acquireInstanceLock(path)
	require.NoError(t, err, "a leftover file is reused")
	t.Cleanup(func() { first.close(path) })

	_, err = acquireInstanceLock(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already running")

	data, err := os.ReadFile(path) // another process reading the pid isn't blocked by the lock
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(os.Getpid()), strings.TrimSpace(string(data)))

	// The process "ends" without close: the handle goes, and with it the lock.
	require.NoError(t, first.file.Close())
	first.file = nil
	second, err := acquireInstanceLock(path)
	require.NoError(t, err, "a lock whose owner is gone doesn't block")
	second.close(path)
}
