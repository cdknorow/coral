package startup

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/dbcrypt"
	"github.com/subgentic/coral/internal/store"
)

// Compact shrinks sessions.db offline (`coral db compact`, #3261). Deleted rows
// (call metrics retention, for one) go to SQLite's free list and are reused, so
// the file stops growing but never shrinks by itself. VACUUM rewrites it, which
// holds the write lock for minutes on a large file: never inside a running
// server. So Compact takes the server's own data-folder lock and keeps it until
// it is done: it refuses while Coral runs, and Coral cannot start meanwhile.
//
// It first writes a backup (VACUUM INTO, a consistent copy) next to the
// database, then VACUUMs in place and truncates the write-ahead log. SQLite's
// VACUUM is transactional: if it fails (a full disk), the database is as it was.
func Compact(ctx context.Context, cfg *config.Config, out io.Writer) error {
	coralDir := cfg.CoralDir()
	lockPath := filepath.Join(coralDir, "coral-server.lock")
	lock, err := acquireInstanceLock(lockPath)
	if err != nil {
		return fmt.Errorf("cannot compact while Coral is running for %s (%v): stop it first", coralDir, err)
	}
	defer lock.close(lockPath)

	security, err := dbcrypt.Load(coralDir)
	if err != nil {
		return err
	}
	if mode := security.DatabaseEncryption; mode != "" && mode != "disabled" {
		return fmt.Errorf("the database is encrypted (%s): compacting an encrypted database is not supported yet", mode)
	}
	if _, err := os.Stat(cfg.DBPath); err != nil {
		return fmt.Errorf("no database at %s: %w", cfg.DBPath, err)
	}

	before := dbFilesSize(cfg.DBPath)
	db, err := store.OpenWithKey(ctx, cfg.DBPath, "")
	if err != nil {
		return fmt.Errorf("open %s: %w", cfg.DBPath, err)
	}
	defer db.Close()

	backup := filepath.Join(filepath.Dir(cfg.DBPath),
		strings.TrimSuffix(filepath.Base(cfg.DBPath), ".db")+".before-compact-"+time.Now().UTC().Format("20060102T150405Z")+".db")
	fmt.Fprintf(out, "Database: %s (%s)\nBacking up to %s ...\n", cfg.DBPath, humanBytes(before), backup)
	started := time.Now()
	if err := newPrivateBackup(backup); err != nil {
		return fmt.Errorf("backup failed, nothing was changed: %w", err)
	}
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", backup); err != nil {
		os.Remove(backup)
		return fmt.Errorf("backup failed, nothing was changed: %w", err)
	}
	fmt.Fprintf(out, "Backup written (%s) in %s.\nCompacting ...\n", humanBytes(fileSize(backup)), time.Since(started).Round(time.Second))
	started = time.Now()
	if _, err := db.ExecContext(ctx, "VACUUM"); err != nil {
		return fmt.Errorf("compact failed; the database is unchanged and the backup is at %s: %w", backup, err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		fmt.Fprintf(out, "warning: could not truncate the write-ahead log: %v\n", err)
	}
	after := dbFilesSize(cfg.DBPath)
	fmt.Fprintf(out, "Compacted in %s: %s -> %s (saved %s).\nThe backup %s can be deleted once Coral works as before.\n",
		time.Since(started).Round(time.Second), humanBytes(before), humanBytes(after), humanBytes(before-after), backup)
	return nil
}

func fileSize(p string) int64 {
	if fi, err := os.Stat(p); err == nil {
		return fi.Size()
	}
	return 0
}

// dbFilesSize is the database with its write-ahead log and shared-memory file.
func dbFilesSize(p string) int64 {
	return fileSize(p) + fileSize(p+"-wal") + fileSize(p+"-shm")
}

func humanBytes(n int64) string {
	switch {
	case n < 0:
		return "-" + humanBytes(-n)
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
