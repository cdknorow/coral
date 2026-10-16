package storetest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// A new path gets a fully migrated database, the same as store.Open's.
func TestOpenNewPathIsMigrated(t *testing.T) {
	fresh, err := store.OpenWithKey(context.Background(), filepath.Join(t.TempDir(), "fresh.db"), "")
	require.NoError(t, err)
	defer fresh.Close()
	copied, err := Open(t, filepath.Join(t.TempDir(), "nested", "copied.db"))
	require.NoError(t, err)
	defer copied.Close()

	schema := func(db *store.DB) []string {
		var rows []string
		require.NoError(t, db.SelectContext(context.Background(), &rows,
			"SELECT type || ' ' || name || ' ' || COALESCE(sql, '') FROM sqlite_master ORDER BY type, name"))
		return rows
	}
	require.Equal(t, schema(fresh), schema(copied))
}

// An existing database is opened as it is, never replaced by the template.
func TestOpenExistingPathKeepsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mine.db")
	db, err := Open(t, path)
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO user_settings (key, value) VALUES ('storetest_marker', 'kept')")
	require.NoError(t, err)
	require.NoError(t, db.Close())

	again, err := Open(t, path)
	require.NoError(t, err)
	defer again.Close()
	var value string
	require.NoError(t, again.Get(&value, "SELECT value FROM user_settings WHERE key = 'storetest_marker'"))
	require.Equal(t, "kept", value)
}
