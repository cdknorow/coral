package startup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/dbcrypt"
	"github.com/cdknorow/coral/internal/store"
)

// compactEnv is a data folder with a database that has lots of free pages.
func compactEnv(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Load(dir)
	db, err := store.Open(cfg.DBPath)
	require.NoError(t, err)
	_, err = db.Exec(`WITH RECURSIVE c(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM c WHERE i < 50000)
		INSERT INTO call_metrics(call_type, operation, method, created_at) SELECT 'api', '/api/some/long/operation/name', 'GET', '2026-01-01T00:00:00Z' FROM c`)
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM call_metrics`)
	require.NoError(t, err)
	_, err = db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	return cfg
}

// #3261: 'coral db compact' backs up, then shrinks the file.
func TestCompactShrinksAndBacksUp(t *testing.T) {
	cfg := compactEnv(t)
	before := dbFilesSize(cfg.DBPath)
	var out bytes.Buffer
	require.NoError(t, Compact(context.Background(), cfg, &out))
	after := dbFilesSize(cfg.DBPath)
	assert.Less(t, after, before/2, "free pages reclaimed: %d -> %d\n%s", before, after, out.String())
	assert.Contains(t, out.String(), "Compacted in")

	backups, err := filepath.Glob(filepath.Join(filepath.Dir(cfg.DBPath), "sessions.before-compact-*.db"))
	require.NoError(t, err)
	require.Len(t, backups, 1)
	bdb, err := store.Open(backups[0])
	require.NoError(t, err, "the backup is a usable database")
	bdb.Close()

	db, err := store.Open(cfg.DBPath)
	require.NoError(t, err, "the database still opens")
	db.Close()
	assert.NoFileExists(t, filepath.Join(cfg.CoralDir(), "coral-server.lock"), "the lock is released")
}

// Never while a server runs on the same data folder (it holds the lock).
func TestCompactRefusesWhileCoralRuns(t *testing.T) {
	cfg := compactEnv(t)
	lockPath := filepath.Join(cfg.CoralDir(), "coral-server.lock")
	lock, err := acquireInstanceLock(lockPath)
	require.NoError(t, err)
	defer lock.close(lockPath)
	before := dbFilesSize(cfg.DBPath)
	err = Compact(context.Background(), cfg, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stop it first")
	assert.Equal(t, before, dbFilesSize(cfg.DBPath), "untouched")
}

func TestCompactRefusesAnEncryptedDatabase(t *testing.T) {
	cfg := compactEnv(t)
	require.NoError(t, os.WriteFile(filepath.Join(cfg.CoralDir(), dbcrypt.BootstrapName), []byte(`{"database_encryption":"key_file"}`), 0o600))
	err := Compact(context.Background(), cfg, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encrypted")
}

func TestCompactNeedsADatabase(t *testing.T) {
	cfg := config.Load(t.TempDir())
	err := Compact(context.Background(), cfg, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no database")
}
