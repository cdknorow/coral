//go:build !windows

package ptymanager

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3601 (weak site W17): an agent's pipe log is readable by this user only and never written
// through a symlink or a hard link planted at its name; a FIFO there never blocks the server.
func TestAgentLogIsPrivateAndNeverFollowed(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "claude_coral_x.log")

	require.NoError(t, resetLog(log))
	fi, err := os.Stat(log)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), "created 0600")
	require.NoError(t, os.WriteFile(log, []byte("old output"), 0o600))
	require.NoError(t, resetLog(log))
	b, _ := os.ReadFile(log)
	assert.Empty(t, b, "a respawn empties the log")
	assert.Error(t, createLog(log), "the self-heal creates only a missing log")

	outside := filepath.Join(t.TempDir(), "victim")
	require.NoError(t, os.WriteFile(outside, []byte("keep me"), 0o600))
	for name, plant := range map[string]func(p string) error{
		"a symlink":   func(p string) error { return os.Symlink(outside, p) },
		"a hard link": func(p string) error { return os.Link(outside, p) },
	} {
		p := filepath.Join(dir, "planted_"+filepath.Base(name)+".log")
		require.NoError(t, plant(p), name)
		assert.Error(t, resetLog(p), "%s: resetLog refuses it", name)
		f, err := openLogAppend(p)
		if err == nil {
			f.Close()
		}
		assert.Error(t, err, "%s: the pty backend's append refuses it", name)
		b, _ := os.ReadFile(outside)
		assert.Equal(t, "keep me", string(b), "%s: the file it leads to is unchanged", name)
		os.Remove(p)
	}

	fifo := filepath.Join(dir, "fifo.log")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	done := make(chan error, 2)
	go func() {
		_, err := openLogAppend(fifo)
		done <- err
		done <- resetLog(fifo)
	}()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			assert.Error(t, err, "a FIFO is refused")
		case <-time.After(3 * time.Second):
			t.Fatal("opening a FIFO log blocked")
		}
	}

	f, err := openLogAppend(filepath.Join(dir, "fresh.log"))
	require.NoError(t, err)
	f.Close()
	fi, _ = os.Stat(filepath.Join(dir, "fresh.log"))
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
}
