package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3628 F8: a fresh database has no connected_apps table; one an older Coral left (OAuth
// secrets and tokens) is dropped with no byte of its rows left in the file or its WAL, and a
// later start changes nothing.
func TestDropConnectedAppsLeavesNoTrace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	require.NoError(t, os.WriteFile(path, migratedTemplate(t), 0o644))
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	var tables int
	require.NoError(t, db.GetContext(ctx, &tables, "SELECT COUNT(*) FROM sqlite_master WHERE name = 'connected_apps'"))
	assert.Zero(t, tables, "the schema no longer creates it")

	exec := func(q string, args ...any) {
		t.Helper()
		_, err := db.ExecContext(ctx, q, args...)
		require.NoError(t, err)
	}
	exec(`CREATE TABLE connected_apps (id INTEGER PRIMARY KEY AUTOINCREMENT, provider_id TEXT NOT NULL, name TEXT NOT NULL, client_id TEXT NOT NULL, client_secret TEXT NOT NULL, scopes TEXT NOT NULL DEFAULT '', access_token TEXT NOT NULL DEFAULT '', refresh_token TEXT NOT NULL DEFAULT '', token_expiry TEXT, account_email TEXT DEFAULT '', account_name TEXT DEFAULT '', status TEXT NOT NULL DEFAULT 'active', created_at TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE(provider_id, name))`)
	markers := []string{"CAMARK-SECRET-3f9a1c7e", "CAMARK-REFRESH-b2d84e10"}
	pad := strings.Repeat("p", 12000) // overflow pages: freed whole, zeroed only by secure_delete
	exec(`INSERT INTO connected_apps (provider_id, name, client_id, client_secret, refresh_token, created_at, updated_at) VALUES ('g', 'a', 'c', ?, 'r', 'x', 'x')`, pad+markers[0])
	exec("PRAGMA wal_checkpoint(PASSIVE)")
	exec(`INSERT INTO connected_apps (provider_id, name, client_id, client_secret, refresh_token, created_at, updated_at) VALUES ('g', 'b', 'c', 's', ?, 'x', 'x')`, markers[1])

	n, err := DropConnectedApps(ctx, db)
	require.NoError(t, err)
	assert.Equal(t, 3, n, "two rows and the table")
	for _, f := range []string{path, path + "-wal"} {
		b, err := os.ReadFile(f)
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		for _, m := range markers {
			assert.False(t, bytes.Contains(b, []byte(m)), "%s still holds %s", filepath.Base(f), m)
		}
	}
	require.NoError(t, db.GetContext(ctx, &tables, "SELECT COUNT(*) FROM sqlite_master WHERE name = 'connected_apps'"))
	assert.Zero(t, tables)
	n, err = DropConnectedApps(ctx, db)
	require.NoError(t, err)
	assert.Zero(t, n, "a later start changes nothing")
}
