package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two changes that each add a column are merged by hand into one columnMigrations list (#6089's hook_dir and #6103's proof_key were): this catches a merge that lists a column twice, loses one, or
// makes one fail to be added after another. A database made before all of them (every column dropped), and one missing only that column, are opened; every listed column ends up once, and opening
// again changes nothing and fails nothing.
func TestColumnMigrationsAddEveryColumnOnceFromAnyOlderDatabase(t *testing.T) {
	seen := map[[2]string]bool{}
	for _, m := range columnMigrations {
		k := [2]string{m.table, m.column}
		assert.False(t, seen[k], "%s.%s is listed twice", m.table, m.column)
		seen[k] = true
	}

	ctx := context.Background()
	columns := func(db *DB, table string) map[string]int {
		var rows []struct {
			Name string `db:"name"`
		}
		require.NoError(t, db.SelectContext(ctx, &rows, `SELECT name FROM pragma_table_info('`+table+`')`))
		out := map[string]int{}
		for _, r := range rows {
			out[r.Name]++
		}
		return out
	}
	check := func(t *testing.T, drop func(*DB, [2]string) bool) {
		p := filepath.Join(t.TempDir(), "m.db")
		db, err := OpenWithKey(ctx, p, "")
		require.NoError(t, err)
		for _, m := range columnMigrations {
			drop(db, [2]string{m.table, m.column})
		}
		require.NoError(t, db.Close())
		for i := 0; i < 3; i++ { // the first open adds what is missing; the next two change nothing
			db, err = OpenWithKey(ctx, p, "")
			require.NoError(t, err, "open %d", i)
			for _, m := range columnMigrations {
				assert.Equal(t, 1, columns(db, m.table)[m.column], "open %d: %s.%s", i, m.table, m.column)
			}
			require.NoError(t, db.Close())
		}
	}
	dropColumn := func(db *DB, c [2]string) bool {
		_, err := db.ExecContext(ctx, "ALTER TABLE "+c[0]+" DROP COLUMN "+c[1])
		return err == nil // a column SQLite will not drop (an index or a constraint uses it) stays: the test still opens it
	}

	t.Run("a database made before all of them", func(t *testing.T) { check(t, dropColumn) })
	for _, only := range columnMigrations {
		only := [2]string{only.table, only.column}
		t.Run("a database missing only "+only[0]+"."+only[1], func(t *testing.T) {
			check(t, func(db *DB, c [2]string) bool { return c == only && dropColumn(db, c) })
		})
	}
}
