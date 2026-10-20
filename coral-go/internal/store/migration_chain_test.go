package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The migration chain (#5392): sessions.db as an old release created it (testdata/migration/<tag>/sessions.sql is `sqlite3 .dump` of the file the
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
	fresh := filepath.Join(t.TempDir(), "sessions.db")
	fdb, err := OpenWithKey(context.Background(), fresh, "")
	require.NoError(t, err)
	require.NoError(t, fdb.Close())
	want := readShape(t, fresh)

	for _, tag := range []string{"v1.4.80", "v1.4.110"} {
		t.Run(tag, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sessions.db")
			loadDump(t, path, filepath.Join("testdata", "migration", tag, "sessions.sql"))
			before := readShape(t, path)
			require.Equal(t, 3, before.counts["live_sessions"], "the fixture's own rows")

			open := func() {
				db, err := OpenWithKey(context.Background(), path, "")
				require.NoError(t, err, "the current code opens a %s database", tag)
				require.NoError(t, db.Close())
			}
			open()
			after := readShape(t, path)

			if tag == "v1.4.80" {
				assert.NotEqual(t, before.schema, after.schema, "the oldest fixture is migrated: the open changed its schema")
			}
			// every table and column a new install has is there
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

			// the rows survive, with their key fields
			for _, tbl := range []string{"live_sessions", "user_settings", "call_metrics"} {
				assert.Equal(t, before.counts[tbl], after.counts[tbl], "rows of %s", tbl)
			}
			assert.Equal(t, 3, after.counts["live_sessions"])
			assert.Equal(t, 5, after.counts["call_metrics"])
			for i := 1; i <= 3; i++ {
				id := fmt.Sprintf("0000000%d-aaaa-4000-8000-00000000000%d", i, i)
				assert.Equal(t, fmt.Sprintf("Fixture Agent %d", i), scalar(t, path, `SELECT display_name FROM live_sessions WHERE session_id = ?`, id))
				assert.Equal(t, fmt.Sprintf("fixture-agent-%d", i), scalar(t, path, `SELECT agent_name FROM live_sessions WHERE session_id = ?`, id))
				assert.Equal(t, "/fixture/work", scalar(t, path, `SELECT working_dir FROM live_sessions WHERE session_id = ?`, id))
			}
			assert.Equal(t, "value-2", scalar(t, path, `SELECT value FROM user_settings WHERE key = 'fixture_setting_2'`))
			assert.Equal(t, "/api/fixture", scalar(t, path, `SELECT operation FROM call_metrics ORDER BY id LIMIT 1`))
			assert.Equal(t, "ok", scalar(t, path, `PRAGMA integrity_check`))

			// a second open is a no-op: the same schema and the same rows, byte for byte
			open()
			again := readShape(t, path)
			assert.Equal(t, after.schema, again.schema, "the schema did not change on the second open")
			assert.Equal(t, after.counts, again.counts)
			assert.Equal(t, after.rows, again.rows, "no row changed on the second open")
			assert.Equal(t, sortedKeys(after.schema), sortedKeys(again.schema))
		})
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
