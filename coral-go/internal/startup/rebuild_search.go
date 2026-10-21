package startup

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/background"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/dbcrypt"
	"github.com/subgentic/coral/internal/store"
)

// RebuildSearch rebuilds the full-text search index of sessions.db offline (`coral db rebuild-search`, #4735). session_fts is an
// FTS5 table with its own content table: the text of each session is stored in it, and the index over that text is derived from
// it, so FTS5's 'rebuild' makes the index again from the stored text and loses nothing. It is the repair for damage in the
// index ("fts5: corruption found reading blob N"); `coral db compact` copies such damage as it is.
//
// Like Compact it takes the server's own data-folder lock (it refuses while Coral runs, and Coral cannot start meanwhile), writes a
// backup first (VACUUM INTO), then rebuilds, then checks the result and says what it found. The rebuild is one transaction: if
// it fails, the database is as it was, and the message says what to do next.
func RebuildSearch(ctx context.Context, cfg *config.Config, out io.Writer) error {
	coralDir := cfg.CoralDir()
	lockPath := filepath.Join(coralDir, "coral-server.lock")
	lock, err := acquireInstanceLock(lockPath)
	if err != nil {
		return fmt.Errorf("cannot rebuild the search index while Coral is running for %s (%v): stop it first", coralDir, err)
	}
	defer lock.close(lockPath)

	security, err := dbcrypt.Load(coralDir)
	if err != nil {
		return err
	}
	if mode := security.DatabaseEncryption; mode != "" && mode != "disabled" {
		return fmt.Errorf("the database is encrypted (%s): rebuilding the search index of an encrypted database is not supported yet", mode)
	}
	if _, err := os.Stat(cfg.DBPath); err != nil {
		return fmt.Errorf("no database at %s: %w", cfg.DBPath, err)
	}
	db, err := store.OpenWithKey(ctx, cfg.DBPath, "")
	if err != nil {
		return fmt.Errorf("open %s: %w", cfg.DBPath, err)
	}
	defer db.Close()

	var sessions int
	if err := db.GetContext(ctx, &sessions, `SELECT COUNT(*) FROM session_fts_content`); err != nil {
		return fmt.Errorf("the search text cannot be read (%v): the index cannot be rebuilt from it; see the end of this message for what to do", err)
	}
	problems, _ := background.QuickCheck(ctx, db.DB)
	if len(problems) == 0 {
		fmt.Fprintf(out, "Database: %s (%s)\nThe search index passes its checks now; rebuilding it anyway as asked (%d sessions of search text).\n", cfg.DBPath, humanBytes(dbFilesSize(cfg.DBPath)), sessions)
	} else {
		fmt.Fprintf(out, "Database: %s (%s)\nChecks before: %s\n", cfg.DBPath, humanBytes(dbFilesSize(cfg.DBPath)), strings.Join(problems, "; "))
	}

	backup := filepath.Join(filepath.Dir(cfg.DBPath),
		strings.TrimSuffix(filepath.Base(cfg.DBPath), ".db")+".before-rebuild-search-"+time.Now().UTC().Format("20060102T150405Z")+".db")
	fmt.Fprintf(out, "Backing up to %s ...\n", backup)
	started := time.Now()
	if err := newPrivateBackup(backup); err != nil {
		return fmt.Errorf("backup failed, nothing was changed: %w", err)
	}
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", backup); err != nil {
		os.Remove(backup)
		return fmt.Errorf("backup failed, nothing was changed: %w", err)
	}
	fmt.Fprintf(out, "Backup written (%s) in %s.\nRebuilding the search index ...\n", humanBytes(fileSize(backup)), time.Since(started).Round(time.Millisecond))
	started = time.Now()
	if _, err := db.ExecContext(ctx, `INSERT INTO session_fts(session_fts) VALUES('rebuild')`); err != nil {
		return fmt.Errorf("the rebuild failed and the database is unchanged (the backup is at %s): %w\n"+
			"If the search text itself is damaged, stop Coral and drop the search table, which Coral makes again and fills again from the transcripts that still exist when it starts:\n"+
			"  sqlite3 %s \"DROP TABLE session_fts;\"", backup, err, cfg.DBPath)
	}
	fmt.Fprintf(out, "Rebuilt in %s. Checking ...\n", time.Since(started).Round(time.Millisecond))
	if _, err := db.ExecContext(ctx, `INSERT INTO session_fts(session_fts) VALUES('integrity-check')`); err != nil {
		return fmt.Errorf("the rebuilt index still fails FTS5's own check (the backup is at %s): %w", backup, err)
	}
	after, err := background.QuickCheck(ctx, db.DB)
	if err != nil {
		return fmt.Errorf("the database could not be checked after the rebuild (the backup is at %s): %w", backup, err)
	}
	if len(after) > 0 {
		return fmt.Errorf("the database still reports: %s (the backup is at %s)", strings.Join(after, "; "), backup)
	}
	var rows int
	if err := db.GetContext(ctx, &rows, `SELECT COUNT(DISTINCT session_id) FROM session_fts`); err != nil {
		return fmt.Errorf("the rebuilt index cannot be read (the backup is at %s): %w", backup, err)
	}
	fmt.Fprintf(out, "Done: the checks pass and %d sessions are searchable (%d before). Nothing but the search index changed.\nThe backup %s can be deleted once Coral works as before.\n", rows, sessions, backup)
	return nil
}
