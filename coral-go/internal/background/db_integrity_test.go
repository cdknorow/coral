package background

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/subgentic/coral/internal/store"
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

// #4735: a problem the read pool reports is checked again on the writer's connection before it is shown: damage the second
// check also finds is shown, a problem it does not find is not (and clears an earlier notice).
func TestDBIntegrityConfirmsOnTheWriterBeforeWarning(t *testing.T) {
	resetIntegrity(t)
	good, bad := integrityDB(t, false), integrityDB(t, true)

	NewDBIntegrity(map[string]*sqlx.DB{"bad.db": bad}).ConfirmOn(map[string]*sqlx.DB{"bad.db": good}).RunOnce(context.Background())
	assert.Empty(t, DatabaseWarning(), "the first check said damaged, the confirming one found nothing: no notice")

	integrity.set("bad.db", "an earlier notice")
	NewDBIntegrity(map[string]*sqlx.DB{"bad.db": bad}).ConfirmOn(map[string]*sqlx.DB{"bad.db": good}).RunOnce(context.Background())
	assert.Empty(t, DatabaseWarning(), "a confirmed-ok check clears the notice")

	NewDBIntegrity(map[string]*sqlx.DB{"bad.db": bad}).ConfirmOn(map[string]*sqlx.DB{"bad.db": bad}).RunOnce(context.Background())
	assert.Contains(t, DatabaseWarning(), "Coral found damage in bad.db (", "damage both checks find is shown")

	resetIntegrity(t)
	NewDBIntegrity(map[string]*sqlx.DB{"bad.db": bad}).RunOnce(context.Background())
	assert.Contains(t, DatabaseWarning(), "Coral found damage in bad.db (", "without a confirming connection the first check stands, as before")
}

// #4735: the real thing. While the indexer rewrites session_fts, quick_check on the read pool reports "fts5: corruption found
// reading blob N" on a sound database (most checks did, in this test's first run: 144 of 146); the check with a confirmation on the
// writer's connection does not warn. If the read pool never produces the false report in the tries, there is nothing to confirm
// and the test is skipped.
func TestDBIntegrityDoesNotAlarmWhileTheIndexerWrites(t *testing.T) {
	resetIntegrity(t)
	ctx := context.Background()
	db, err := store.OpenWithKey(ctx, filepath.Join(t.TempDir(), "sessions.db"), "")
	require.NoError(t, err)
	defer db.Close()
	s := store.NewSessionStore(db)
	words := strings.Fields("function handler request response error cache index session transcript window cursor refactor rename extract inline fixture assert mutation oracle floor build vet lint format alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima")
	body := func(i, n int) string {
		var b strings.Builder
		for b.Len() < n {
			b.WriteString(words[(i*7+b.Len()*13)%len(words)])
			b.WriteString(fmt.Sprintf(" w%d ", (b.Len()*31+i+n)%7000))
		}
		return b.String()
	}
	for i := 0; i < 100; i++ {
		require.NoError(t, s.UpsertFTS(ctx, fmt.Sprintf("sess-%d", i), body(i, 32<<10)))
	}
	var stop atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // the indexer: a changed session's text is rewritten
		defer wg.Done()
		for round := 0; !stop.Load(); round++ {
			_ = s.UpsertFTS(ctx, fmt.Sprintf("sess-%d", round%100), body(round%100, 32<<10+round%4096))
		}
	}()
	defer func() { stop.Store(true); wg.Wait() }()
	falseReports := 0
	for try := 0; try < 40; try++ {
		if problems, err := QuickCheck(ctx, db.Reader()); err == nil && len(problems) > 0 {
			falseReports++
			resetIntegrity(t)
			NewDBIntegrity(map[string]*sqlx.DB{"sessions.db": db.Reader()}).ConfirmOn(map[string]*sqlx.DB{"sessions.db": db.DB}).RunOnce(ctx)
			require.Empty(t, DatabaseWarning(), "the writer's check found nothing wrong: no notice (the read pool said %v)", problems)
			if falseReports >= 3 {
				break
			}
		}
	}
	if falseReports == 0 {
		t.Skip("the read pool did not report the false damage in 40 tries")
	}
}

// #4735: damage in the search index is pointed to `coral db rebuild-search` (compact would copy it); other damage is not.
func TestDatabaseWarningPointsSearchIndexDamageToRebuildSearch(t *testing.T) {
	resetIntegrity(t)
	integrity.set("bad.db", `fts5: corruption found reading blob 1 from table "session_fts"`)
	assert.Contains(t, DatabaseWarning(), "coral db rebuild-search")
	assert.Contains(t, DatabaseWarning(), `"coral db compact" does not fix it`)
	integrity.set("bad.db", "database disk image is malformed")
	assert.NotContains(t, DatabaseWarning(), "rebuild-search")
}
