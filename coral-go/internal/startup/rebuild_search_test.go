package startup

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/background"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store"
)

// searchEnv is a data folder whose sessions.db has the search text of 60 sessions; damage deletes one page of the FTS5 index,
// as a lost page would leave it ("fts5: corruption found reading blob N from table session_fts").
func searchEnv(t *testing.T, damage bool) *config.Config {
	t.Helper()
	cfg := config.Load(t.TempDir())
	ctx := context.Background()
	db, err := store.OpenWithKey(ctx, cfg.DBPath, "")
	require.NoError(t, err)
	s := store.NewSessionStore(db)
	words := strings.Fields("function handler request response error cache index session transcript window cursor refactor rename extract inline fixture assert mutation oracle floor build vet lint format alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima")
	for i := 0; i < 60; i++ {
		var b strings.Builder
		for b.Len() < 24<<10 {
			b.WriteString(words[(i*7+b.Len()*13)%len(words)])
			b.WriteString(fmt.Sprintf(" w%d ", (b.Len()*31+i)%3000))
		}
		require.NoError(t, s.UpsertFTS(ctx, fmt.Sprintf("sess-%d", i), b.String()))
	}
	if damage {
		var victim int64
		require.NoError(t, db.GetContext(ctx, &victim, `SELECT id FROM session_fts_data WHERE id > 100 ORDER BY id LIMIT 1 OFFSET 3`))
		_, err = db.ExecContext(ctx, `DELETE FROM session_fts_data WHERE id = ?`, victim)
		require.NoError(t, err)
		problems, err := background.QuickCheck(ctx, db.DB)
		require.NoError(t, err)
		require.NotEmpty(t, problems, "the damage is there before the repair")
		require.Contains(t, problems[0], "session_fts")
	}
	require.NoError(t, db.Close())
	return cfg
}

// #4735: 'coral db rebuild-search' backs up, rebuilds the search index from the text stored with it and checks the result; the
// damage 'coral db compact' copies is gone, every session is still searchable, and only the search index changed.
func TestRebuildSearchRepairsADamagedIndexAndKeepsEverySession(t *testing.T) {
	cfg := searchEnv(t, true)
	ctx := context.Background()
	var out bytes.Buffer
	require.NoError(t, RebuildSearch(ctx, cfg, &out), out.String())
	assert.Contains(t, out.String(), "Checks before: fts5: corruption found reading blob")
	assert.Contains(t, out.String(), "Done: the checks pass and 60 sessions are searchable (60 before)")

	backups, err := filepath.Glob(filepath.Join(filepath.Dir(cfg.DBPath), "sessions.before-rebuild-search-*.db"))
	require.NoError(t, err)
	require.Len(t, backups, 1)
	bdb, err := store.OpenWithKey(ctx, backups[0], "")
	require.NoError(t, err, "the backup is a usable database")
	bproblems, err := background.QuickCheck(ctx, bdb.DB)
	require.NoError(t, err)
	assert.NotEmpty(t, bproblems, "the backup is the database as it was, damage included")
	bdb.Close()

	db, err := store.OpenWithKey(ctx, cfg.DBPath, "")
	require.NoError(t, err)
	defer db.Close()
	problems, err := background.QuickCheck(ctx, db.DB)
	require.NoError(t, err)
	assert.Empty(t, problems)
	var n, hits int
	require.NoError(t, db.GetContext(ctx, &n, `SELECT COUNT(DISTINCT session_id) FROM session_fts`))
	assert.Equal(t, 60, n)
	require.NoError(t, db.GetContext(ctx, &hits, `SELECT COUNT(*) FROM session_fts WHERE session_fts MATCH 'alpha'`))
	assert.Positive(t, hits, "a search finds sessions again")
}

// A sound database is rebuilt too when asked, and the command says so.
func TestRebuildSearchOnASoundDatabase(t *testing.T) {
	cfg := searchEnv(t, false)
	var out bytes.Buffer
	require.NoError(t, RebuildSearch(context.Background(), cfg, &out), out.String())
	assert.Contains(t, out.String(), "passes its checks now")
	assert.Contains(t, out.String(), "Done: the checks pass and 60 sessions are searchable")
}

// It refuses while a Coral server runs on the data folder, and changes nothing.
func TestRebuildSearchRefusesWhileCoralRuns(t *testing.T) {
	cfg := searchEnv(t, true)
	lockPath := filepath.Join(cfg.CoralDir(), "coral-server.lock")
	lock, err := acquireInstanceLock(lockPath)
	require.NoError(t, err)
	defer lock.close(lockPath)
	var out bytes.Buffer
	err = RebuildSearch(context.Background(), cfg, &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "while Coral is running")
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(cfg.DBPath), "sessions.before-rebuild-search-*.db"))
	assert.Empty(t, backups, "no backup, nothing touched")
}
