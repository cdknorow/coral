package config

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lockIn(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ServerLockFile), []byte(body), 0o600))
	t.Setenv("CORAL_DATA_DIR", dir)
	t.Setenv("CORAL_DIR", "")
	return dir
}

// With nothing saying where Coral is, a command finds the running server by the address it recorded in its lock file (#256).
func TestLocalServerURLIsTheRecordedAddress(t *testing.T) {
	lockIn(t, "4242\nhttp://127.0.0.1:9123\n")
	assert.Equal(t, "http://127.0.0.1:9123", LocalServerURL())
	lockIn(t, "4242\nhttp://localhost:9124/\n")
	assert.Equal(t, "http://localhost:9124", LocalServerURL(), "no trailing slash")
	lockIn(t, "4242\nhttps://[::1]:9125\n")
	assert.Equal(t, "https://[::1]:9125", LocalServerURL())
}

// No lock (Coral not running here, or it stopped and emptied the file): the platform's default port, 8450 on Windows, 8420 elsewhere.
func TestLocalServerURLFallsBackToThePlatformDefaultPort(t *testing.T) {
	want := "http://localhost:" + strconv.Itoa(DefaultPort)
	dir := t.TempDir()
	t.Setenv("CORAL_DATA_DIR", dir)
	assert.Equal(t, want, LocalServerURL(), "no lock file")
	lockIn(t, "")
	assert.Equal(t, want, LocalServerURL(), "an emptied lock")
	lockIn(t, "4242\n")
	assert.Equal(t, want, LocalServerURL(), "a lock with no address")
}

// A lock that names an address that is not this computer's is ignored: a command never sends its token to it.
func TestLocalServerURLIgnoresAnAddressThatIsNotLoopback(t *testing.T) {
	want := "http://localhost:" + strconv.Itoa(DefaultPort)
	for _, body := range []string{
		"1\nhttp://example.com:8450\n", "1\nhttp://192.168.1.5:8450\n", "1\nhttp://127.0.0.1.example.com:8450\n",
		"1\nhttp://user:pw@127.0.0.1:8450\n", "1\nhttp://127.0.0.1:8450/admin\n", "1\nftp://127.0.0.1:21\n", "1\nhttp://127.0.0.1\n", "1\njavascript:alert(1)\n",
	} {
		lockIn(t, body)
		assert.Equal(t, want, LocalServerURL(), "%q", body)
	}
}
