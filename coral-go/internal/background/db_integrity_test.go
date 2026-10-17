package background

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// integrityDB makes a database file with a table spread over many pages; damage writes over
// its second page (the table's), as a disk or a crashed copy might.
func integrityDB(t *testing.T, damage bool) *sqlx.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "check.db")
	db, err := sqlx.Open("sqlite", path)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT);
		WITH RECURSIVE c(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM c WHERE i < 2000) INSERT INTO t(v) SELECT hex(randomblob(40)) FROM c;`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	if damage {
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		require.NoError(t, err)
		_, err = f.WriteAt([]byte(strings.Repeat("\xde\xad", 2048)), 4096)
		require.NoError(t, err)
		require.NoError(t, f.Close())
	}
	db, err = sqlx.Open("sqlite", path+"?_pragma=query_only(1)")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

func resetIntegrity(t *testing.T) {
	t.Helper()
	integrity.set("good.db", "")
	integrity.set("bad.db", "")
	t.Cleanup(func() { integrity.set("good.db", ""); integrity.set("bad.db", "") })
}

// #4157: a sound database reports nothing; a damaged one is named on the dashboard with what
// to do, and the file is left as it was.
func TestDBIntegrityFindsDamageAndChangesNothing(t *testing.T) {
	resetIntegrity(t)
	good, bad := integrityDB(t, false), integrityDB(t, true)

	problems, err := QuickCheck(context.Background(), good)
	require.NoError(t, err)
	assert.Empty(t, problems)

	NewDBIntegrity(map[string]*sqlx.DB{"good.db": good}).RunOnce(context.Background())
	assert.Empty(t, DatabaseWarning(), "a sound database: no notice")

	var badPath string
	var seq int
	var name string
	require.NoError(t, bad.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &badPath))
	before, err := os.ReadFile(badPath)
	require.NoError(t, err)

	NewDBIntegrity(map[string]*sqlx.DB{"good.db": good, "bad.db": bad}).RunOnce(context.Background())
	w := DatabaseWarning()
	assert.Contains(t, w, "Coral found damage in bad.db (")
	assert.NotContains(t, w, "*** in database", "the header line is not the problem")
	assert.NotContains(t, w, "good.db")
	assert.Contains(t, w, "coral db compact")
	assert.Contains(t, w, "has not changed anything")

	after, err := os.ReadFile(badPath)
	require.NoError(t, err)
	assert.Equal(t, before, after, "the damaged file is left as it was")
}

// Coral stopping ends the wait before the check, at once.
func TestDBIntegrityStopsWithCoral(t *testing.T) {
	resetIntegrity(t)
	j := NewDBIntegrity(map[string]*sqlx.DB{"bad.db": integrityDB(t, true)})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { j.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop with its context")
	}
	assert.Empty(t, DatabaseWarning(), "no check ran")
}
