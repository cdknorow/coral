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

// Security's SHOULD-2 on #3601: an agent started by an older install logged to the shared temp
// dir at 0644. On reattach its log moves into the data folder: the new log holds the old output
// at 0600 and the old file is gone. A symlink at the old name is not followed.
func TestReattachMovesAnOldTempLog(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	logs := t.TempDir()
	name := "claude_coral_0b2f6c1e-3d4a-4b5c-8d6e-7f8091a2b3c4.log"
	old := filepath.Join(tmp, name)
	require.NoError(t, os.WriteFile(old, []byte("scrollback before the upgrade\n"), 0o644))

	assert.True(t, migrateTempLog(filepath.Join(logs, name)))
	b, err := os.ReadFile(filepath.Join(logs, name))
	require.NoError(t, err)
	assert.Equal(t, "scrollback before the upgrade\n", string(b))
	fi, _ := os.Stat(filepath.Join(logs, name))
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	assert.NoFileExists(t, old, "the readable copy in the temp dir is gone")
	assert.False(t, migrateTempLog(filepath.Join(logs, name)), "only when the new log doesn't exist yet")

	// A symlink planted at the old name isn't read or followed.
	secret := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(secret, []byte("not yours"), 0o600))
	name2 := "claude_coral_a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d.log"
	require.NoError(t, os.Symlink(secret, filepath.Join(tmp, name2)))
	assert.False(t, migrateTempLog(filepath.Join(logs, name2)))
	assert.NoFileExists(t, filepath.Join(logs, name2))
}

// An empty log path (naming.LogFile refusing an unsafe name) is an error, never '.' (Backend's
// note on #3601).
func TestAgentLogHelpersRefuseAnEmptyPath(t *testing.T) {
	assert.ErrorIs(t, resetLog(""), errNoLogPath)
	assert.ErrorIs(t, createLog(""), errNoLogPath)
	_, err := openLogAppend("")
	assert.ErrorIs(t, err, errNoLogPath)
	assert.False(t, migrateTempLog(""))
}
