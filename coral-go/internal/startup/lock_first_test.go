//go:build !windows

package startup

import (
	"bytes"
	"context"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
)

// folderState is everything a start could change in a folder: its own mode and, for each entry, its name, mode, size and modification time.
func folderState(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	fi, err := os.Stat(dir)
	require.NoError(t, err)
	out["."] = fi.Mode().String()
	require.NoError(t, filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out[rel] = info.Mode().String() + " " + info.ModTime().String() + " " + string(rune('0'+info.Size()%10))
		return nil
	}))
	return out
}

// #6379 (Security, from #6370's stray start): a second start that loses the instance lock touches nothing: no mode tightened, nothing created or written in the data folder, and no line about
// itself in the log. The lock is taken before the data folder is prepared.
func TestAStartThatLosesTheInstanceLockTouchesNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CORAL_NO_DEMO_SEED", "1")
	home := t.TempDir()
	cfg := config.Load(home)
	cfg.Host = "127.0.0.1"
	cfg.Port = freePort(t)
	dir := cfg.CoralDir()
	require.NoError(t, os.MkdirAll(dir, 0o755)) // looser than a start would leave it: a start that got as far as preparing the folder would tighten it
	require.NoError(t, os.Chmod(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("kept"), 0o644))

	lockPath := filepath.Join(dir, "coral-server.lock")
	first, err := acquireInstanceLock(lockPath) // the running server
	require.NoError(t, err)
	defer first.close(lockPath)
	before := folderState(t, dir)

	var logged bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(prev) })

	rs, err := Start(context.Background(), cfg, Options{BackendType: "pty"})
	require.Error(t, err)
	assert.Nil(t, rs)
	assert.Contains(t, err.Error(), "already running")

	assert.Equal(t, before, folderState(t, dir), "the losing start changed nothing in the data folder (modes, files, sizes, times)")
	assert.Equal(t, "drwxr-xr-x", folderState(t, dir)["."], "the folder's mode is as it was: not tightened")
	assert.NotContains(t, logged.String(), "[STARTUP]", "and logged nothing about itself")
}

// A start with no data folder yet has no server to lose to: it makes the folder (this user only), takes the lock, and a later failure (the port is taken) releases it.
func TestAFirstStartMakesTheFolderAndReleasesTheLockWhenItFailsLater(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CORAL_NO_DEMO_SEED", "1")
	home := filepath.Join(t.TempDir(), "new")
	cfg := config.Load(home)
	cfg.Host = "127.0.0.1"
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer taken.Close()
	cfg.Port = taken.Addr().(*net.TCPAddr).Port
	require.True(t, strings.HasPrefix(cfg.CoralDir(), home), "the data folder is under the test's own folder")
	require.NoError(t, os.RemoveAll(cfg.CoralDir())) // config.Load made it; a first start has none
	_, statErr := os.Stat(cfg.CoralDir())
	require.True(t, os.IsNotExist(statErr), "no folder yet")

	_, err = Start(context.Background(), cfg, Options{BackendType: "pty"})
	require.Error(t, err)
	assert.True(t, addrInUse(err), "it got as far as binding: %v", err)
	fi, statErr := os.Stat(cfg.CoralDir())
	require.NoError(t, statErr, "the first start made the folder")
	assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
	lockPath := filepath.Join(cfg.CoralDir(), "coral-server.lock")
	lock, err := acquireInstanceLock(lockPath)
	require.NoError(t, err, "the failed start released the lock it took")
	lock.close(lockPath)
}
