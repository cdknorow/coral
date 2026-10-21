package startup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
)

// #4157: an unreadable API key fails Start with an error (the tray shows it with Open Log and
// Retry) instead of exiting the process, and the data folder's lock is released.
func TestStartReturnsTheAPIKeyError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CORAL_NO_DEMO_SEED", "1")
	home := t.TempDir()
	cfg := config.Load(home)
	cfg.Host = "127.0.0.1"
	cfg.Port = freePort(t)
	require.NoError(t, os.MkdirAll(filepath.Join(cfg.CoralDir(), "api_key"), 0o700))

	rs, err := Start(context.Background(), cfg, Options{BackendType: "pty"})
	require.Error(t, err)
	assert.Nil(t, rs)
	assert.Contains(t, err.Error(), "API key")

	lockPath := filepath.Join(cfg.CoralDir(), "coral-server.lock")
	lock, err := acquireInstanceLock(lockPath)
	require.NoError(t, err, "the lock is released")
	lock.close(lockPath)
}
