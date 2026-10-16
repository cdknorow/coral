package store

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// minSQLite is the oldest SQLite Coral runs on: 3.53.4 (modernc.org/sqlite v1.60.1) carries
// SQLite's fix for the WAL-reset database corruption, and Coral runs WAL with a writer and a
// read pool (#3678, from #3677).
var minSQLite = [3]int{3, 53, 4}

func sqliteAtLeast(t *testing.T, v string, min [3]int) bool {
	t.Helper()
	parts := strings.Split(v, ".")
	require.Len(t, parts, 3, "sqlite_version() = %q", v)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		require.NoError(t, err, "sqlite_version() = %q", v)
		if n != min[i] {
			return n > min[i]
		}
	}
	return true
}

// Both the writer and the read pool run an SQLite with the WAL-reset fix, in WAL mode.
func TestSQLiteVersionHasTheWALResetFix(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "v.db"))
	require.NoError(t, err)
	defer db.Close()
	ctx := context.Background()

	var writer, reader, mode string
	require.NoError(t, db.DB.GetContext(ctx, &writer, "SELECT sqlite_version()"))
	require.NoError(t, db.Reader().GetContext(ctx, &reader, "SELECT sqlite_version()"))
	require.NoError(t, db.DB.GetContext(ctx, &mode, "PRAGMA journal_mode"))
	t.Logf("sqlite_version() = %s (writer), %s (reader); journal_mode = %s", writer, reader, mode)
	assert.Equal(t, writer, reader)
	assert.True(t, sqliteAtLeast(t, writer, minSQLite), "SQLite %s predates 3.53.4's WAL-reset fix", writer)
	assert.Equal(t, "wal", strings.ToLower(mode))

	assert.True(t, sqliteAtLeast(t, "3.53.4", minSQLite))
	assert.True(t, sqliteAtLeast(t, "3.54.0", minSQLite))
	assert.True(t, sqliteAtLeast(t, "4.0.0", minSQLite))
	assert.False(t, sqliteAtLeast(t, "3.50.4", minSQLite), "v1.43.0's SQLite")
	assert.False(t, sqliteAtLeast(t, "3.53.3", minSQLite))
}
