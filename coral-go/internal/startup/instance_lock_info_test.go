package startup

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
)

// #235: once the server listens it records its address next to its PID in the lock file, and a second start on the same data
// folder names both and says what to do, instead of only "in use".
func TestASecondStartSaysWhichCoralRunsAndWhere(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "coral-server.lock")
	first, err := acquireInstanceLock(lockPath)
	require.NoError(t, err)
	defer first.close(lockPath)
	first.recordAddress("http://localhost:8476")

	pid, url := runningServer(lockPath)
	assert.Equal(t, os.Getpid(), pid)
	assert.Equal(t, "http://localhost:8476", url)

	_, err = acquireInstanceLock(lockPath)
	require.ErrorIs(t, err, errAlreadyRunning, "the lock is held")
	msg := alreadyRunningError(dir, lockPath).Error()
	assert.Contains(t, msg, "PID "+strconv.Itoa(os.Getpid()))
	assert.Contains(t, msg, "http://localhost:8476")
	assert.Contains(t, msg, dir)
	assert.Contains(t, msg, "coral open")
}

// A lock file from before the address was recorded (a PID only), or one that can't be read, still gives a useful message.
func TestRunningServerToleratesOldAndMissingLockFiles(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "coral-server.lock")
	pid, url := runningServer(p)
	assert.Zero(t, pid)
	assert.Empty(t, url)
	require.NoError(t, os.WriteFile(p, []byte("1234\n"), 0o600))
	pid, url = runningServer(p)
	assert.Equal(t, 1234, pid)
	assert.Empty(t, url)
	assert.Contains(t, alreadyRunningError(dir, p).Error(), ", PID 1234.")
	require.NoError(t, os.WriteFile(p, []byte("1234\njavascript:evil\n"), 0o600))
	_, url = runningServer(p)
	assert.Empty(t, url, "only an http(s) address is shown")
}

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
