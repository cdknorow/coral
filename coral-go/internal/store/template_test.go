package store

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// The store package's own copy of storetest's template (storetest imports
// store, so these tests cannot import it). openTestDB starts every test from
// a copy of one migrated database instead of running the migrations again;
// tests of Open and of the migrations themselves call Open on a fresh path.
var (
	templateOnce  sync.Once
	templateBytes []byte
	templateErr   error
)

func migratedTemplate(t *testing.T) []byte {
	t.Helper()
	templateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "coral-store-template-*")
		if err != nil {
			templateErr = err
			return
		}
		defer os.RemoveAll(dir)
		path := filepath.Join(dir, "template.db")
		db, err := Open(path)
		if err != nil {
			templateErr = err
			return
		}
		if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			db.Close()
			templateErr = err
			return
		}
		if err := db.Close(); err != nil {
			templateErr = err
			return
		}
		templateBytes, templateErr = os.ReadFile(path)
	})
	require.NoError(t, templateErr)
	return templateBytes
}
