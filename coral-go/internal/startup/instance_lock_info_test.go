package startup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
)

// #235: once the server listens it records its address next to its PID in the lock file, where a command finds it, and a server
// that stops empties the file before it lets the lock go, so a command does not find the address of one that is gone; the next
// server can still take the lock and record its own.
func TestTheServerRecordsItsAddressWhileItRuns(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, config.ServerLockFile)
	first, err := acquireInstanceLock(lockPath)
	require.NoError(t, err)
	first.recordAddress("http://localhost:8476")
	pid, url := config.RecordedServer(dir)
	assert.Equal(t, os.Getpid(), pid)
	assert.Equal(t, "http://localhost:8476", url)
	first.close(lockPath)

	pid, url = config.RecordedServer(dir)
	assert.Zero(t, pid)
	assert.Empty(t, url)

	second, err := acquireInstanceLock(lockPath)
	require.NoError(t, err, "the next server takes the lock")
	defer second.close(lockPath)
	second.recordAddress("http://localhost:8477")
	_, url = config.RecordedServer(dir)
	assert.Equal(t, "http://localhost:8477", url)
}
