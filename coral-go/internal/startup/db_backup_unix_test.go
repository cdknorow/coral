//go:build !windows

package startup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/dbcrypt"
)

// The backups 'coral db compact' and 'coral db rebuild-search' write next to sessions.db hold every session's text: they are private to
// the user (0600) whatever the process's umask, like the database itself, not 0644 as VACUUM INTO makes a file it creates (#4735).
func TestDatabaseBackupsArePrivateToTheUser(t *testing.T) {
	old := syscall.Umask(0o022) // a login shell's umask
	t.Cleanup(func() { syscall.Umask(old) })
	t.Run("rebuild-search", func(t *testing.T) {
		cfg := searchEnv(t, true)
		require.NoError(t, os.Chmod(cfg.DBPath, 0o600))
		var out bytes.Buffer
		require.NoError(t, RebuildSearch(context.Background(), cfg, &out), out.String())
		backups, _ := filepath.Glob(filepath.Join(filepath.Dir(cfg.DBPath), "*.before-rebuild-search-*"))
		require.Len(t, backups, 1)
		fi, err := os.Stat(backups[0])
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), "the backup of the database is as private as the database")
		assert.Greater(t, fi.Size(), int64(0), "and it holds the database")
	})
	t.Run("compact", func(t *testing.T) {
		cfg := searchEnv(t, false)
		require.NoError(t, os.Chmod(cfg.DBPath, 0o600))
		var out bytes.Buffer
		require.NoError(t, Compact(context.Background(), cfg, &out), out.String())
		backups, _ := filepath.Glob(filepath.Join(filepath.Dir(cfg.DBPath), "*.before-compact-*"))
		require.Len(t, backups, 1)
		fi, err := os.Stat(backups[0])
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
		assert.Greater(t, fi.Size(), int64(0))
	})
}

// A backup name that already exists is refused before anything is changed, as VACUUM INTO refuses it.
func TestNewPrivateBackupRefusesAnExistingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "b.db")
	require.NoError(t, newPrivateBackup(p))
	assert.Error(t, newPrivateBackup(p))
}

// An encrypted database is refused before anything is written: a backup of it made by VACUUM INTO of a plaintext connection would be a
// plaintext copy, and the rebuild does not support it (like compact).
func TestRebuildSearchRefusesAnEncryptedDatabaseAndWritesNoBackup(t *testing.T) {
	cfg := searchEnv(t, true)
	require.NoError(t, os.WriteFile(filepath.Join(cfg.CoralDir(), dbcrypt.BootstrapName), []byte(`{"database_encryption":"key_file"}`), 0o600))
	var out bytes.Buffer
	err := RebuildSearch(context.Background(), cfg, &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encrypted")
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(cfg.DBPath), "*.before-rebuild-search-*"))
	assert.Empty(t, backups, "no copy of the database was made")
}
