package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4129: the repair of double-encoded agent_events.detail_json reads every event, so it runs once,
// marked in store_meta, and not on every start (~450 ms on 200k events, cold).
func TestDetailJSONFixupRunsOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	require.NoError(t, os.WriteFile(path, migratedTemplate(t), 0o644))
	open := func() *DB {
		db, err := OpenWithKey(ctx, path, "")
		require.NoError(t, err)
		return db
	}
	detail := func(db *DB, id int64) string {
		var s string
		require.NoError(t, db.GetContext(ctx, &s, `SELECT detail_json FROM agent_events WHERE id = ?`, id))
		return s
	}
	insert := func(db *DB) int64 {
		res, err := db.ExecContext(ctx, `INSERT INTO agent_events (agent_name, session_id, event_type, summary, detail_json, created_at)
			VALUES ('a', 's', 'tool_use', 'x', ?, '2026-10-16T00:00:00Z')`, `"{\"key\":\"val\"}"`)
		require.NoError(t, err)
		id, _ := res.LastInsertId()
		return id
	}

	// A database from before the marker: the repair runs on open and sets it.
	db := open()
	_, err := db.ExecContext(ctx, `DELETE FROM store_meta WHERE key = ?`, detailJSONFixupKey)
	require.NoError(t, err)
	first := insert(db)
	require.NoError(t, db.Close())
	db = open()
	assert.Equal(t, `{"key":"val"}`, detail(db, first), "repaired")
	var marked int
	require.NoError(t, db.GetContext(ctx, &marked, `SELECT COUNT(*) FROM store_meta WHERE key = ?`, detailJSONFixupKey))
	assert.Equal(t, 1, marked)

	// Marked: later opens don't read the events again (a row like that is left as it is).
	second := insert(db)
	require.NoError(t, db.Close())
	db = open()
	defer db.Close()
	assert.Equal(t, `"{\"key\":\"val\"}"`, detail(db, second), "not scanned again")
}
