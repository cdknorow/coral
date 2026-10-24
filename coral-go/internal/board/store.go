// Package board provides the message board store and HTTP handlers.
// It uses a separate SQLite database from the main Coral store.
package board

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/subgentic/coral/internal/testguard"

	"time"

	"github.com/subgentic/coral/internal/dbcrypt"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

// Store provides message board operations with its own SQLite database.
type Store struct {
	db                      *sqlx.DB
	readDB                  *sqlx.DB
	sessionsDB              *sqlx.DB // optional reference to the main sessions DB for cross-DB queries
	completionCheckRunner   CompletionCheckRunner
	completionChecksEnabled bool
	unreadCounts            unreadCountCache
	numbered                numberedCache // each board's numbered agent names (#4598)
	noMentionIndex          bool          // tests: read as before the mention index (#4465)
	mentionMu               sync.Mutex
	mentionIndexAnswers     atomic.Int64 // queries the mention index answered (tests check it is used)
	mentionIndexedTo        atomic.Int64 // the mention index's watermark, -1 until read (see mention_index.go)
	waitChanges             waitSignal
	waitDispatchMu          sync.Mutex     // protects timeout claims/cursor and short wait mutations
	waitTimeoutClaims       map[int64]bool // under waitDispatchMu: claimed until callback/receipt completes
	waitTimeoutCursor       int64          // under waitDispatchMu: failed old targets cannot starve later waits
	stmts                   *hotStmts      // the hot fixed queries, prepared (#4463); nil: unprepared
}

func isInMemoryPath(dbPath string) bool {
	return dbPath == ":memory:" || strings.HasPrefix(dbPath, "file::memory:") || strings.Contains(dbPath, "mode=memory")
}

func (s *Store) reader() *sqlx.DB {
	if s.readDB != nil {
		return s.readDB
	}
	return s.db
}

// Reader returns the read-only connection pool for the board database.
func (s *Store) Reader() *sqlx.DB {
	return s.reader()
}

// ReaderStats returns statistics for the read pool (or writer pool if in-memory).
func (s *Store) ReaderStats() sql.DBStats {
	return s.reader().Stats()
}

// SetSessionsDB sets an optional reference to the main sessions database,
// enabling cross-DB queries for session_id resolution and cost tracking.
// Ping runs SELECT 1 on the board database: with its single connection it waits while a
// query or transaction holds it (the stall watchdog's probe, #3262).
func (s *Store) Ping(ctx context.Context) error {
	var one int
	return s.db.GetContext(ctx, &one, "SELECT 1")
}

func (s *Store) SetSessionsDB(db *sqlx.DB) {
	s.sessionsDB = db
}

// SetCompletionCheckRunner installs the trusted registered-check runner. A
// nil runner makes registered checks fail explicitly as unavailable.
func (s *Store) SetCompletionCheckRunner(runner CompletionCheckRunner) {
	s.completionCheckRunner = runner
}

// SetCompletionChecksEnabled is trusted server configuration used by tests and
// an explicit future rollout. Callers cannot enable checks through task input.
func (s *Store) SetCompletionChecksEnabled(enabled bool) {
	s.completionChecksEnabled = enabled
}

func (s *Store) CompletionChecksEnabled() bool { return s.completionChecksEnabled }

// NewStoreWithKey opens the board database with SQLCipher when key is set.
func NewStoreWithKey(dbPath, key string) (*Store, error) {
	return newStore(dbPath, key, false)
}

// NewStorePrepared opens a board database file that is a copy of one this build already migrated (boardtest makes it): the schema step is not run again. It is for tests,
// which open hundreds of stores: parsing the whole schema each time was a third of a heavy test's CPU. A file that is not a copy of this build's migrated database must not be
// opened with it.
func NewStorePrepared(dbPath string) (*Store, error) {
	return newStore(dbPath, "", true)
}

func newStore(dbPath, key string, schemaDone bool) (*Store, error) {
	testguard.CheckDataPath(dbPath)
	if key == "" {
		if err := dbcrypt.RejectEncryptedFile(dbPath); err != nil {
			return nil, err
		}
	}
	if key != "" {
		if err := dbcrypt.RequireAvailable(); err != nil {
			return nil, err
		}
	}
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create board db directory: %w", err)
	}

	driver := "sqlite"
	// journal_size_limit (16 MiB): the WAL is truncated back after a checkpoint (#4142).
	dsn := dbPath + "?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)&_pragma=cache_size(-8000)&_pragma=journal_size_limit(16777216)"
	if key != "" {
		// The SQLCipher driver's own DSN keys: it ignores _pragma_ ones (#3454).
		driver = dbcrypt.EncryptedDriver
		dsn = dbcrypt.EncryptedDSN(dbPath, key, dbcrypt.DSNOptions{ForeignKeys: false})
	}
	db, err := sqlx.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open board database: %w", err)
	}
	db.SetMaxOpenConns(1)

	s := &Store{db: db, completionCheckRunner: NewLocalRegisteredCheckRunner(os.Getenv("CORAL_CHECK_WORKDIR")), completionChecksEnabled: completionChecksEnabledFromEnv()}
	if !schemaDone {
		if err := s.ensureSchema(context.Background()); err != nil {
			db.Close()
			return nil, err
		}
	}
	if err := s.recoverTaskReadiness(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("recover task readiness: %w", err)
	}

	if isInMemoryPath(dbPath) {
		s.readDB = db
		s.stmts = prepareHotStmts(context.Background(), s.reader())
		s.stmts.prepareWriter(context.Background(), s.db)
		return s, nil
	}

	readDSN := dbPath + "?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)&_pragma=cache_size(-8000)&_pragma=query_only(1)"
	if key != "" {
		readDSN = dbcrypt.EncryptedDSN(dbPath, key, dbcrypt.DSNOptions{ReadOnly: true, ForeignKeys: false})
	}
	readDB, err := sqlx.Open(driver, readDSN)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("open board read database: %w", err)
	}
	readDB.SetMaxOpenConns(4)
	readDB.SetMaxIdleConns(4)
	s.readDB = readDB
	s.stmts = prepareHotStmts(context.Background(), s.reader())
	s.stmts.prepareWriter(context.Background(), s.db)
	return s, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	// The prepared statements first: each holds its pool connections' statements.
	firstErr := s.stmts.close()
	if s.readDB != nil && s.readDB != s.db {
		if err := s.readDB.Close(); err != nil {
			firstErr = err
		}
	}
	if err := s.db.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// EncryptionSelfTest exercises the board database through the real store path
// for packaged-binary verification without touching an operator home.
func (s *Store) EncryptionSelfTest(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS coral_encryption_self_test(value TEXT)`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM coral_encryption_self_test`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO coral_encryption_self_test(value) VALUES('board-ok')`); err != nil {
		return err
	}
	var value string
	if err := s.db.GetContext(ctx, &value, `SELECT value FROM coral_encryption_self_test`); err != nil {
		return err
	}
	if value != "board-ok" {
		return fmt.Errorf("board encryption self-test sentinel = %q", value)
	}
	return nil
}

// UseDatabase initializes the task engine on a caller-owned SQLite connection.
// Personal tasks use this engine in their own database, separate from team boards.
// The caller retains responsibility for closing db.
func UseDatabase(ctx context.Context, db *sqlx.DB) (*Store, error) {
	s := &Store{db: db, completionCheckRunner: NewLocalRegisteredCheckRunner(os.Getenv("CORAL_CHECK_WORKDIR")), completionChecksEnabled: completionChecksEnabledFromEnv()}
	if err := s.ensureSchema(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) RecoverTaskReadiness(ctx context.Context) error { return s.recoverTaskReadiness(ctx) }

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// ── Subscribers ──────────────────────────────────────────────────────

// ── Messages ─────────────────────────────────────────────────────────

// ── Groups ───────────────────────────────────────────────────────────

// ── Projects ─────────────────────────────────────────────────────────

// ── Tasks ───────────────────────────────────────────────────────────

// ── Task Dependencies ────────────────────────────────────────────
