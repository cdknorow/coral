package board

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The migration chain (#5392): messageboard.db as an old release created it (testdata/migration/<tag>/messageboard.sql is `sqlite3 .dump` of the file the
// release's own code made, plus a few rows through SQL; no live data), opened with the current code. Every migration applies, the rows and their key
// fields survive, the schema has everything a new install has, and a second open changes nothing.

// dbShape is a database's schema text, its columns and a digest of every row of every table.
type dbShape struct {
	schema map[string]string   // "table:name" or "index:name" -> its CREATE statement
	cols   map[string][]string // table -> column names
	counts map[string]int
	rows   map[string]string // table -> every row, one string
}

func readShape(t *testing.T, path string) dbShape {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	s := dbShape{schema: map[string]string{}, cols: map[string][]string{}, counts: map[string]int{}, rows: map[string]string{}}
	rows, err := db.Query(`SELECT type, name, COALESCE(sql, '') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	require.NoError(t, err)
	var tables []string
	for rows.Next() {
		var typ, name, ddl string
		require.NoError(t, rows.Scan(&typ, &name, &ddl))
		s.schema[typ+":"+name] = ddl
		if typ == "table" {
			tables = append(tables, name)
		}
	}
	require.NoError(t, rows.Err())
	rows.Close()
	for _, tbl := range tables {
		cr, err := db.Query(fmt.Sprintf(`SELECT name FROM pragma_table_info('%s') ORDER BY cid`, tbl))
		require.NoError(t, err)
		for cr.Next() {
			var c string
			require.NoError(t, cr.Scan(&c))
			s.cols[tbl] = append(s.cols[tbl], c)
		}
		cr.Close()
		var n int
		require.NoError(t, db.QueryRow(fmt.Sprintf(`SELECT count(*) FROM "%s"`, tbl)).Scan(&n))
		s.counts[tbl] = n
		dr, err := db.Query(fmt.Sprintf(`SELECT * FROM "%s" ORDER BY 1`, tbl))
		require.NoError(t, err)
		n = len(s.cols[tbl])
		var b strings.Builder
		for dr.Next() {
			vals := make([]any, n)
			ptrs := make([]any, n)
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			require.NoError(t, dr.Scan(ptrs...))
			fmt.Fprintf(&b, "%v\n", vals)
		}
		dr.Close()
		s.rows[tbl] = b.String()
	}
	return s
}

func loadDump(t *testing.T, dbPath, dumpPath string) {
	t.Helper()
	script, err := os.ReadFile(dumpPath)
	require.NoError(t, err)
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = db.Exec(string(script))
	require.NoError(t, err, "the dump loads")
	require.NoError(t, db.Close())
}

func scalar(t *testing.T, path, q string, args ...any) string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	var v sql.NullString
	require.NoError(t, db.QueryRow(q, args...).Scan(&v))
	return v.String
}

func TestMigrationChainFromOldReleases(t *testing.T) {
	fresh := filepath.Join(t.TempDir(), "messageboard.db")
	fs, err := NewStoreWithKey(fresh, "")
	require.NoError(t, err)
	require.NoError(t, fs.Close())
	want := readShape(t, fresh)

	for _, tag := range []string{"v1.4.80", "v1.4.110"} {
		t.Run(tag, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "messageboard.db")
			loadDump(t, path, filepath.Join("testdata", "migration", tag, "messageboard.sql"))
			before := readShape(t, path)
			require.Equal(t, 4, before.counts["board_messages"], "the fixture's own rows")

			open := func() {
				s, err := NewStoreWithKey(path, "")
				require.NoError(t, err, "the current code opens a %s database", tag)
				require.NoError(t, s.Close())
			}
			open()
			after := readShape(t, path)

			if tag == "v1.4.80" {
				assert.NotEqual(t, before.schema, after.schema, "the oldest fixture is migrated: the open changed its schema")
			}
			for tbl, cols := range want.cols {
				require.Contains(t, after.cols, tbl, "table %s", tbl)
				for _, c := range cols {
					assert.Contains(t, after.cols[tbl], c, "%s.%s", tbl, c)
				}
			}
			for key := range want.schema {
				if strings.HasPrefix(key, "index:") {
					assert.Contains(t, after.schema, key, "index %s", key)
				}
			}

			for _, tbl := range []string{"board_subscribers", "board_messages", "board_tasks"} {
				assert.Equal(t, before.counts[tbl], after.counts[tbl], "rows of %s", tbl)
			}
			assert.Equal(t, 2, after.counts["board_subscribers"])
			assert.Equal(t, 3, after.counts["board_tasks"])
			for i := 1; i <= 4; i++ {
				assert.Equal(t, "fixture-board", scalar(t, path, `SELECT project FROM board_messages WHERE content = ?`, fmt.Sprintf("fixture message %d", i)))
			}
			for i := 1; i <= 3; i++ {
				title := fmt.Sprintf("Fixture task %d", i)
				assert.Equal(t, "fixture-board", scalar(t, path, `SELECT board_id FROM board_tasks WHERE title = ?`, title))
				assert.Equal(t, "pending", scalar(t, path, `SELECT status FROM board_tasks WHERE title = ?`, title))
				assert.Equal(t, "Fixture Role 1", scalar(t, path, `SELECT created_by FROM board_tasks WHERE title = ?`, title))
			}
			// a subscriber is keyed by its title now: the migration moved the old session id there, and kept the title and the board
			assert.Equal(t, "Fixture Role 2", scalar(t, path, `SELECT session_id FROM board_subscribers WHERE job_title = 'Fixture Role 2' AND project = 'fixture-board'`))
			assert.Equal(t, "Fixture Role 2", scalar(t, path, `SELECT subscriber_id FROM board_subscribers WHERE job_title = 'Fixture Role 2' AND project = 'fixture-board'`))
			assert.Equal(t, "ok", scalar(t, path, `PRAGMA integrity_check`))

			open()
			again := readShape(t, path)
			assert.Equal(t, after.schema, again.schema, "the schema did not change on the second open")
			assert.Equal(t, after.counts, again.counts)
			assert.Equal(t, after.rows, again.rows, "no row changed on the second open")
		})
	}
}
