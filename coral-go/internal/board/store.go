// Package board provides the message board store and HTTP handlers.
// It uses a separate SQLite database from the main Coral store.
package board

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/cdknorow/coral/internal/testguard"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/cdknorow/coral/internal/clitext"
	"github.com/cdknorow/coral/internal/dbcrypt"
	"github.com/cdknorow/coral/internal/naming"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

// Subscriber represents a board subscriber.
// SubscriberID is the stable identity (role/display name, e.g. "Orchestrator").
// SessionName is the current tmux/pty session name (mutable across restarts).
type Subscriber struct {
	ID           int64   `db:"id" json:"id"`
	Project      string  `db:"project" json:"project"`
	SubscriberID string  `db:"subscriber_id" json:"subscriber_id"`
	SessionName  string  `db:"session_name" json:"session_name,omitempty"`
	JobTitle     string  `db:"job_title" json:"job_title"`
	WebhookURL   *string `db:"webhook_url" json:"-"` // unused: webhooks were removed (#3646); kept for SELECT *, wiped at start
	OriginServer *string `db:"origin_server" json:"origin_server"`
	ReceiveMode  string  `db:"receive_mode" json:"receive_mode"`
	LastReadID   int64   `db:"last_read_id" json:"last_read_id"`
	SubscribedAt string  `db:"subscribed_at" json:"subscribed_at"`
	IsActive     int     `db:"is_active" json:"is_active"`
	CanPeek      int     `db:"can_peek" json:"can_peek"`
	// Legacy column — kept for DB compat, mirrors SubscriberID for new rows.
	SessionID string `db:"session_id" json:"-"`
	// SessionJoinedAt is when SessionName became this subscription's session (#4379): set when the
	// row is made, when another session takes the role over, and when it rejoins after leaving. NULL
	// on rows from before it existed (SubscribedAt stands in for them).
	SessionJoinedAt *string `db:"session_joined_at" json:"-"`
}

// GroupInfo holds group summary info.
type GroupInfo struct {
	GroupID     string `db:"group_id" json:"group_id"`
	MemberCount int    `db:"member_count" json:"member_count"`
}

// Task represents a board task.
type Task struct {
	ID                int64    `db:"id" json:"id"`
	Revision          int      `db:"revision" json:"revision"`
	BoardID           string   `db:"board_id" json:"board_id"`
	BoardLabel        string   `db:"-" json:"board_label,omitempty"` // filled where a list spans boards (#3536)
	Title             string   `db:"title" json:"title"`
	Body              *string  `db:"body" json:"body,omitempty"`
	Status            string   `db:"status" json:"status"`
	Priority          string   `db:"priority" json:"priority"`
	CreatedBy         string   `db:"created_by" json:"created_by"`
	AssignedTo        *string  `db:"assigned_to" json:"assigned_to"`
	CompletedBy       *string  `db:"completed_by" json:"completed_by"`
	CompletionMessage *string  `db:"completion_message" json:"completion_message,omitempty"`
	CreatedAt         string   `db:"created_at" json:"created_at"`
	ClaimedAt         *string  `db:"claimed_at" json:"claimed_at,omitempty"`
	CompletedAt       *string  `db:"completed_at" json:"completed_at,omitempty"`
	SessionID         *string  `db:"session_id" json:"session_id,omitempty"`
	CostUSD           *float64 `db:"cost_usd" json:"cost_usd,omitempty"`
	InputTokens       *int     `db:"input_tokens" json:"input_tokens,omitempty"`
	OutputTokens      *int     `db:"output_tokens" json:"output_tokens,omitempty"`
	CacheReadTokens   *int     `db:"cache_read_tokens" json:"cache_read_tokens,omitempty"`
	CacheWriteTokens  *int     `db:"cache_write_tokens" json:"cache_write_tokens,omitempty"`
	// ParkedReason and ParkedUntil: a parked task (#4175 step 3, park.go). Read where the health
	// rules and the task views need them; nil elsewhere.
	ParkedReason *string `db:"parked_reason" json:"parked_reason,omitempty"`
	ParkedUntil  *string `db:"parked_until" json:"parked_until,omitempty"`
	// RemindAt: a one-time reminder of the task at that time (task add --remind-at, remind.go).
	RemindAt  *string      `db:"remind_at" json:"remind_at,omitempty"`
	BlockedBy []TaskDep    `db:"-" json:"blocked_by,omitempty"`
	Workflow  TaskWorkflow `db:"-" json:"workflow"`
}

type TaskDep struct {
	Satisfied         bool     `json:"satisfied"`
	Outcome           string   `json:"outcome,omitempty"`
	MissingArtifacts  []string `json:"missing_artifacts,omitempty"`
	BlockedReason     string   `json:"blocked_reason,omitempty"`
	TaskID            int64    `json:"task_id"`
	BoardID           string   `json:"board_id"`
	Title             string   `json:"title,omitempty"`
	Status            string   `json:"status,omitempty"`
	Condition         string   `json:"condition,omitempty"`
	RequiredArtifacts []string `json:"required_artifacts,omitempty"`
}

// Message represents a board message.
// SubscriberID is the stable poster identity (role name).
type Message struct {
	ID            int64   `db:"id" json:"id"`
	Project       string  `db:"project" json:"project"`
	SubscriberID  string  `db:"subscriber_id" json:"subscriber_id"`
	Content       string  `db:"content" json:"content"`
	CreatedAt     string  `db:"created_at" json:"created_at"`
	JobTitle      string  `db:"job_title" json:"job_title,omitempty"`
	TargetGroupID *string `db:"target_group_id" json:"target_group_id,omitempty"`
	// External is the origin envelope of a cross-team message (inbound on the
	// receiving board, outbound on the sender's mirror copy); absent otherwise.
	External RawJSON `db:"external_json" json:"external,omitempty"`
	// Origin is set by the server only: "hub_user" for a chat post a Coral Hub user made
	// ("hub_operator" when made as the Operator, #3404)
	// through the tunnel (#3377). The dashboard badges those; never inferred from a name.
	Origin *string `db:"origin" json:"origin,omitempty"`
	// HubUser is the Coral Hub user (hub-user-<8 hex>) behind a post with origin hub_user
	// or hub_operator (the Operator through Coral Hub), from the tunnel-stamped header the
	// server validated (#3404). Set by the server only.
	HubUser *string `db:"hub_user" json:"hub_user,omitempty"`
	// IsSystem is set by the server only, for Coral's own posts (the task queue, the
	// health monitor), never inferred from the sender's name (#3387). The chat
	// collapses these (#3343).
	IsSystem bool `db:"is_system" json:"is_system"`
	// Legacy column — kept for DB compat, mirrors SubscriberID for new rows.
	SessionID string `db:"session_id" json:"-"`
}

// ProjectInfo holds project summary info.
type ProjectInfo struct {
	Project         string `db:"project" json:"project"`
	BoardLabel      string `db:"-" json:"board_label"` // the board's display name (#3536)
	SubscriberCount int    `db:"subscriber_count" json:"subscriber_count"`
	MessageCount    int    `db:"message_count" json:"message_count"`
}

type SubscriberReminder struct {
	Project         string `db:"project"`
	SubscriberID    string `db:"subscriber_id"`
	Message         string `db:"message"`
	IntervalSeconds int    `db:"interval_seconds"`
}

// Store provides message board operations with its own SQLite database.
type Store struct {
	db                      *sqlx.DB
	readDB                  *sqlx.DB
	sessionsDB              *sqlx.DB // optional reference to the main sessions DB for cross-DB queries
	completionCheckRunner   CompletionCheckRunner
	completionChecksEnabled bool
	unreadCounts            unreadCountCache
	noMentionIndex          bool // tests: read as before the mention index (#4465)
	mentionMu               sync.Mutex
	mentionIndexedTo        atomic.Int64 // the mention index's watermark, -1 until read (see mention_index.go)
	waitChanges             waitSignal
	stmts                   *hotStmts // the hot fixed queries, prepared (#4463); nil: unprepared
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

// UnreadState is the startup notification baseline for one active session.
// LatestID identifies the newest message currently eligible for that
// subscriber, allowing the notifier to distinguish a later same-count batch.
type UnreadState struct {
	Count    int
	LatestID int64
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
	if err := s.ensureSchema(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.recoverTaskReadiness(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("recover task readiness: %w", err)
	}

	if isInMemoryPath(dbPath) {
		s.readDB = db
		s.stmts = prepareHotStmts(context.Background(), s.reader())
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

func (s *Store) UpsertSubscriberReminder(ctx context.Context, r SubscriberReminder) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO board_reminders(project,subscriber_id,message,interval_seconds) VALUES(?,?,?,?) ON CONFLICT(project,subscriber_id) DO UPDATE SET message=excluded.message, interval_seconds=excluded.interval_seconds`, r.Project, r.SubscriberID, r.Message, r.IntervalSeconds)
	return err
}

func (s *Store) DeleteSubscriberReminder(ctx context.Context, project, subscriber string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM board_reminders WHERE project=? AND subscriber_id=?`, project, subscriber)
	return err
}

func (s *Store) ListSubscriberReminders(ctx context.Context) ([]SubscriberReminder, error) {
	var out []SubscriberReminder
	err := s.reader().SelectContext(ctx, &out, `SELECT project,subscriber_id,message,interval_seconds FROM board_reminders`)
	return out, err
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

func (s *Store) ensureSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS board_subscribers (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			project       TEXT NOT NULL,
			session_id    TEXT NOT NULL,
			job_title     TEXT NOT NULL,
			webhook_url   TEXT,
			origin_server TEXT,
			receive_mode  TEXT NOT NULL DEFAULT 'mentions',
			last_read_id  INTEGER NOT NULL DEFAULT 0,
			subscribed_at TEXT NOT NULL,
			UNIQUE(project, session_id)
		);
		CREATE TABLE IF NOT EXISTS board_messages (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			project     TEXT NOT NULL,
			session_id  TEXT NOT NULL,
			content     TEXT NOT NULL,
			created_at  TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_board_messages_project ON board_messages(project, id);
		CREATE TABLE IF NOT EXISTS board_reminders (
			project TEXT NOT NULL,
			subscriber_id TEXT NOT NULL,
			message TEXT NOT NULL,
			interval_seconds INTEGER NOT NULL,
			PRIMARY KEY(project, subscriber_id)
		);
		CREATE TABLE IF NOT EXISTS board_groups (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			project    TEXT NOT NULL,
			group_id   TEXT NOT NULL,
			session_id TEXT NOT NULL,
			UNIQUE(project, group_id, session_id)
		);
		CREATE INDEX IF NOT EXISTS idx_board_groups_project_group ON board_groups(project, group_id);
	`)
	if err != nil {
		return err
	}
	// Task tables
	_, err = s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS board_tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			revision INTEGER NOT NULL DEFAULT 1,
			board_id TEXT NOT NULL,
			title TEXT NOT NULL,
			body TEXT,
			status TEXT NOT NULL DEFAULT 'pending'
				CHECK (status IN ('pending', 'in_progress', 'completed', 'skipped', 'blocked', 'draft', 'review_pending')),
			priority TEXT NOT NULL DEFAULT 'medium'
				CHECK (priority IN ('critical', 'high', 'medium', 'low')),
			created_by TEXT NOT NULL,
			assigned_to TEXT,
			completed_by TEXT,
			completion_message TEXT,
			created_at TEXT NOT NULL,
			claimed_at TEXT,
			completed_at TEXT,
			last_activity_at TEXT,
			idle_snoozed_until TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_board_tasks_board_status ON board_tasks(board_id, status);
		CREATE INDEX IF NOT EXISTS idx_board_tasks_assigned ON board_tasks(board_id, assigned_to);
		-- The task list pages done tasks newest first (ListTaskPage, #3269). The WHERE clause
		-- must stay identical to doneTaskStatusSQL for SQLite to use this partial index.
		CREATE INDEX IF NOT EXISTS idx_board_tasks_done ON board_tasks(board_id, completed_at, id)
			WHERE status IN ('completed', 'skipped');
		CREATE TABLE IF NOT EXISTS task_amendments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id INTEGER NOT NULL REFERENCES board_tasks(id),
			revision INTEGER NOT NULL,
			actor TEXT NOT NULL,
			created_at TEXT NOT NULL,
			reason TEXT NOT NULL,
			changes_json TEXT NOT NULL,
			previous_snapshot_json TEXT NOT NULL,
			effective_snapshot_json TEXT NOT NULL,
			UNIQUE(task_id, revision)
		);
	`)
	if err != nil {
		return err
	}

	// Task dependencies table
	_, err = s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS task_dependencies (
			task_id INTEGER NOT NULL,
			blocked_by_task_id INTEGER NOT NULL,
			blocked_by_board_id TEXT NOT NULL,
			PRIMARY KEY (task_id, blocked_by_task_id),
			FOREIGN KEY (task_id) REFERENCES board_tasks(id),
			FOREIGN KEY (blocked_by_task_id) REFERENCES board_tasks(id)
		);
		CREATE INDEX IF NOT EXISTS idx_task_deps_blocked_by ON task_dependencies(blocked_by_task_id);
	`)
	if err != nil {
		return err
	}

	// Migrations for existing DBs — ALTER TABLE ADD COLUMN is idempotent
	// (fails with "duplicate column" on re-run, which is expected and ignored).
	alterColumns := []string{
		"ALTER TABLE board_subscribers ADD COLUMN receive_mode TEXT NOT NULL DEFAULT 'mentions'",
		"ALTER TABLE board_messages ADD COLUMN target_group_id TEXT",
		"ALTER TABLE board_subscribers ADD COLUMN is_active INTEGER NOT NULL DEFAULT 1",
		"ALTER TABLE board_subscribers ADD COLUMN can_peek INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE board_subscribers ADD COLUMN subscriber_id TEXT",
		"ALTER TABLE board_subscribers ADD COLUMN session_name TEXT",
		"ALTER TABLE board_messages ADD COLUMN subscriber_id TEXT",
		"ALTER TABLE board_groups ADD COLUMN subscriber_id TEXT",
		"ALTER TABLE board_tasks ADD COLUMN session_id TEXT",
		"ALTER TABLE board_tasks ADD COLUMN last_activity_at TEXT",
		"ALTER TABLE board_tasks ADD COLUMN idle_snoozed_until TEXT",
		"ALTER TABLE board_tasks ADD COLUMN cost_usd REAL",
		"ALTER TABLE board_tasks ADD COLUMN input_tokens INTEGER",
		"ALTER TABLE board_tasks ADD COLUMN output_tokens INTEGER",
		"ALTER TABLE board_tasks ADD COLUMN cache_read_tokens INTEGER",
		"ALTER TABLE board_tasks ADD COLUMN cache_write_tokens INTEGER",
		"ALTER TABLE board_tasks ADD COLUMN revision INTEGER NOT NULL DEFAULT 1",
		"ALTER TABLE board_messages ADD COLUMN external_json TEXT",
		"ALTER TABLE board_messages ADD COLUMN origin TEXT",
		"ALTER TABLE board_messages ADD COLUMN is_system INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE board_messages ADD COLUMN hub_user TEXT",
		"ALTER TABLE board_tasks ADD COLUMN parked_reason TEXT",
		"ALTER TABLE board_tasks ADD COLUMN parked_until TEXT",
		"ALTER TABLE board_tasks ADD COLUMN remind_at TEXT",
		"ALTER TABLE board_tasks ADD COLUMN reminded_at TEXT",
		"ALTER TABLE board_subscribers ADD COLUMN session_joined_at TEXT",
	}
	for _, ddl := range alterColumns {
		if _, err := s.db.ExecContext(ctx, ddl); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
				log.Printf("[board] migration warning: %v", err)
			}
			continue
		}
		if strings.Contains(ddl, "ADD COLUMN is_system") {
			// The column is new: mark the existing posts of Coral's own senders once (#3387).
			if _, err := s.db.ExecContext(ctx, "UPDATE board_messages SET is_system = 1 WHERE subscriber_id IN ('Coral Task Queue', 'Coral Health Monitor')"); err != nil {
				log.Printf("[board] migration warning: %v", err)
			}
		}
		if strings.Contains(ddl, "ADD COLUMN hub_user") {
			// A Coral Hub user's earlier posts were sent as that user (#3377, #3404).
			if _, err := s.db.ExecContext(ctx, "UPDATE board_messages SET hub_user = subscriber_id WHERE origin = 'hub_user'"); err != nil {
				log.Printf("[board] migration warning: %v", err)
			}
		}
	}

	// Backfill: save old session_id as session_name, then set subscriber_id from job_title.
	backfills := []struct {
		desc string
		sql  string
	}{
		{"backfill session_name", "UPDATE board_subscribers SET session_name = session_id WHERE session_name IS NULL"},
		{"backfill subscriber_id", "UPDATE board_subscribers SET subscriber_id = job_title WHERE subscriber_id IS NULL"},
		{"deduplicate subscribers", `DELETE FROM board_subscribers WHERE id NOT IN (
			SELECT MAX(id) FROM board_subscribers GROUP BY project, subscriber_id
		) AND subscriber_id IS NOT NULL`},
		{"mirror subscriber_id", "UPDATE board_subscribers SET session_id = subscriber_id WHERE subscriber_id IS NOT NULL AND session_id != subscriber_id"},
		{"backfill message subscriber_id", `UPDATE board_messages SET subscriber_id = COALESCE(
			(SELECT bs.subscriber_id FROM board_subscribers bs
			 WHERE bs.session_name = board_messages.session_id AND bs.project = board_messages.project
			 LIMIT 1),
			board_messages.session_id
		) WHERE subscriber_id IS NULL`},
		{"backfill group subscriber_id", `UPDATE board_groups SET subscriber_id = COALESCE(
			(SELECT bs.subscriber_id FROM board_subscribers bs
			 WHERE bs.session_name = board_groups.session_id AND bs.project = board_groups.project
			 LIMIT 1),
			board_groups.session_id
		) WHERE subscriber_id IS NULL`},
	}
	for _, bf := range backfills {
		if _, err := s.db.ExecContext(ctx, bf.sql); err != nil {
			log.Printf("[board] migration %s failed: %v", bf.desc, err)
		}
	}

	// Lookups by session name (notifier, waits) and by subscriber id across
	// boards. Both columns are migrated, so the indexes follow the ALTERs.
	for _, ddl := range []string{
		"CREATE INDEX IF NOT EXISTS idx_board_subscribers_session_name ON board_subscribers(session_name)",
		"CREATE INDEX IF NOT EXISTS idx_board_subscribers_subscriber ON board_subscribers(subscriber_id, project)",
	} {
		if _, err := s.db.ExecContext(ctx, ddl); err != nil {
			log.Printf("[board] index warning: %v", err)
		}
	}

	// Migrate CHECK constraint to include 'blocked' status for existing databases.
	// SQLite doesn't support ALTER CONSTRAINT, so we recreate the table.
	if err := s.migrateTasksCheckConstraint(ctx); err != nil {
		return err
	}

	if err := s.ensureWaitsSchema(ctx); err != nil {
		return err
	}
	if err := s.ensureBoardSettingsSchema(ctx); err != nil {
		return err
	}
	if err := s.ensureHubOperatorActionsSchema(ctx); err != nil {
		return err
	}
	if err := s.ensureHealthSchema(ctx); err != nil {
		return err
	}

	if err := s.initTaskWorkflows(ctx); err != nil {
		return err
	}
	if err := s.ensureBoardsSchema(ctx); err != nil {
		return err
	}
	// The mention index and its backfill (#4465). A failure here only means ReadMessages reads as it did.
	if err := s.ensureMentionIndexSchema(ctx); err != nil {
		log.Printf("[board] mention index: %v", err)
	}
	return nil
}

// Rebuild from the existing schema so all columns, indexes, and personal-task
// projection triggers survive a status constraint upgrade. DDL is atomic.
func (s *Store) migrateTasksCheckConstraint(ctx context.Context) error {
	var schema string
	if err := s.db.GetContext(ctx, &schema, "SELECT sql FROM sqlite_master WHERE type='table' AND name='board_tasks'"); err != nil {
		return err
	}
	if strings.Contains(schema, "'review_pending'") {
		return nil
	}
	var objects []string
	if err := s.db.SelectContext(ctx, &objects, "SELECT sql FROM sqlite_master WHERE tbl_name='board_tasks' AND type IN ('index','trigger') AND sql IS NOT NULL"); err != nil {
		return err
	}
	// Foreign keys must be disabled outside the transaction during a table rebuild.
	// The store uses one connection; startup runs before it is exposed to callers.
	var foreignKeys int
	if err := s.db.GetContext(ctx, &foreignKeys, "PRAGMA foreign_keys"); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	defer s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA foreign_keys=%d", foreignKeys))
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sequence int64
	if err := tx.GetContext(ctx, &sequence, "SELECT COALESCE(MAX(seq),0) FROM sqlite_sequence WHERE name='board_tasks'"); err != nil {
		return err
	}
	schema = strings.Replace(schema, "board_tasks", "board_tasks_new", 1)
	schema = strings.Replace(schema, "'skipped'", "'skipped', 'review_pending'", 1)
	if !strings.Contains(schema, "'blocked'") {
		schema = strings.Replace(schema, "'skipped'", "'skipped', 'blocked'", 1)
	}
	if !strings.Contains(schema, "'draft'") {
		schema = strings.Replace(schema, "'skipped'", "'skipped', 'draft'", 1)
	}
	for _, statement := range append([]string{schema, "INSERT INTO board_tasks_new SELECT * FROM board_tasks", "DROP TABLE board_tasks", "ALTER TABLE board_tasks_new RENAME TO board_tasks"}, objects...) {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate task review status: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE sqlite_sequence SET seq=MAX(seq,?) WHERE name='board_tasks'", sequence); err != nil {
		return err
	}
	return tx.Commit()
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// ── Subscribers ──────────────────────────────────────────────────────

// Subscribe adds or updates a subscriber on a board project.
// subscriberID is the stable identity (role name). sessionName is the current tmux/pty session.
func (s *Store) Subscribe(ctx context.Context, project, subscriberID, jobTitle, sessionName string, originServer *string, receiveMode string, canPeek ...bool) (*Subscriber, error) {
	peekFlag := 0
	if len(canPeek) > 0 && canPeek[0] {
		peekFlag = 1
	}
	if receiveMode == "" {
		if isOrchestrator(subscriberID, jobTitle, peekFlag) {
			receiveMode = "all"
		} else {
			receiveMode = "mentions"
		}
	}
	now := nowUTC()

	// For new subscribers who haven't been on this board before, start their
	// cursor at the latest message so they don't get flooded with history.
	var carryForwardCursor int64
	_ = s.db.GetContext(ctx, &carryForwardCursor,
		"SELECT COALESCE(MAX(last_read_id), 0) FROM board_subscribers WHERE project = ? AND subscriber_id = ?",
		project, subscriberID)
	if carryForwardCursor == 0 {
		_ = s.db.GetContext(ctx, &carryForwardCursor,
			"SELECT COALESCE(MAX(last_read_id), 0) FROM board_subscribers WHERE project = ?",
			project)
	}

	// session_id mirrors subscriber_id so UNIQUE(project, session_id) enforces subscriber uniqueness.
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO board_subscribers (project, session_id, subscriber_id, session_name, job_title, origin_server, receive_mode, last_read_id, subscribed_at, is_active, can_peek, session_joined_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
		 ON CONFLICT(project, session_id) DO UPDATE SET
		     job_title = excluded.job_title,
		     webhook_url = NULL,
		     origin_server = excluded.origin_server,
		     receive_mode = excluded.receive_mode,
		     session_joined_at = CASE
		         WHEN board_subscribers.session_name IS excluded.session_name AND board_subscribers.is_active = 1
		         THEN COALESCE(board_subscribers.session_joined_at, board_subscribers.subscribed_at)
		         ELSE excluded.session_joined_at END,
		     session_name = excluded.session_name,
		     subscriber_id = excluded.subscriber_id,
		     is_active = 1,
		     can_peek = excluded.can_peek`,
		project, subscriberID, subscriberID, sessionName, jobTitle, originServer, receiveMode, carryForwardCursor, now, peekFlag, now)
	if err == nil {
		s.ensureBoardRow(ctx, project)
	}
	if err != nil {
		return nil, err
	}
	var sub Subscriber
	err = s.db.GetContext(ctx, &sub,
		"SELECT * FROM board_subscribers WHERE project = ? AND subscriber_id = ?",
		project, subscriberID)
	return &sub, err
}

// Unsubscribe marks a subscriber as inactive. Returns true if a row was updated.
func (s *Store) Unsubscribe(ctx context.Context, project, subscriberID string) (bool, error) {
	result, err := s.db.ExecContext(ctx,
		"UPDATE board_subscribers SET is_active = 0 WHERE project = ? AND subscriber_id = ? AND is_active = 1",
		project, subscriberID)
	if err != nil {
		return false, err
	}
	s.unreadCounts.forget(project, subscriberID)
	n, _ := result.RowsAffected()
	return n > 0, nil
}

// AdvanceReadCursor sets a subscriber's last_read_id to the current max
// message ID on the board, so they see no stale unreads after a reset.
func (s *Store) AdvanceReadCursor(ctx context.Context, project, subscriberID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE board_subscribers
		 SET last_read_id = COALESCE((SELECT MAX(id) FROM board_messages WHERE project = ?), 0)
		 WHERE project = ? AND subscriber_id = ?`,
		project, project, subscriberID)
	return err
}

// ListSubscribers returns all active subscribers for a project.
func (s *Store) ListSubscribers(ctx context.Context, project string) ([]Subscriber, error) {
	var subs []Subscriber
	err := s.reader().SelectContext(ctx, &subs,
		"SELECT * FROM board_subscribers WHERE project = ? AND is_active = 1 ORDER BY subscribed_at", project)
	return subs, err
}

// GetSubscription returns the active subscription for a subscriber.
func (s *Store) GetSubscription(ctx context.Context, subscriberID string) (*Subscriber, error) {
	var sub Subscriber
	err := s.reader().GetContext(ctx, &sub,
		"SELECT * FROM board_subscribers WHERE subscriber_id = ? AND is_active = 1 ORDER BY subscribed_at DESC LIMIT 1", subscriberID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &sub, err
}

// GetProjectSubscription returns a subscriber's active subscription on one
// board. The same subscriber_id (e.g. "Frontend Dev") can be subscribed on
// several boards with different sessions, so task nudges must use this rather
// than GetSubscription.
func (s *Store) GetProjectSubscription(ctx context.Context, project, subscriberID string) (*Subscriber, error) {
	var sub Subscriber
	err := s.reader().GetContext(ctx, &sub,
		"SELECT * FROM board_subscribers WHERE project = ? AND subscriber_id = ? AND is_active = 1 ORDER BY subscribed_at DESC LIMIT 1", project, subscriberID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &sub, err
}

// GetTask returns one task on a board.
func (s *Store) GetTask(ctx context.Context, project string, taskID int64) (*Task, error) {
	return s.getTaskByID(ctx, project, taskID)
}

// GetSubscriptionBySessionName returns the active subscription for a specific tmux session.
// This is more precise than GetSubscription when the same subscriber_id has
// multiple active subscriptions across different boards.
func (s *Store) GetSubscriptionBySessionName(ctx context.Context, sessionName string) (*Subscriber, error) {
	var sub Subscriber
	err := getHot(ctx, s.hot().subscriptionBySessionName, s.reader(), &sub, sqlSubscriptionBySessionName, sessionName)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &sub, err
}

// SessionSubscription is the agent session sessionName's active subscription on project, or
// nil when it has none there.
func (s *Store) SessionSubscription(ctx context.Context, project, sessionName string) (*Subscriber, error) {
	var sub Subscriber
	err := s.reader().GetContext(ctx, &sub,
		"SELECT * FROM board_subscribers WHERE project = ? AND session_name = ? AND is_active = 1 ORDER BY subscribed_at DESC LIMIT 1", project, sessionName)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &sub, err
}

// SessionSubscribedOn reports whether the agent session sessionName has an active
// subscription on project: what a notice about that board must find before it is typed into
// the session (#3864).
func (s *Store) SessionSubscribedOn(ctx context.Context, project, sessionName string) (bool, error) {
	var n int
	err := s.reader().GetContext(ctx, &n,
		"SELECT COUNT(*) FROM board_subscribers WHERE project = ? AND session_name = ? AND is_active = 1", project, sessionName)
	return n > 0, err
}

// GetAllSubscriptions returns all active subscriptions keyed by session_name
// (the tmux session identifier) for compatibility with live session lookups.
func (s *Store) GetAllSubscriptions(ctx context.Context) (map[string]*Subscriber, error) {
	var subs []Subscriber
	err := s.reader().SelectContext(ctx, &subs, "SELECT * FROM board_subscribers WHERE is_active = 1")
	if err != nil {
		return nil, err
	}
	result := make(map[string]*Subscriber, len(subs))
	for i := range subs {
		result[subs[i].SessionName] = &subs[i]
	}
	return result, nil
}

// UpdateSessionName updates the mutable tmux/pty session name for a subscriber.
// Used when an agent restarts and gets a new session but keeps its identity.
func (s *Store) UpdateSessionName(ctx context.Context, project, subscriberID, sessionName string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE board_subscribers SET session_name = ? WHERE project = ? AND subscriber_id = ?",
		sessionName, project, subscriberID)
	return err
}

// ── Messages ─────────────────────────────────────────────────────────

// PostMessage posts a new message to a project board.
func (s *Store) PostMessage(ctx context.Context, project, subscriberID, content string, targetGroupID *string) (*Message, error) {
	return s.postMessage(ctx, project, subscriberID, content, targetGroupID, "", "", false)
}

// OriginHubUser marks a chat post a Coral Hub user made through the tunnel (#3377).
const OriginHubUser = "hub_user"

// OriginHubOperator marks a chat post a Coral Hub user made as the Operator through the
// tunnel (#3404): sent as the Operator, with HubUser naming who.
const OriginHubOperator = "hub_operator"

// OriginHubUserUnknown marks a Coral Hub chat post with no Hub user header: a hub still on
// an older pkg/hubproxy (#3377). It's sent as HubUnknownSender.
const OriginHubUserUnknown = "hub_user_unknown"

// OriginHubNotice marks a fixed-template notice from Coral Hub about a team link (#3489):
// sent as HubUnknownSender ("Coral Hub"), never an agent or the Operator.
const OriginHubNotice = "hub_notice"

// PostHubNotice posts a notice Coral Hub asked for, as "Coral Hub" with origin hub_notice,
// marked as one of Coral's own messages. content is built by Coral from a fixed template.
func (s *Store) PostHubNotice(ctx context.Context, project, content string) (*Message, error) {
	return s.postMessage(ctx, project, HubUnknownSender, content, nil, OriginHubNotice, "", true)
}

// PostMessageWithOrigin is PostMessage with a server-set origin ("" for none).
func (s *Store) PostMessageWithOrigin(ctx context.Context, project, subscriberID, content string, targetGroupID *string, origin string) (*Message, error) {
	return s.postMessage(ctx, project, subscriberID, content, targetGroupID, origin, "", false)
}

// PostHubMessage posts a chat message a Coral Hub user made through the tunnel, with the
// validated Hub user recorded (#3404): sent as that user with origin hub_user, or, when
// asOperator, as the Operator with origin hub_operator.
func (s *Store) PostHubMessage(ctx context.Context, project, hubUser, content string, targetGroupID *string, asOperator bool) (*Message, error) {
	sender, origin := hubUser, OriginHubUser
	if asOperator {
		sender, origin = "Operator", OriginHubOperator
	}
	return s.postMessage(ctx, project, sender, content, targetGroupID, origin, hubUser, false)
}

// PostSystemMessage posts as one of Coral's own senders (the task queue, the health
// monitor) and marks the message is_system (#3387). Only server code calls it.
func (s *Store) PostSystemMessage(ctx context.Context, project, sender, content string, targetGroupID *string) (*Message, error) {
	return s.postMessage(ctx, project, sender, content, targetGroupID, "", "", true)
}

func (s *Store) postMessage(ctx context.Context, project, subscriberID, content string, targetGroupID *string, origin, hubUser string, system bool) (*Message, error) {
	// No terminal control is stored (#3580): a post is printed in agents' terminals by
	// coral-board, and system posts quote task titles agents wrote.
	content = clitext.Clean(content)
	now := nowUTC()
	var originVal, hubUserVal *string
	if origin != "" {
		originVal = &origin
	}
	if hubUser != "" {
		hubUserVal = &hubUser
	}
	result, err := s.db.ExecContext(ctx,
		"INSERT INTO board_messages (project, session_id, subscriber_id, content, target_group_id, created_at, origin, hub_user, is_system) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		project, subscriberID, subscriberID, content, targetGroupID, now, originVal, hubUserVal, system)
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()
	s.ensureBoardRow(ctx, project)
	msg := &Message{ID: id, Project: project, SubscriberID: subscriberID, Content: content, CreatedAt: now, Origin: originVal, HubUser: hubUserVal, IsSystem: system}
	if targetGroupID != nil {
		msg.TargetGroupID = targetGroupID
	}
	return msg, nil
}

// ReadMessages returns unread messages for a subscriber (cursor-based).
// By default, for non-orchestrator subscribers, only messages where they are
// explicitly tagged (or @all / @notify-all) are returned.
// For orchestrators or when readAll is true, all unread messages from others are returned.
func (s *Store) ReadMessages(ctx context.Context, project, subscriberID string, limit int, readAll ...bool) ([]Message, error) {
	// Get subscriber cursor and role info
	var sub struct {
		LastReadID  int64  `db:"last_read_id"`
		JobTitle    string `db:"job_title"`
		ReceiveMode string `db:"receive_mode"`
		CanPeek     int    `db:"can_peek"`
	}
	err := s.reader().GetContext(ctx, &sub,
		"SELECT last_read_id, job_title, receive_mode, can_peek FROM board_subscribers WHERE project = ? AND subscriber_id = ?",
		project, subscriberID)
	if err != nil {
		return nil, nil // Not subscribed
	}

	wantAll := len(readAll) > 0 && readAll[0]
	isOrch := isOrchestrator(subscriberID, sub.JobTitle, sub.CanPeek)
	filterTagged := !wantAll && !isOrch

	if !wantAll && sub.ReceiveMode == "none" {
		return nil, nil
	}

	var messages []Message
	if filterTagged {
		terms := mentionTerms(subscriberID, sub.JobTitle)
		patterns := make([]string, len(terms))
		for i, t := range terms {
			patterns[i] = "%" + t + "%"
		}

		fetchLimit := limit * 2
		if fetchLimit < 50 {
			fetchLimit = 50
		}
		candidates, indexed, err := s.indexedMentionCandidates(ctx, project, subscriberID, sub.LastReadID, terms, fetchLimit)
		if err != nil {
			return nil, err
		}
		if !indexed {
			if candidates, err = s.likeMentionCandidates(ctx, project, subscriberID, sub.LastReadID, patterns, fetchLimit); err != nil {
				return nil, err
			}
		}

		numbered := s.numberedNames(ctx, s.reader(), project)
		for _, msg := range candidates {
			if containsExplicitTag(msg.Content, subscriberID, sub.JobTitle, numbered) {
				messages = append(messages, msg)
				if len(messages) >= limit {
					break
				}
			}
		}
	} else {
		err = s.reader().SelectContext(ctx, &messages,
			`SELECT m.id, m.project, m.subscriber_id, m.session_id, m.content, m.target_group_id, m.external_json, m.origin, m.hub_user, m.is_system, m.created_at,
			        COALESCE(s.job_title, m.subscriber_id, 'Unknown') as job_title
			 FROM board_messages m
			 LEFT JOIN board_subscribers s ON m.project = s.project AND m.subscriber_id = s.subscriber_id
			 WHERE m.project = ? AND m.id > ? AND m.subscriber_id != ?
			 ORDER BY m.id ASC LIMIT ?`,
			project, sub.LastReadID, subscriberID, limit)
		if err != nil {
			return nil, err
		}
	}
	// Advance cursor past returned messages and own messages
	newCursor := sub.LastReadID
	if len(messages) > 0 {
		for _, m := range messages {
			if m.ID > newCursor {
				newCursor = m.ID
			}
		}
	}

	// Skip past own messages
	var ownMax int64
	getHot(ctx, s.hot().ownMaxMessageID, s.reader(), &ownMax, sqlOwnMaxMessageID, project, subscriberID)
	if ownMax > newCursor {
		newCursor = ownMax
	}

	if newCursor > sub.LastReadID {
		s.db.ExecContext(ctx,
			"UPDATE board_subscribers SET last_read_id = ? WHERE project = ? AND subscriber_id = ?",
			newCursor, project, subscriberID)
	}

	return messages, nil
}

// MarkReadThrough moves a subscriber's read position forward to throughID
// (never back), for readers that looked at the newest messages rather than
// reading in order (coral-board read --last N). It reports the messages from
// others that were unread and older than fromID, the oldest one shown, which
// this skips without their having been displayed.
func (s *Store) MarkReadThrough(ctx context.Context, project, subscriberID string, throughID, fromID int64) (skipped int, firstSkipped, lastSkipped int64, err error) {
	var lastReadID int64
	if err = s.reader().GetContext(ctx, &lastReadID,
		"SELECT last_read_id FROM board_subscribers WHERE project = ? AND subscriber_id = ?",
		project, subscriberID); err != nil {
		if err == sql.ErrNoRows {
			err = fmt.Errorf("%s is not subscribed to %s", subscriberID, project)
		}
		return 0, 0, 0, err
	}
	if throughID <= lastReadID {
		return 0, 0, 0, nil
	}
	var r struct {
		N     int   `db:"n"`
		First int64 `db:"first"`
		Last  int64 `db:"last"`
	}
	if err = s.reader().GetContext(ctx, &r,
		`SELECT COUNT(*) AS n, COALESCE(MIN(id), 0) AS first, COALESCE(MAX(id), 0) AS last FROM board_messages
		 WHERE project = ? AND id > ? AND id < ? AND subscriber_id != ? AND subscriber_id NOT IN ('Coral Task Queue')`,
		project, lastReadID, fromID, subscriberID); err != nil {
		return 0, 0, 0, err
	}
	_, err = s.db.ExecContext(ctx,
		"UPDATE board_subscribers SET last_read_id = ? WHERE project = ? AND subscriber_id = ? AND last_read_id < ?",
		throughID, project, subscriberID, throughID)
	return r.N, r.First, r.Last, err
}

// ListMessages returns recent messages (no cursor, no side effects).
// If beforeID > 0, only messages with id < beforeID are returned (keyset pagination).
func (s *Store) ListMessages(ctx context.Context, project string, limit, offset int, beforeID int64) ([]Message, error) {
	var messages []Message
	var err error
	if beforeID > 0 {
		err = s.reader().SelectContext(ctx, &messages,
			`SELECT m.id, m.project, m.subscriber_id, m.session_id, m.content, m.created_at,
			        COALESCE(s.job_title, m.subscriber_id, 'Unknown') as job_title,
			        m.target_group_id, m.external_json, m.origin, m.hub_user, m.is_system
			 FROM board_messages m
			 LEFT JOIN board_subscribers s ON m.project = s.project AND m.subscriber_id = s.subscriber_id
			 WHERE m.project = ? AND m.id < ?
			 ORDER BY m.id ASC LIMIT ? OFFSET ?`,
			project, beforeID, limit, offset)
	} else if offset > 0 {
		// Paginated load: use ASC order with offset for consistent pagination
		err = s.reader().SelectContext(ctx, &messages,
			`SELECT m.id, m.project, m.subscriber_id, m.session_id, m.content, m.created_at,
			        COALESCE(s.job_title, m.subscriber_id, 'Unknown') as job_title,
			        m.target_group_id, m.external_json, m.origin, m.hub_user, m.is_system
			 FROM board_messages m
			 LEFT JOIN board_subscribers s ON m.project = s.project AND m.subscriber_id = s.subscriber_id
			 WHERE m.project = ?
			 ORDER BY m.id ASC LIMIT ? OFFSET ?`,
			project, limit, offset)
	} else {
		// Initial load (offset=0): get most recent messages
		err = s.reader().SelectContext(ctx, &messages,
			`SELECT * FROM (
			    SELECT m.id, m.project, m.subscriber_id, m.session_id, m.content, m.created_at,
			           COALESCE(s.job_title, m.subscriber_id, 'Unknown') as job_title,
			           m.target_group_id, m.external_json, m.origin, m.hub_user, m.is_system
			    FROM board_messages m
			    LEFT JOIN board_subscribers s ON m.project = s.project AND m.subscriber_id = s.subscriber_id
			    WHERE m.project = ?
			    ORDER BY m.id DESC LIMIT ?
			 ) sub ORDER BY id ASC`,
			project, limit)
	}
	return messages, err
}

// listMessagesSelect is the dashboard message row; callers append the WHERE.
const listMessagesSelect = `SELECT m.id, m.project, m.subscriber_id, m.session_id, m.content, m.created_at,
	        COALESCE(s.job_title, m.subscriber_id, 'Unknown') as job_title,
	        m.target_group_id, m.external_json, m.origin, m.hub_user, m.is_system
	 FROM board_messages m
	 LEFT JOIN board_subscribers s ON m.project = s.project AND m.subscriber_id = s.subscriber_id`

// Keyset paging queries; the EXPLAIN test checks these exact strings.
const (
	listMessagesBeforeSQL = `SELECT * FROM (` + listMessagesSelect + `
	    WHERE m.project = ? AND m.id < ?
	    ORDER BY m.id DESC LIMIT ?
	 ) sub ORDER BY id ASC`
	listMessagesAfterSQL = listMessagesSelect + `
	 WHERE m.project = ? AND m.id > ?
	 ORDER BY m.id ASC LIMIT ?`
	countMessagesBeforeSQL = "SELECT COUNT(*) FROM board_messages WHERE project = ? AND id < ?"
	// A board's total, counted from where its cached total stopped (messageTotal).
	messageTotalHighSQL  = "SELECT COALESCE(MAX(id), ?) FROM board_messages WHERE project = ? AND id > ?"
	messageTotalCountSQL = "SELECT COUNT(*) FROM board_messages WHERE project = ? AND id > ? AND id <= ?"
	// The messages from an id up to the counted high, at most a limit of them.
	countMessagesTailSQL = "SELECT COUNT(*) FROM (SELECT 1 FROM board_messages WHERE project = ? AND id >= ? AND id <= ? LIMIT ?)"
)

// beforeCountTail: CountMessagesBefore subtracts the messages from beforeID on from the
// board's total when there are at most this many (a dashboard poll or the latest page: none
// to a page's worth), and counts the older ones directly when paging deep into the history.
const beforeCountTail = 5000

// ListMessagesBefore returns the newest limit messages with id < beforeID,
// oldest first: the page just before a loaded one. Unlike OFFSET paging it
// seeks the (project, id) index, so its cost does not grow with board size.
func (s *Store) ListMessagesBefore(ctx context.Context, project string, beforeID int64, limit int) ([]Message, error) {
	var messages []Message
	err := s.reader().SelectContext(ctx, &messages, listMessagesBeforeSQL, project, beforeID, limit)
	return messages, err
}

// ListMessagesAfter returns the oldest limit messages with id > afterID,
// oldest first: what arrived after the newest loaded message.
func (s *Store) ListMessagesAfter(ctx context.Context, project string, afterID int64, limit int) ([]Message, error) {
	var messages []Message
	err := s.reader().SelectContext(ctx, &messages, listMessagesAfterSQL, project, afterID, limit)
	return messages, err
}

// CountMessagesBefore counts the project's messages with id < beforeID.
func (s *Store) CountMessagesBefore(ctx context.Context, project string, beforeID int64) (int, error) {
	total, hi, err := s.messageTotal(ctx, project)
	if err != nil {
		return 0, err
	}
	if beforeID > hi {
		return total, nil
	}
	var tail int
	if err := s.reader().GetContext(ctx, &tail, countMessagesTailSQL, project, beforeID, hi, beforeCountTail+1); err != nil {
		return 0, err
	}
	if tail <= beforeCountTail {
		return max(total-tail, 0), nil
	}
	var count int
	err = s.reader().GetContext(ctx, &count, countMessagesBeforeSQL, project, beforeID)
	return count, err
}

// GetMessageByID returns one message of a board by its id. Message ids are numbered across
// every board, so an id from another board is not found here (sql.ErrNoRows), as if it did
// not exist (#3864: an agent read other teams' messages by walking ids under its own board).
func (s *Store) GetMessageByID(ctx context.Context, project string, id int64) (*Message, error) {
	var msg Message
	err := s.reader().GetContext(ctx, &msg,
		`SELECT m.id, m.project, m.subscriber_id, m.session_id, m.content, m.created_at,
		        COALESCE(s.job_title, m.subscriber_id, 'Unknown') as job_title,
		        m.target_group_id, m.external_json, m.origin, m.hub_user, m.is_system
		 FROM board_messages m
		 LEFT JOIN board_subscribers s ON m.project = s.project AND m.subscriber_id = s.subscriber_id
		 WHERE m.id = ? AND m.project = ?`, id, project)
	if err != nil {
		return nil, err
	}
	return &msg, nil
}

// SearchMessageHits returns bounded board messages containing query text. It
// exposes the immutable message ID used by chat search navigation.
func (s *Store) SearchMessageHits(ctx context.Context, query string, limit int) ([]Message, error) {
	if limit < 1 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	var messages []Message
	err := s.reader().SelectContext(ctx, &messages,
		`SELECT m.id, m.project, m.subscriber_id, m.session_id, m.content, m.created_at,
		        COALESCE(bs.job_title, m.subscriber_id, 'Unknown') as job_title,
		        m.target_group_id, m.external_json, m.origin, m.hub_user, m.is_system
		 FROM board_messages m
		 LEFT JOIN board_subscribers bs ON m.project = bs.project AND m.subscriber_id = bs.subscriber_id
		 WHERE lower(m.content) LIKE lower(?)
		 ORDER BY m.id DESC LIMIT ?`, "%"+query+"%", limit)
	return messages, err
}

// CountMessages returns the total message count for a project.
func (s *Store) CountMessages(ctx context.Context, project string) (int, error) {
	total, _, err := s.messageTotal(ctx, project)
	return total, err
}

// CheckUnread returns the count of unread messages based on the subscriber's receive_mode.
//
// Modes:
//   - "none"     → always 0
//   - "all"      → all unread messages from others
//   - "mentions" → only messages with @notify-all, @<subscriber_id>, or @<job_title>
//   - anything else → treat as group-id, count only messages from group members
//
// mentionTerms returns the canonical list of mention patterns for a subscriber.
// Used by CheckUnread (SQL LIKE), GetAllUnreadCounts (Go string matching), and ReadMessages.
func mentionTerms(subscriberID, jobTitle string) []string {
	terms := []string{"@notify-all", "@notify_all", "@notifyall", "@all"}
	seen := make(map[string]bool)
	for _, t := range terms {
		seen[strings.ToLower(t)] = true
	}

	addTerm := func(t string) {
		t = strings.TrimSpace(t)
		if t == "" {
			return
		}
		lower := strings.ToLower(t)
		if !seen[lower] {
			seen[lower] = true
			terms = append(terms, t)
		}
	}

	for _, name := range []string{subscriberID, jobTitle} {
		if name == "" {
			continue
		}
		addTerm("@" + name)
		clean := strings.ReplaceAll(name, " ", "")
		if clean != "" {
			addTerm("@" + clean)
		}
		addTerm(name + ":")
		addTerm(name + " —")
		addTerm(name + "—")
	}

	return terms
}

// numberedSuffix returns " <digits>" when s continues at i with that and then a word
// boundary (the number of a numbered agent's name, "Backend Dev 2"), else "".
func numberedSuffix(s string, i int) string {
	if i >= len(s) || s[i] != ' ' {
		return ""
	}
	j := i + 1
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j > i+1 && (j == len(s) || !isAlphaNum(s[j])) {
		return s[i:j]
	}
	return ""
}

// numberedNames is the set of a board's active subscriber ids that end in " <digits>"
// (numbered agents, #3543), lowercased, for containsExplicitTag.
func (s *Store) numberedNames(ctx context.Context, q sqlx.QueryerContext, project string) map[string]bool {
	var ids []string
	if err := sqlx.SelectContext(ctx, q, &ids, `SELECT subscriber_id FROM board_subscribers WHERE project = ? AND is_active = 1`, project); err != nil {
		return nil
	}
	var out map[string]bool
	for _, id := range ids {
		lower := strings.ToLower(id)
		if i := strings.LastIndexByte(lower, ' '); i > 0 && numberedSuffix(lower, i) != "" && i+len(numberedSuffix(lower, i)) == len(lower) {
			if out == nil {
				out = map[string]bool{}
			}
			out[lower] = true
		}
	}
	return out
}

func isAlphaNum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

// containsExplicitTag returns true if content explicitly mentions or tags subscriberID/jobTitle,
// or includes broadcast tags like @all, @notify-all.
//
// numbered (optional) is the board's numbered agent names, lowercased (numberedNames):
// "@Backend Dev 2" skips "Backend Dev" only when "backend dev 2" is one of them, so
// "@Orchestrator 3 tasks are ready" still tags the Orchestrator on a board without an
// "Orchestrator 3" (Security's #3543 review).
func containsExplicitTag(content, subscriberID, jobTitle string, numbered ...map[string]bool) bool {
	var names map[string]bool
	if len(numbered) > 0 {
		names = numbered[0]
	}
	contentLower := strings.ToLower(content)
	terms := mentionTerms(subscriberID, jobTitle)
	for _, term := range terms {
		tLower := strings.ToLower(term)
		idx := 0
		for {
			pos := strings.Index(contentLower[idx:], tLower)
			if pos == -1 {
				break
			}
			matchPos := idx + pos
			endPos := matchPos + len(tLower)
			idx = endPos

			// Check word boundary before match: should not be preceded by alphanumeric (e.g. user@domain.com or mydev:)
			if matchPos > 0 && isAlphaNum(contentLower[matchPos-1]) {
				continue
			}

			// Check word boundary at the end if the term starts with '@' (e.g. @Dev in @Developer)
			if strings.HasPrefix(term, "@") {
				if endPos < len(contentLower) && isAlphaNum(contentLower[endPos]) {
					continue
				}
				// "@Backend Dev 2" names the numbered agent "Backend Dev 2", not "Backend Dev"
				// (#3543), when the board has that agent.
				if num := numberedSuffix(contentLower, endPos); num != "" && names[tLower[1:]+num] {
					continue
				}
			}
			return true
		}
	}
	return false
}

// isOrchestrator returns true if the subscriber identity or job title indicates an orchestrator,
// or if canPeek is enabled.
// IsOrchestrator reports whether an identity gets the orchestrator's rights (receive
// every message, plan tasks, peek): can_peek, or "orchestrator" in its id or job title.
func IsOrchestrator(subscriberID, jobTitle string, canPeek int) bool {
	return isOrchestrator(subscriberID, jobTitle, canPeek)
}

func isOrchestrator(subscriberID, jobTitle string, canPeek int) bool {
	if canPeek != 0 {
		return true
	}
	if strings.Contains(strings.ToLower(subscriberID), "orchestrator") {
		return true
	}
	if strings.Contains(strings.ToLower(jobTitle), "orchestrator") {
		return true
	}
	return false
}

func (s *Store) CheckUnread(ctx context.Context, project, subscriberID string) (int, error) {
	var sub struct {
		LastReadID  int64  `db:"last_read_id"`
		JobTitle    string `db:"job_title"`
		ReceiveMode string `db:"receive_mode"`
		CanPeek     int    `db:"can_peek"`
	}
	err := s.reader().GetContext(ctx, &sub,
		"SELECT last_read_id, job_title, receive_mode, can_peek FROM board_subscribers WHERE project = ? AND subscriber_id = ?",
		project, subscriberID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.unreadCounts.forget(project, subscriberID)
		}
		return 0, nil
	}

	receiveMode := sub.ReceiveMode
	if receiveMode == "" {
		receiveMode = "mentions"
	}

	if receiveMode == "none" {
		return 0, nil
	}

	// System senders post audit messages that should not trigger notifications
	systemFilter := " AND subscriber_id NOT IN ('Coral Task Queue')"

	if receiveMode == "all" {
		// Coral's own notice to one agent (CoralSender, #3636) counts only for the agent it
		// names: never a fan-out to every subscriber that reads everything.
		var count int
		err := s.reader().GetContext(ctx, &count,
			`SELECT COUNT(*) FROM board_messages WHERE project = ? AND id > ? AND subscriber_id != ?`+systemFilter+
				` AND (subscriber_id != '`+CoralSender+`' OR instr(content, ?) > 0)`,
			project, sub.LastReadID, subscriberID, "@"+sub.JobTitle)
		return count, err
	}

	if receiveMode == "mentions" {
		terms := mentionTerms(subscriberID, sub.JobTitle)
		// Convert terms to SQL LIKE patterns (% wildcard matches any substring)
		whereClauses := make([]string, len(terms))
		args := []any{subscriberID}
		for i, t := range terms {
			whereClauses[i] = "content LIKE ? COLLATE NOCASE"
			args = append(args, "%"+t+"%")
		}
		filter := "subscriber_id != ?" + systemFilter + " AND (" + strings.Join(whereClauses, " OR ") + ")"
		return s.countMentionsUnread(ctx, project, subscriberID, sub.JobTitle, sub.LastReadID, filter, args)
	}

	// Group-based mode: count messages from group members only
	var memberIDs []string
	err = s.reader().SelectContext(ctx, &memberIDs,
		"SELECT subscriber_id FROM board_groups WHERE project = ? AND group_id = ?",
		project, receiveMode)
	if err != nil || len(memberIDs) == 0 {
		return 0, nil
	}

	placeholders := make([]string, len(memberIDs))
	args := []interface{}{project, sub.LastReadID, subscriberID}
	for i, id := range memberIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}

	var count int
	query := fmt.Sprintf(
		`SELECT COUNT(*) FROM board_messages
		 WHERE project = ? AND id > ? AND subscriber_id != ?`+systemFilter+` AND subscriber_id IN (%s)`,
		strings.Join(placeholders, ","))
	err = s.reader().GetContext(ctx, &count, query, args...)
	return count, err
}

// LatestUnreadMessageID returns the newest message eligible for a subscriber's
// current receive mode without advancing its cursor. It lets delivery code
// distinguish a new one-message batch from the previous one-message batch.
func (s *Store) LatestUnreadMessageID(ctx context.Context, project, subscriberID string) (int64, error) {
	var sub struct {
		LastReadID  int64  `db:"last_read_id"`
		JobTitle    string `db:"job_title"`
		ReceiveMode string `db:"receive_mode"`
		CanPeek     int    `db:"can_peek"`
	}
	if err := s.reader().GetContext(ctx, &sub, "SELECT last_read_id, job_title, receive_mode, can_peek FROM board_subscribers WHERE project=? AND subscriber_id=?", project, subscriberID); err != nil {
		return 0, nil
	}
	if sub.ReceiveMode == "none" {
		return 0, nil
	}
	args := []interface{}{project, sub.LastReadID, subscriberID}
	where := "m.project=? AND m.id>? AND m.subscriber_id!=? AND m.subscriber_id!='Coral Task Queue'"
	if sub.ReceiveMode != "all" && !isOrchestrator(subscriberID, sub.JobTitle, sub.CanPeek) {
		args[1] = max(sub.LastReadID, s.unreadFloor(ctx, project)) // within the unread window (#3875)
		return s.confirmedMention(ctx, project, subscriberID, sub.JobTitle, where, args, true)
	}
	var latest int64
	err := s.reader().GetContext(ctx, &latest, "SELECT COALESCE(MAX(m.id),0) FROM board_messages m WHERE "+where, args...)
	return latest, err
}

// confirmedMention returns a message matching where (and args) that tags subscriberID, or 0:
// the newest one when newest is set, else any (a cheaper plan, for a yes/no). The SQL LIKE only finds candidates; each is confirmed with the rule
// ReadMessages and CheckUnread use (containsExplicitTag with the board's numbered names: word
// boundaries, and "@Backend Dev 2" isn't "@Backend Dev" on a board that has a "Backend Dev 2"),
// so the notifier never nudges an agent for a message its read wouldn't show it (#3549).
func (s *Store) confirmedMention(ctx context.Context, project, subscriberID, jobTitle, where string, args []interface{}, newest bool) (int64, error) {
	terms := mentionTerms(subscriberID, jobTitle)
	if len(terms) == 0 {
		return 0, nil
	}
	parts := make([]string, len(terms))
	args = append([]interface{}{}, args...)
	for i, term := range terms {
		parts[i] = "m.content LIKE ? COLLATE NOCASE"
		args = append(args, "%"+term+"%")
	}
	query := "SELECT m.id, m.content FROM board_messages m WHERE " + where + " AND (" + strings.Join(parts, " OR ") + ")"
	if newest {
		query += " ORDER BY m.id DESC"
	}
	// The numbered names are read before the candidates' cursor is opened, so this never holds
	// two reader connections at once (Security's #3549 review).
	numbered := s.numberedNames(ctx, s.reader(), project)
	rows, err := s.reader().QueryxContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var content string
		if err := rows.Scan(&id, &content); err != nil {
			return 0, err
		}
		if containsExplicitTag(content, subscriberID, jobTitle, numbered) {
			return id, nil
		}
	}
	return 0, rows.Err()
}

// HasUnreadHealthMessage reports whether the subscriber has an unread message
// emitted by the health monitor. Sender identity is authoritative so ordinary
// worker text cannot be classified as health.
func (s *Store) HasUnreadHealthMessage(ctx context.Context, project, subscriberID string) (bool, error) {
	return s.hasUnreadSenderMessage(ctx, project, subscriberID, "Coral Health Monitor", false)
}

// HasUnreadNonHealthMessage reports whether an eligible unread message was
// posted by anyone other than the health monitor. It lets worker sessions with
// receive-all mode retain ordinary notifications without being nudged solely
// for an Orchestrator health backlog.
// HasUnreadMention reports whether an unread message (from someone else, not the task queue)
// tags the subscriber: its id or job title, a numbered name of its own, @all or @notify-all,
// by containsExplicitTag, the rule the notifier and the reads use (#3578: the gate types a
// mention in mid-turn and holds a routine nudge until the turn ends). Its receive mode doesn't
// matter: an agent that receives everything is still mentioned by a message that tags it.
func (s *Store) HasUnreadMention(ctx context.Context, project, subscriberID string) (bool, error) {
	var sub struct {
		LastReadID int64  `db:"last_read_id"`
		JobTitle   string `db:"job_title"`
	}
	if err := s.reader().GetContext(ctx, &sub, "SELECT last_read_id, job_title FROM board_subscribers WHERE project=? AND subscriber_id=?", project, subscriberID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	id, err := s.confirmedMention(ctx, project, subscriberID, sub.JobTitle,
		"m.project=? AND m.id>? AND m.subscriber_id!=? AND m.subscriber_id!='Coral Task Queue'",
		[]interface{}{project, max(sub.LastReadID, s.unreadFloor(ctx, project)), subscriberID}, false) // within the unread window (#3875)
	return id > 0, err
}

func (s *Store) HasUnreadNonHealthMessage(ctx context.Context, project, subscriberID string) (bool, error) {
	return s.hasUnreadSenderMessage(ctx, project, subscriberID, "Coral Health Monitor", true)
}

func (s *Store) hasUnreadSenderMessage(ctx context.Context, project, subscriberID, sender string, excludeSender bool) (bool, error) {
	var sub struct {
		LastReadID  int64  `db:"last_read_id"`
		JobTitle    string `db:"job_title"`
		ReceiveMode string `db:"receive_mode"`
		CanPeek     int    `db:"can_peek"`
	}
	if err := s.reader().GetContext(ctx, &sub, "SELECT last_read_id, job_title, receive_mode, can_peek FROM board_subscribers WHERE project=? AND subscriber_id=?", project, subscriberID); err != nil {
		return false, nil
	}
	if sub.ReceiveMode == "none" {
		return false, nil
	}
	args := []interface{}{project, sub.LastReadID, subscriberID, sender}
	senderClause := "m.subscriber_id=?"
	if excludeSender {
		senderClause = "m.subscriber_id!=?"
	}
	where := "m.project=? AND m.id>? AND m.subscriber_id!=? AND m.subscriber_id!='Coral Task Queue' AND " + senderClause
	if sub.ReceiveMode != "all" && !isOrchestrator(subscriberID, sub.JobTitle, sub.CanPeek) {
		args[1] = max(sub.LastReadID, s.unreadFloor(ctx, project)) // within the unread window (#3875)
		id, err := s.confirmedMention(ctx, project, subscriberID, sub.JobTitle, where, args, false)
		return id > 0, err
	}
	var found int
	err := s.reader().GetContext(ctx, &found, "SELECT 1 FROM board_messages m WHERE "+where+" LIMIT 1", args...)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return found == 1, err
}

// GetAllUnreadCounts returns unread counts for all subscribers, respecting each subscriber's receive_mode.
// Returns map keyed by session_name (tmux session identifier) for compatibility with live session lookups.
func (s *Store) GetAllUnreadCounts(ctx context.Context) (map[string]int, error) {
	return s.getUnreadCounts(ctx, nil)
}

// GetUnreadCountsForSessions excludes subscriptions unrelated to live refresh.
func (s *Store) GetUnreadCountsForSessions(ctx context.Context, sessionNames []string) (map[string]int, error) {
	if len(sessionNames) == 0 {
		return map[string]int{}, nil
	}
	return s.getUnreadCounts(ctx, sessionNames)
}

func (s *Store) getUnreadCounts(ctx context.Context, sessionNames []string) (map[string]int, error) {
	return s.countUnread(ctx, sessionNames, true)
}

func (s *Store) countUnread(ctx context.Context, sessionNames []string, retry bool) (map[string]int, error) {
	var subs []struct {
		Project      string `db:"project"`
		SubscriberID string `db:"subscriber_id"`
		SessionName  string `db:"session_name"`
		JobTitle     string `db:"job_title"`
		LastReadID   int64  `db:"last_read_id"`
		ReceiveMode  string `db:"receive_mode"`
	}
	query := "SELECT project, subscriber_id, session_name, job_title, last_read_id, receive_mode FROM board_subscribers WHERE is_active = 1"
	var args []any
	if sessionNames != nil {
		var err error
		query, args, err = sqlx.In(query+" AND session_name IN (?)", sessionNames)
		if err != nil {
			return nil, err
		}
	}
	if err := s.reader().SelectContext(ctx, &subs, s.reader().Rebind(query), args...); err != nil {
		return nil, err
	}
	if len(subs) == 0 {
		return map[string]int{}, nil
	}
	projects := make([]string, 0, len(subs))
	seen := make(map[string]bool)
	for _, sub := range subs {
		if !seen[sub.Project] {
			seen[sub.Project] = true
			projects = append(projects, sub.Project)
		}
	}
	var groupRows []struct {
		Project      string `db:"project"`
		GroupID      string `db:"group_id"`
		SubscriberID string `db:"subscriber_id"`
	}
	query, args, err := sqlx.In("SELECT project, group_id, subscriber_id FROM board_groups WHERE project IN (?)", projects)
	if err != nil {
		return nil, err
	}
	if err := s.reader().SelectContext(ctx, &groupRows, s.reader().Rebind(query), args...); err != nil {
		return nil, err
	}

	type groupKey struct{ project, groupID string }
	groupsByKey := make(map[groupKey]map[string]bool)
	for _, gr := range groupRows {
		key := groupKey{gr.Project, gr.GroupID}
		if groupsByKey[key] == nil {
			groupsByKey[key] = make(map[string]bool)
		}
		groupsByKey[key][gr.SubscriberID] = true
	}

	// Group subscribers by project. Counts are incremental (see
	// unread_cache.go): each subscriber resumes at its cached watermark, and a
	// project's messages are streamed once from the lowest of them.
	type subInfo struct {
		SubscriberID string
		SessionName  string
		JobTitle     string
		LastReadID   int64
		ReceiveMode  string
		key          sidebarUnreadKey
		start        int64
		count        int
	}
	epoch := s.unreadCounts.epoch.Load()
	result := make(map[string]int)
	byProject := make(map[string][]*subInfo)
	for _, sub := range subs {
		result[sub.SessionName] += 0
		rm := sub.ReceiveMode
		if rm == "none" {
			continue
		}
		if rm == "" {
			rm = "mentions"
		}
		info := &subInfo{SubscriberID: sub.SubscriberID, SessionName: sub.SessionName, JobTitle: sub.JobTitle, LastReadID: sub.LastReadID, ReceiveMode: rm,
			key: sidebarUnreadKey{sub.Project, sub.SubscriberID, sub.SessionName}}
		info.start, info.count = s.unreadCounts.sidebarUnreadStart(info.key, sub.LastReadID, sub.JobTitle, rm, epoch)
		byProject[sub.Project] = append(byProject[sub.Project], info)
	}

	for project, projectSubs := range byProject {
		var hi int64
		if err := s.reader().GetContext(ctx, &hi, "SELECT COALESCE(MAX(id), 0) FROM board_messages WHERE project = ?", project); err != nil {
			return nil, err
		}
		// A subscriber that receives everything is counted by the index: streaming every
		// message's content for it took minutes on a long board (#3875). The others are
		// matched in Go, and a first count (nothing cached) looks back at most unreadWindow
		// messages.
		var streamed []*subInfo
		floor := int64(-1)
		for _, sub := range projectSubs {
			if sub.ReceiveMode == "all" {
				var n int
				if err := s.reader().GetContext(ctx, &n,
					"SELECT COUNT(*) FROM board_messages WHERE project = ? AND id > ? AND id <= ? AND subscriber_id != ? AND subscriber_id != 'Coral Task Queue'",
					project, sub.start, hi, sub.SubscriberID); err != nil {
					return nil, err
				}
				sub.count += n
				continue
			}
			if sub.start == sub.LastReadID {
				if floor < 0 {
					floor = s.unreadFloor(ctx, project)
				}
				sub.start = max(sub.start, floor)
			}
			streamed = append(streamed, sub)
		}
		if len(streamed) > 0 {
			minStart := streamed[0].start
			for _, sub := range streamed {
				minStart = min(minStart, sub.start)
			}
			numbered := s.numberedNames(ctx, s.reader(), project)
			// Stream messages instead of retaining every unread body in memory.
			rows, err := s.reader().QueryxContext(ctx,
				"SELECT id, subscriber_id, content FROM board_messages WHERE project = ? AND id > ? AND id <= ? AND subscriber_id != 'Coral Task Queue' ORDER BY id",
				project, minStart, hi)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var id int64
				var sender, content string
				if err := rows.Scan(&id, &sender, &content); err != nil {
					rows.Close()
					return nil, err
				}
				for _, sub := range streamed {
					if id <= sub.start || sender == sub.SubscriberID {
						continue
					}
					eligible := false
					switch sub.ReceiveMode {
					case "mentions":
						eligible = containsExplicitTag(content, sub.SubscriberID, sub.JobTitle, numbered)
					default:
						eligible = groupsByKey[groupKey{project, sub.ReceiveMode}][sender]
					}
					if eligible {
						sub.count++
					}
				}
			}
			readErr := rows.Err()
			closeErr := rows.Close()
			if readErr != nil {
				return nil, readErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
		}
		for _, sub := range projectSubs {
			result[sub.SessionName] += sub.count
			s.unreadCounts.storeSidebarUnread(sub.key, sidebarUnreadEntry{lastReadID: sub.LastReadID, jobTitle: sub.JobTitle, receiveMode: sub.ReceiveMode,
				scannedTo: max(hi, sub.start), count: sub.count, epoch: epoch})
		}
	}
	if retry && s.unreadCounts.epoch.Load() != epoch {
		// Messages were deleted or groups changed meanwhile: cached bases may
		// be stale, so count again.
		return s.countUnread(ctx, sessionNames, false)
	}
	return result, nil
}

// GetAllUnreadStates returns unread counts together with the latest eligible
// message ID for each active session. The notifier uses this pair when seeding
// after startup so a new same-count batch is not mistaken for the seeded one.
func (s *Store) GetAllUnreadStates(ctx context.Context) (map[string]UnreadState, error) {
	return s.getUnreadStates(ctx, nil)
}

// GetUnreadStatesFor is GetAllUnreadStates for these sessions only (the notifier seeds the
// agents that are running, #3875). An empty list gives an empty map.
func (s *Store) GetUnreadStatesFor(ctx context.Context, sessionNames []string) (map[string]UnreadState, error) {
	if len(sessionNames) == 0 {
		return map[string]UnreadState{}, nil
	}
	return s.getUnreadStates(ctx, sessionNames)
}

func (s *Store) getUnreadStates(ctx context.Context, sessionNames []string) (map[string]UnreadState, error) {
	counts, err := s.getUnreadCounts(ctx, sessionNames)
	if err != nil {
		return nil, err
	}
	states := make(map[string]UnreadState, len(counts))
	var subs []struct {
		Project      string `db:"project"`
		SubscriberID string `db:"subscriber_id"`
		SessionName  string `db:"session_name"`
		ReceiveMode  string `db:"receive_mode"`
		JobTitle     string `db:"job_title"`
		CanPeek      int    `db:"can_peek"`
	}
	query := `SELECT project, subscriber_id, session_name, COALESCE(receive_mode, '') AS receive_mode,
		        COALESCE(job_title, '') AS job_title, COALESCE(can_peek, 0) AS can_peek
		 FROM board_subscribers WHERE is_active = 1`
	var args []any
	if sessionNames != nil {
		if query, args, err = sqlx.In(query+" AND session_name IN (?)", sessionNames); err != nil {
			return nil, err
		}
	}
	if err := s.reader().SelectContext(ctx, &subs, s.reader().Rebind(query), args...); err != nil {
		return nil, err
	}
	for _, sub := range subs {
		// A mentions-mode subscriber with nothing unread has no unread message that tags it:
		// the count above and LatestUnreadMessageID's search confirm mentions by the same
		// rule over the same messages. The search would read every message after its cursor
		// and find none, which for a subscriber that never read is the board's whole history
		// (#3845: 40 such subscribers kept a start with 300,000 messages from serving for
		// minutes). A group-mode count is by sender, not mention, and an orchestrator's latest
		// is any message's, so those still search.
		if counts[sub.SessionName] == 0 && (sub.ReceiveMode == "" || sub.ReceiveMode == "mentions") &&
			!isOrchestrator(sub.SubscriberID, sub.JobTitle, sub.CanPeek) {
			states[sub.SessionName] = UnreadState{}
			continue
		}
		latest, err := s.LatestUnreadMessageID(ctx, sub.Project, sub.SubscriberID)
		if err != nil {
			return nil, err
		}
		states[sub.SessionName] = UnreadState{Count: counts[sub.SessionName], LatestID: latest}
	}
	return states, nil
}

// ── Groups ───────────────────────────────────────────────────────────

// AddToGroup adds a subscriber to a board group.
func (s *Store) AddToGroup(ctx context.Context, project, groupID, subscriberID string) error {
	s.unreadCounts.invalidate() // before and after, as for message deletes
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO board_groups (project, group_id, session_id, subscriber_id) VALUES (?, ?, ?, ?)
		 ON CONFLICT(project, group_id, session_id) DO NOTHING`,
		project, groupID, subscriberID, subscriberID)
	s.unreadCounts.invalidate()
	return err
}

// RemoveFromGroup removes a subscriber from a board group. Returns true if removed.
func (s *Store) RemoveFromGroup(ctx context.Context, project, groupID, subscriberID string) (bool, error) {
	s.unreadCounts.invalidate() // before and after, as for message deletes
	result, err := s.db.ExecContext(ctx,
		"DELETE FROM board_groups WHERE project = ? AND group_id = ? AND subscriber_id = ?",
		project, groupID, subscriberID)
	s.unreadCounts.invalidate()
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	return n > 0, nil
}

// ListGroupMembers returns subscriber_ids in a group.
func (s *Store) ListGroupMembers(ctx context.Context, project, groupID string) ([]string, error) {
	var members []string
	err := s.reader().SelectContext(ctx, &members,
		"SELECT subscriber_id FROM board_groups WHERE project = ? AND group_id = ? ORDER BY subscriber_id",
		project, groupID)
	if err != nil {
		return []string{}, nil
	}
	return members, nil
}

// ListGroups returns all groups for a project with member counts.
func (s *Store) ListGroups(ctx context.Context, project string) ([]GroupInfo, error) {
	var groups []GroupInfo
	err := s.reader().SelectContext(ctx, &groups,
		`SELECT group_id, COUNT(*) as member_count
		 FROM board_groups WHERE project = ?
		 GROUP BY group_id ORDER BY group_id`,
		project)
	if err != nil {
		return []GroupInfo{}, nil
	}
	return groups, nil
}

// DeleteMessage deletes a single message by ID.
// DeleteMessage deletes one message of a board: an id from another board deletes nothing
// (#3668, the board authorization audit's finding 1).
func (s *Store) DeleteMessage(ctx context.Context, project string, messageID int64) (bool, error) {
	s.unreadCounts.invalidate()
	forgetMentionTokens(ctx, s.db, "id = ? AND project = ?", messageID, project)
	result, err := s.db.ExecContext(ctx, "DELETE FROM board_messages WHERE id = ? AND project = ?", messageID, project)
	if err != nil {
		return false, err
	}
	s.unreadCounts.invalidate()
	n, _ := result.RowsAffected()
	return n > 0, nil
}

// ── Projects ─────────────────────────────────────────────────────────

// ListProjects returns all known projects with subscriber and message counts.
func (s *Store) ListProjects(ctx context.Context) ([]ProjectInfo, error) {
	var projects []ProjectInfo
	err := s.reader().SelectContext(ctx, &projects,
		`SELECT project,
		        (SELECT COUNT(*) FROM board_subscribers s WHERE s.project = p.project AND s.is_active = 1) as subscriber_count,
		        (SELECT COUNT(*) FROM board_messages m WHERE m.project = p.project) as message_count
		 FROM (
		     SELECT DISTINCT project FROM board_subscribers WHERE is_active = 1
		     UNION
		     SELECT DISTINCT project FROM board_messages
		 ) p ORDER BY project`)
	if err == nil {
		ids := make([]string, len(projects))
		for i := range projects {
			ids[i] = projects[i].Project
		}
		labels := s.BoardLabels(ctx, ids)
		for i := range projects {
			projects[i].BoardLabel = labels[projects[i].Project]
		}
	}
	return projects, err
}

// EnrichedProject holds project info with timestamps and participant names.
type EnrichedProject struct {
	Project          string  `db:"project" json:"project"`
	BoardLabel       string  `db:"-" json:"board_label"` // the board's display name (#3536)
	SubscriberCount  int     `db:"subscriber_count" json:"subscriber_count"`
	MessageCount     int     `db:"message_count" json:"message_count"`
	FirstMessageAt   *string `db:"first_message_at" json:"first_message_at"`
	LastMessageAt    *string `db:"last_message_at" json:"last_message_at"`
	ParticipantNames *string `db:"participant_names" json:"participant_names"`
}

// ListProjectsEnriched returns board projects with timestamps, subscriber info, and participant names.
func (s *Store) ListProjectsEnriched(ctx context.Context) ([]EnrichedProject, error) {
	var projects []EnrichedProject
	err := s.reader().SelectContext(ctx, &projects,
		`SELECT
		     p.project,
		     (SELECT COUNT(*) FROM board_subscribers s WHERE s.project = p.project AND s.is_active = 1) as subscriber_count,
		     (SELECT COUNT(*) FROM board_messages m WHERE m.project = p.project) as message_count,
		     -- The first and last rows by id: a seek on (project, id) instead of
		     -- scanning every message's created_at. Messages are stamped at
		     -- insert, so id order is time order.
		     (SELECT created_at FROM board_messages m WHERE m.project = p.project ORDER BY m.id ASC LIMIT 1) as first_message_at,
		     (SELECT created_at FROM board_messages m WHERE m.project = p.project ORDER BY m.id DESC LIMIT 1) as last_message_at,
		     (SELECT GROUP_CONCAT(s.job_title, ', ')
		      FROM board_subscribers s WHERE s.project = p.project AND s.is_active = 1
		      ORDER BY s.subscribed_at LIMIT 5) as participant_names
		 FROM (
		     SELECT DISTINCT project FROM board_subscribers WHERE is_active = 1
		     UNION
		     SELECT DISTINCT project FROM board_messages
		 ) p
		 ORDER BY last_message_at DESC`)
	if err == nil {
		ids := make([]string, len(projects))
		for i := range projects {
			ids[i] = projects[i].Project
		}
		labels := s.BoardLabels(ctx, ids)
		for i := range projects {
			projects[i].BoardLabel = labels[projects[i].Project]
		}
	}
	return projects, err
}

// SearchMessages returns project names that have messages matching the query (LIKE search).
func (s *Store) SearchMessages(ctx context.Context, query string) ([]string, error) {
	var projects []string
	err := s.reader().SelectContext(ctx, &projects,
		"SELECT DISTINCT project FROM board_messages WHERE content LIKE ? COLLATE NOCASE",
		"%"+query+"%")
	if err != nil {
		return []string{}, nil
	}
	return projects, nil
}

// DeleteProject removes all messages and subscribers for a project.
func (s *Store) DeleteProject(ctx context.Context, project string) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	s.unreadCounts.invalidate()
	forgetMentionTokens(ctx, tx, "project = ?", project)
	if _, err := tx.ExecContext(ctx, "DELETE FROM board_messages WHERE project = ?", project); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM board_subscribers WHERE project = ?", project); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM board_workflow_presets WHERE board_id = ?", project); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM board_working_modes WHERE board_id = ?", project); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM board_groups WHERE project = ?", project); err != nil {
		return err
	}
	err = tx.Commit()
	s.unreadCounts.invalidate()
	return err
}

// ── Tasks ───────────────────────────────────────────────────────────

// getTaskByID fetches a task by ID and board_id.
func (s *Store) getTaskByID(ctx context.Context, project string, taskID int64) (*Task, error) {
	var t Task
	err := s.reader().GetContext(ctx, &t,
		"SELECT id, revision, board_id, title, body, status, priority, created_by, assigned_to, completed_by, completion_message, created_at, claimed_at, completed_at, session_id, cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, parked_reason, parked_until, remind_at FROM board_tasks WHERE id = ? AND board_id = ?", taskID, project)
	if err != nil {
		return nil, err
	}
	if err := s.hydrateWorkflow(ctx, &t); err != nil {
		return nil, err
	}
	t.BlockedBy, err = s.GetTaskDependencies(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// CreateTaskOpts holds optional parameters for CreateTask.
type CreateTaskOpts struct {
	BlockedBy []TaskDep
	MaxDepth  int
	Draft     bool
	Workflow  TaskWorkflow
}

// CreateTask inserts a task and returns it.
func (s *Store) CreateTask(ctx context.Context, project, title, body, priority, createdBy string, assignedTo ...string) (*Task, error) {
	return s.CreateTaskWithOpts(ctx, project, title, body, priority, createdBy, nil, assignedTo...)
}

// CreateTaskWithOpts inserts a task with optional dependency configuration.
func (s *Store) CreateTaskWithOpts(ctx context.Context, project, title, body, priority, createdBy string, opts *CreateTaskOpts, assignedTo ...string) (*Task, error) {
	w := TaskWorkflow{}
	if opts != nil {
		w = opts.Workflow
	}
	w, err := s.prepareWorkflow(ctx, project, w)
	if err != nil {
		return nil, err
	}
	if priority == "" {
		priority = "medium"
	}
	now := nowUTC()

	var bodyPtr *string
	if body != "" {
		bodyPtr = &body
	}
	var assignPtr *string
	if len(assignedTo) > 0 && assignedTo[0] != "" {
		assignPtr = &assignedTo[0]
	}

	initialStatus := "draft"

	result, err := s.db.ExecContext(ctx,
		`INSERT INTO board_tasks (board_id, title, body, status, priority, created_by, assigned_to, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		project, title, bodyPtr, initialStatus, priority, createdBy, assignPtr, now)
	if err != nil {
		return nil, err
	}
	taskID, _ := result.LastInsertId()
	s.ensureBoardRow(ctx, project)
	// Keep new tasks unclaimable until configuration and dependencies are stored.
	if err := saveWorkflow(ctx, s.db, taskID, w); err != nil {
		return nil, err
	}

	if opts != nil && len(opts.BlockedBy) > 0 {
		maxDepth := opts.MaxDepth
		if maxDepth <= 0 {
			maxDepth = 3
		}
		if err := s.AddTaskDependencies(ctx, project, taskID, opts.BlockedBy, maxDepth); err != nil {
			s.db.ExecContext(ctx, "DELETE FROM task_workflows WHERE task_id = ?", taskID)
			s.db.ExecContext(ctx, "DELETE FROM board_tasks WHERE id = ? AND board_id = ?", taskID, project)
			return nil, err
		}
	}

	if opts == nil || !opts.Draft {
		task, err := s.PublishTask(ctx, project, taskID)
		if err != nil {
			return nil, err
		}
		if w.RetryOf != 0 {
			if err := s.RewireRetryDependents(ctx); err != nil {
				return nil, err
			}
			task, err = s.getTaskByID(ctx, project, taskID)
			if err != nil {
				return nil, err
			}
		}
		return task, nil
	}
	return s.getTaskByID(ctx, project, taskID)
}

// ListTasks returns all tasks for a project, ordered by priority then ID.
func (s *Store) ListTasks(ctx context.Context, project string) ([]Task, error) {
	var tasks []Task
	err := s.reader().SelectContext(ctx, &tasks,
		`SELECT id, revision, board_id, title, body, status, priority, created_by, assigned_to, completed_by, completion_message, created_at, claimed_at, completed_at, session_id, cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, parked_reason, parked_until, remind_at FROM board_tasks WHERE board_id = ?
		 ORDER BY CASE priority WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 END, id ASC`,
		project)
	if err != nil {
		return nil, err
	}
	s.populateTaskDeps(ctx, tasks)
	return tasks, nil
}

// ListOpenTasks returns the board's pending, in-progress, blocked and draft
// tasks with their BlockedBy dependencies, but without workflow hydration
// (Task.Workflow is left zero). It is the cheap read for frequently polled views
// such as the team status endpoint: ListTasks hydrates every task, completed
// ones included, with several queries each, which on a long-lived board means
// thousands of queries holding the single connection per call.
func (s *Store) ListOpenTasks(ctx context.Context, project string) ([]Task, error) {
	var tasks []Task
	err := s.reader().SelectContext(ctx, &tasks,
		`SELECT id, revision, board_id, title, body, status, priority, created_by, assigned_to, completed_by, completion_message, created_at, claimed_at, completed_at, session_id, cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, parked_reason, parked_until, remind_at FROM board_tasks
		 WHERE board_id = ? AND status IN ('pending', 'in_progress', 'blocked', 'draft')
		 ORDER BY CASE priority WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 END, id ASC`,
		project)
	if err != nil {
		return nil, err
	}
	s.loadBlockedBy(ctx, tasks)
	return tasks, nil
}

// TasksUnderReview returns a board's tasks waiting for their completion review, by id,
// with their assignees: a candidate submitted for review (still in_progress, or
// review_pending once the worker's slot is released). The team status says
// "awaiting-review" only for these (#3520).
func (s *Store) TasksUnderReview(ctx context.Context, project string) (map[int64]string, error) {
	var rows []struct {
		ID         int64  `db:"id"`
		AssignedTo string `db:"assigned_to"`
	}
	err := s.reader().SelectContext(ctx, &rows,
		`SELECT t.id, COALESCE(t.assigned_to, '') AS assigned_to FROM board_tasks t
		 WHERE t.board_id = ? AND (t.status = 'review_pending' OR (t.status = 'in_progress' AND EXISTS (
		   SELECT 1 FROM task_workflows w WHERE w.task_id = t.id AND json_extract(w.data, '$.completion_review') IS NOT NULL)))`, project)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(rows))
	for _, r := range rows {
		out[r.ID] = r.AssignedTo
	}
	return out, nil
}

// ListAllTasks returns the most recent tasks across all boards.
func (s *Store) ListAllTasks(ctx context.Context, limit int) ([]Task, error) {
	if limit <= 0 {
		limit = 100
	}
	var tasks []Task
	err := s.reader().SelectContext(ctx, &tasks,
		`SELECT id, revision, board_id, title, body, status, priority, created_by, assigned_to, completed_by, completion_message, created_at, claimed_at, completed_at, session_id, cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, parked_reason, parked_until, remind_at FROM board_tasks
		 ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	s.populateTaskDeps(ctx, tasks)
	return tasks, nil
}

// populateTaskDeps batch-loads dependencies for a slice of tasks.
func (s *Store) populateTaskDeps(ctx context.Context, tasks []Task) {
	s.hydrateWorkflows(ctx, tasks)
	s.loadBlockedBy(ctx, tasks)
}

// loadBlockedBy batch-loads the BlockedBy dependencies for a slice of tasks.
func (s *Store) loadBlockedBy(ctx context.Context, tasks []Task) {
	if len(tasks) == 0 {
		return
	}
	ids := make([]int64, len(tasks))
	idMap := make(map[int64]*Task, len(tasks))
	for i := range tasks {
		ids[i] = tasks[i].ID
		idMap[tasks[i].ID] = &tasks[i]
	}

	// Build IN clause
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(
		`SELECT td.task_id, td.blocked_by_task_id, td.blocked_by_board_id, bt.title, bt.status
		 FROM task_dependencies td
		 LEFT JOIN board_tasks bt ON bt.id = td.blocked_by_task_id
		 WHERE td.task_id IN (%s)`, strings.Join(placeholders, ","))

	var rows []struct {
		TaskID           int64   `db:"task_id"`
		BlockedByTaskID  int64   `db:"blocked_by_task_id"`
		BlockedByBoardID string  `db:"blocked_by_board_id"`
		Title            *string `db:"title"`
		Status           *string `db:"status"`
	}
	if err := s.reader().SelectContext(ctx, &rows, query, args...); err != nil {
		return
	}
	if len(rows) == 0 {
		return
	}
	// Like loadDependencyRule per dependency, but batched; on error the
	// dependencies are listed without rule evaluation, as before.
	rules, rulesErr := s.loadDependencyRules(ctx, ids)
	for _, r := range rows {
		t := idMap[r.TaskID]
		if t == nil {
			continue
		}
		dep := TaskDep{TaskID: r.BlockedByTaskID, BoardID: r.BlockedByBoardID}
		if rulesErr == nil {
			applyDependencyRule(&dep, rules[dependencyRuleKey{t.ID, dep.TaskID}])
		}
		if r.Title != nil {
			dep.Title = *r.Title
		}
		if r.Status != nil {
			dep.Status = *r.Status
		}
		t.BlockedBy = append(t.BlockedBy, dep)
	}
}

// HasActiveTaskForAssignee returns true when the assignee already has another
// in-progress task on the same board. excludeTaskID can be used to ignore the
// task currently being created or reassigned.
func (s *Store) HasActiveTaskForAssignee(ctx context.Context, project, assignee string, excludeTaskID int64) (bool, error) {
	if assignee == "" {
		return false, nil
	}

	var count int
	err := s.reader().GetContext(ctx, &count,
		`SELECT COUNT(1) FROM board_tasks
		 WHERE board_id = ? AND assigned_to = ? AND status = 'in_progress' AND id != ?`,
		project, assignee, excludeTaskID)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// FindIdleSubscriber returns a random active subscriber with no in-progress tasks, or nil.
// skip, when set, excludes subscribers the board cannot judge on its own (for
// example archived agents, which the session store knows about).
func (s *Store) FindIdleSubscriber(ctx context.Context, project string, skip func(*Subscriber) bool) *Subscriber {
	var subs []Subscriber
	err := s.reader().SelectContext(ctx, &subs,
		`SELECT * FROM board_subscribers
		 WHERE project = ? AND is_active = 1 AND session_name != ''
		 AND subscriber_id NOT IN (
		     SELECT assigned_to FROM board_tasks WHERE board_id = ? AND status = 'in_progress' AND assigned_to IS NOT NULL
		 )
		 ORDER BY RANDOM()`, project, project)
	if err != nil {
		return nil
	}
	for i := range subs {
		if skip == nil || !skip(&subs[i]) {
			return &subs[i]
		}
	}
	return nil
}

// ActiveTaskForSubscriber returns the subscriber's current in-progress task, or nil.
func (s *Store) ActiveTaskForSubscriber(ctx context.Context, project, subscriberID string) *Task {
	var task Task
	err := s.reader().GetContext(ctx, &task,
		`SELECT id, revision, board_id, title, body, status, priority, created_by, assigned_to, completed_by, completion_message, created_at, claimed_at, completed_at, session_id, cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens
		 FROM board_tasks WHERE board_id = ? AND assigned_to = ? AND status = 'in_progress'
		 ORDER BY claimed_at DESC LIMIT 1`, project, subscriberID)
	if err != nil {
		return nil
	}
	result, err := s.getTaskByID(ctx, project, task.ID)
	if err != nil {
		return nil
	}
	return result
}

// NextPendingTaskForSubscriber returns the next pending task assigned to or
// available for the subscriber, without claiming it. Returns nil if none.
func (s *Store) NextPendingTaskForSubscriber(ctx context.Context, project, subscriberID string) *Task {
	priorityOrder := `CASE priority WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 END, id ASC`
	var task Task
	// Check assigned tasks first
	err := s.db.GetContext(ctx, &task,
		`SELECT id, revision, board_id, title, body, status, priority, created_by, assigned_to, completed_by, completion_message, created_at, claimed_at, completed_at, session_id, cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens
		 FROM board_tasks WHERE board_id = ? AND status = 'pending' AND assigned_to = ?
		 ORDER BY `+priorityOrder+` LIMIT 1`, project, subscriberID)
	if err == nil {
		return &task
	}
	// Then unassigned
	err = s.db.GetContext(ctx, &task,
		`SELECT id, revision, board_id, title, body, status, priority, created_by, assigned_to, completed_by, completion_message, created_at, claimed_at, completed_at, session_id, cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens
		 FROM board_tasks WHERE board_id = ? AND status = 'pending' AND (assigned_to IS NULL OR assigned_to = '')
		 ORDER BY `+priorityOrder+` LIMIT 1`, project)
	if err == nil {
		return &task
	}
	return nil
}

// ClaimTask finds and atomically claims the best pending task for the subscriber.
// It first looks for tasks assigned to the subscriber, then unassigned tasks.
// Tasks are ordered by priority (critical > high > medium > low), then by ID.
// Returns nil if no tasks are available.
// An agent cannot claim a new task while they have an in-progress task.
func (s *Store) ClaimTask(ctx context.Context, project, subscriberID string, requestedID ...int64) (*Task, error) {
	now := nowUTC()
	priorityOrder := `CASE priority WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 END, id ASC`

	var activeCount int
	if err := s.db.GetContext(ctx, &activeCount,
		`SELECT COUNT(*) FROM board_tasks WHERE board_id = ? AND assigned_to = ? AND status = 'in_progress'`,
		project, subscriberID); err == nil && activeCount > 0 {
		return nil, fmt.Errorf("complete your current task before claiming a new one")
	}

	// Assigned pending work is always considered before the unassigned pool.
	// Keep all candidates so a stale pending row with unmet dependencies cannot
	// hide a later ready task.
	var candidateIDs []int64
	if len(requestedID) > 0 && requestedID[0] > 0 {
		if err := s.db.SelectContext(ctx, &candidateIDs, `SELECT id FROM board_tasks WHERE id = ? AND board_id = ? AND status = 'pending' AND (assigned_to = ? OR assigned_to IS NULL OR assigned_to = '')`, requestedID[0], project, subscriberID); err != nil || len(candidateIDs) == 0 {
			return nil, fmt.Errorf("requested task is not available to this subscriber")
		}
	} else {
		var assigned, unassigned []int64
		if err := s.db.SelectContext(ctx, &assigned, `SELECT id FROM board_tasks WHERE board_id = ? AND status = 'pending' AND assigned_to = ? ORDER BY `+priorityOrder, project, subscriberID); err != nil {
			return nil, err
		}
		if err := s.db.SelectContext(ctx, &unassigned, `SELECT id FROM board_tasks WHERE board_id = ? AND status = 'pending' AND (assigned_to IS NULL OR assigned_to = '') ORDER BY `+priorityOrder, project); err != nil {
			return nil, err
		}
		candidateIDs = append(assigned, unassigned...)
	}

	for _, taskID := range candidateIDs {
		tx, err := s.db.BeginTxx(ctx, nil)
		if err != nil {
			return nil, err
		}
		inputs, ready, err := dependencyInputs(ctx, tx, taskID)
		if err != nil {
			tx.Rollback()
			return nil, err
		}
		if !ready {
			// Reconcile stale queue metadata and continue searching this claim.
			if _, err := tx.ExecContext(ctx, "UPDATE board_tasks SET status = 'blocked' WHERE id = ? AND board_id = ? AND status = 'pending'", taskID, project); err != nil {
				tx.Rollback()
				return nil, err
			}
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			continue
		}
		result, err := tx.ExecContext(ctx,
			`UPDATE board_tasks SET status = 'in_progress', assigned_to = ?, claimed_at = ?, last_activity_at = ?
			 WHERE id = ? AND board_id = ? AND status = 'pending'
			   AND (assigned_to = ? OR assigned_to IS NULL OR assigned_to = '')
			   AND NOT EXISTS (SELECT 1 FROM board_tasks WHERE board_id = ? AND assigned_to = ? AND status = 'in_progress')`,
			subscriberID, now, now, taskID, project, subscriberID, project, subscriberID)
		if err != nil {
			tx.Rollback()
			return nil, err
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			var hasActive int
			tx.GetContext(ctx, &hasActive, `SELECT COUNT(*) FROM board_tasks WHERE board_id = ? AND assigned_to = ? AND status = 'in_progress'`, project, subscriberID)
			tx.Rollback()
			if hasActive > 0 {
				return nil, fmt.Errorf("complete your current task before claiming a new one")
			}
			continue
		}
		w, err := loadWorkflow(ctx, tx, taskID)
		if err != nil {
			tx.Rollback()
			return nil, err
		}
		if w.TeamMode == nil {
			mode, err := loadWorkingMode(ctx, tx, project)
			if err != nil {
				tx.Rollback()
				return nil, err
			}
			w.TeamMode = &mode
			if mode.Instructions != "" {
				w.Instructions += "\n\nTeam working instructions:\n" + mode.Instructions
			}
		}
		w.Inputs = inputs
		if err := saveWorkflow(ctx, tx, taskID, w); err != nil {
			tx.Rollback()
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		if s.sessionsDB != nil {
			var sessionName string
			if err := s.db.GetContext(ctx, &sessionName, `SELECT session_name FROM board_subscribers WHERE subscriber_id = ? AND project = ? AND is_active = 1 LIMIT 1`, subscriberID, project); err == nil && sessionName != "" {
				sessionUUID := naming.SessionIDFromName(sessionName)
				var exists int
				if err := s.sessionsDB.GetContext(ctx, &exists, `SELECT 1 FROM live_sessions WHERE session_id = ? AND status = 'active' LIMIT 1`, sessionUUID); err == nil {
					s.db.ExecContext(ctx, `UPDATE board_tasks SET session_id = ? WHERE id = ? AND board_id = ?`, sessionUUID, taskID, project)
				}
			}
		}
		return s.getTaskByID(ctx, project, taskID)
	}
	return nil, nil
}

// TouchActiveTask records activity by an agent without changing task state.
// It is intentionally best-effort: activity tracking must never block the
// agent's normal board operation.
func (s *Store) TouchActiveTask(ctx context.Context, project, subscriberID string) error {
	if project == "" || subscriberID == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE board_tasks SET last_activity_at = ?
		WHERE board_id = ? AND assigned_to = ? AND status = 'in_progress'`, nowUTC(), project, subscriberID)
	return err
}

// TouchTask records activity for a specific active task.
func (s *Store) TouchTask(ctx context.Context, project string, taskID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE board_tasks SET last_activity_at = ?
		WHERE board_id = ? AND id = ? AND status = 'in_progress'`, nowUTC(), project, taskID)
	return err
}

// SnoozeTaskReminder suppresses inactivity reminders until the supplied time.
func (s *Store) SnoozeTaskReminder(ctx context.Context, project string, taskID int64, until time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE board_tasks SET idle_snoozed_until = ?
		WHERE board_id = ? AND id = ? AND status = 'in_progress'`, until.UTC().Format(time.RFC3339), project, taskID)
	return err
}

// IdleTasks returns active tasks that have had no recorded activity since the
// supplied time. Older databases use claimed_at until activity is recorded.
func (s *Store) IdleTasks(ctx context.Context, project string, before time.Time) ([]Task, error) {
	var tasks []Task
	err := s.reader().SelectContext(ctx, &tasks, `SELECT id, revision, board_id, title, body, status, priority,
		created_by, assigned_to, completed_by, completion_message, created_at, claimed_at,
		completed_at, session_id, cost_usd, input_tokens, output_tokens, cache_read_tokens,
		cache_write_tokens FROM board_tasks
		WHERE board_id = ? AND status = 'in_progress' AND COALESCE(last_activity_at, claimed_at) IS NOT NULL
		AND NOT `+parkedSQL+`
		AND COALESCE(last_activity_at, claimed_at) < ?
		AND (idle_snoozed_until IS NULL OR idle_snoozed_until <= ?)`, project, before.UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339))
	return tasks, err
}

// computeAndStoreTaskCost queries the sessions DB for proxy request costs
// incurred during the task's lifetime and stores them on the task.
func (s *Store) computeAndStoreTaskCost(ctx context.Context, project string, taskID int64) {
	if s.sessionsDB == nil {
		return
	}
	// Fetch the task's session_id and time window.
	var task struct {
		SessionID   *string `db:"session_id"`
		ClaimedAt   *string `db:"claimed_at"`
		CompletedAt *string `db:"completed_at"`
	}
	if err := s.reader().GetContext(ctx, &task,
		`SELECT session_id, claimed_at, completed_at FROM board_tasks WHERE id = ? AND board_id = ?`, taskID, project); err != nil {
		return
	}
	if task.SessionID == nil || task.ClaimedAt == nil || task.CompletedAt == nil {
		return
	}

	var costs struct {
		CostUSD          float64 `db:"cost_usd"`
		InputTokens      int     `db:"input_tokens"`
		OutputTokens     int     `db:"output_tokens"`
		CacheReadTokens  int     `db:"cache_read_tokens"`
		CacheWriteTokens int     `db:"cache_write_tokens"`
	}
	err := s.sessionsDB.GetContext(ctx, &costs,
		`SELECT COALESCE(SUM(cost_usd), 0) AS cost_usd,
		        COALESCE(SUM(input_tokens), 0) AS input_tokens,
		        COALESCE(SUM(output_tokens), 0) AS output_tokens,
		        COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
		        COALESCE(SUM(cache_write_tokens), 0) AS cache_write_tokens
		 FROM token_usage_all
		 WHERE session_id = ? AND recorded_at >= ? AND recorded_at <= ?`,
		*task.SessionID, *task.ClaimedAt, *task.CompletedAt)
	if err != nil {
		return
	}

	s.db.ExecContext(ctx,
		`UPDATE board_tasks
		 SET cost_usd = ?, input_tokens = ?, output_tokens = ?,
		     cache_read_tokens = ?, cache_write_tokens = ?
		 WHERE id = ? AND board_id = ?`,
		costs.CostUSD, costs.InputTokens, costs.OutputTokens,
		costs.CacheReadTokens, costs.CacheWriteTokens, taskID, project)
}

// CompleteTask marks a task as completed. Pending tasks can be completed
// without being claimed first: the operator is never prompted to claim the
// tasks assigned to them, and just marks them done.
func (s *Store) CompleteTask(ctx context.Context, project string, taskID int64, subscriberID string, message *string) (*Task, error) {
	return s.CompleteTaskWithArtifacts(ctx, project, taskID, subscriberID, message, "success", nil)
}

// ReassignTask resets a task back to pending, optionally with a new assignee.
// Works on pending, in_progress, or blocked tasks, preserving blocked status.
// If assignee is empty, the task becomes unassigned.
func (s *Store) ReassignTask(ctx context.Context, project string, taskID int64, assignee string) (*Task, error) {
	var assignPtr *string
	if assignee != "" {
		assignPtr = &assignee
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE board_tasks
		 SET status = CASE WHEN status = 'blocked' THEN 'blocked' ELSE 'pending' END, assigned_to = ?, claimed_at = NULL, session_id = NULL
		 WHERE id = ? AND board_id = ? AND status IN ('pending', 'in_progress', 'blocked') AND NOT EXISTS (SELECT 1 FROM task_workflows w WHERE w.task_id=board_tasks.id AND json_extract(w.data,'$.completion_review') IS NOT NULL)`,
		assignPtr, taskID, project)
	if err != nil {
		return nil, err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("task #%d cannot be reassigned (finished, awaiting review, or not found)", taskID)
	}

	return s.getTaskByID(ctx, project, taskID)
}

// TaskUpdate holds optional fields for updating a task.
// TaskUpdate holds optional fields for updating a task.
type TaskUpdate struct {
	Title      *string    `json:"title,omitempty"`
	Body       *string    `json:"body,omitempty"`
	Priority   *string    `json:"priority,omitempty"`
	AssignedTo *string    `json:"assigned_to,omitempty"`
	BlockedBy  *[]TaskDep `json:"blocked_by,omitempty"`
}

// UpdateTask applies partial updates to a pending, in_progress, or blocked task.
// If AssignedTo changes on an in_progress task, the task resets to pending.
// Returns (updatedTask, previousStatus, error) so the handler can detect status transitions.
func (s *Store) UpdateTask(ctx context.Context, project string, taskID int64, updates TaskUpdate, maxDepth int) (*Task, string, error) {
	task, err := s.getTaskByID(ctx, project, taskID)
	if err != nil {
		return nil, "", fmt.Errorf("task #%d not found", taskID)
	}
	if task.Status == "completed" || task.Status == "skipped" || task.Status == "review_pending" {
		return nil, "", fmt.Errorf("task #%d cannot be edited (status: %s)", taskID, task.Status)
	}
	prevStatus := task.Status
	if updates.BlockedBy != nil && task.Status == "in_progress" {
		return nil, "", fmt.Errorf("dependencies can only change before a task starts")
	}

	setClauses := []string{}
	args := []any{}

	if updates.Title != nil {
		if *updates.Title == "" {
			return nil, "", fmt.Errorf("title cannot be empty")
		}
		setClauses = append(setClauses, "title = ?")
		args = append(args, *updates.Title)
	}
	if updates.Body != nil {
		setClauses = append(setClauses, "body = ?")
		args = append(args, *updates.Body)
	}
	if updates.Priority != nil {
		switch *updates.Priority {
		case "critical", "high", "medium", "low":
		default:
			return nil, "", fmt.Errorf("invalid priority: %s", *updates.Priority)
		}
		setClauses = append(setClauses, "priority = ?")
		args = append(args, *updates.Priority)
	}
	if updates.AssignedTo != nil {
		setClauses = append(setClauses, "assigned_to = ?")
		if *updates.AssignedTo == "" {
			args = append(args, nil)
		} else {
			args = append(args, *updates.AssignedTo)
		}
		if task.Status == "in_progress" {
			setClauses = append(setClauses, "status = 'pending'", "claimed_at = NULL", "session_id = NULL")
		}
	}

	// Handle blocked_by updates
	if updates.BlockedBy != nil {
		if maxDepth <= 0 {
			maxDepth = 3
		}
		if err := s.AddTaskDependencies(ctx, project, taskID, *updates.BlockedBy, maxDepth); err != nil {
			return nil, "", err
		}
	}
	if len(setClauses) > 0 {
		query := fmt.Sprintf("UPDATE board_tasks SET %s WHERE id = ? AND board_id = ? AND status NOT IN ('completed', 'skipped', 'review_pending') AND NOT EXISTS (SELECT 1 FROM task_workflows w WHERE w.task_id=board_tasks.id AND json_extract(w.data,'$.completion_review') IS NOT NULL)", strings.Join(setClauses, ", "))
		args = append(args, taskID, project)
		result, err := s.db.ExecContext(ctx, query, args...)
		if err != nil {
			return nil, "", err
		}
		if n, _ := result.RowsAffected(); n == 0 {
			return nil, "", fmt.Errorf("task has already finished")
		}
	}

	result, err := s.getTaskByID(ctx, project, taskID)
	if err != nil {
		return nil, "", err
	}
	return result, prevStatus, nil
}

// PublishTask transitions a draft task to pending or blocked based on its dependencies.
func (s *Store) PublishTask(ctx context.Context, project string, taskID int64) (*Task, error) {
	task, err := s.getTaskByID(ctx, project, taskID)
	if err != nil {
		return nil, fmt.Errorf("task #%d not found", taskID)
	}
	if task.Status != "draft" {
		return nil, fmt.Errorf("task #%d is not a draft (status: %s)", taskID, task.Status)
	}

	// Check if task has unresolved dependencies.
	_, ready, err := dependencyInputs(ctx, s.db, taskID)
	if err != nil {
		return nil, err
	}
	newStatus := "pending"
	if !ready {
		newStatus = "blocked"
	}

	result, err := s.db.ExecContext(ctx,
		"UPDATE board_tasks SET status = ? WHERE id = ? AND board_id = ? AND status = 'draft'",
		newStatus, taskID, project)
	if err != nil {
		return nil, err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("task is no longer a draft")
	}

	return s.getTaskByID(ctx, project, taskID)
}

// CancelTask marks a task as skipped. Can cancel pending, in_progress, or blocked tasks.
func (s *Store) CancelTask(ctx context.Context, project string, taskID int64, subscriberID string, message *string) (*Task, error) {
	now := nowUTC()
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx,
		`UPDATE board_tasks
		 SET status = 'skipped', completed_by = ?, completion_message = ?, completed_at = ?
		 WHERE id = ? AND board_id = ? AND status IN ('pending', 'in_progress', 'blocked', 'draft', 'review_pending')`,
		subscriberID, message, now, taskID, project)
	if err != nil {
		return nil, err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("task #%d cannot be cancelled (already completed or not found)", taskID)
	}
	if err := resolveCompletedTask(ctx, tx, taskID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.computeAndStoreTaskCost(ctx, project, taskID)

	return s.getTaskByID(ctx, project, taskID)
}

// StallDownstreamTasks marks pending descendants of a terminal prerequisite as
// blocked. Ownership is preserved so the orchestrator can rewire or retry the
// work; blocked rows are never claimable.
func (s *Store) StallDownstreamTasks(ctx context.Context, project string, upstreamID int64) ([]Task, error) {
	queue := []int64{upstreamID}
	seen := map[int64]bool{upstreamID: true}
	var stalled []Task
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		var downstream []struct {
			ID        int64  `db:"task_id"`
			Condition string `db:"condition"`
		}
		if err := s.db.SelectContext(ctx, &downstream, `SELECT DISTINCT d.task_id,
			COALESCE(r.condition, 'success') AS condition FROM task_dependencies d
			JOIN board_tasks t ON t.id = d.task_id
			LEFT JOIN task_dependency_rules r ON r.task_id = d.task_id AND r.upstream_id = d.blocked_by_task_id
			WHERE d.blocked_by_task_id = ? AND d.blocked_by_board_id = ?`, id, project); err != nil {
			return nil, err
		}
		for _, child := range downstream {
			childID := child.ID
			if !seen[childID] {
				seen[childID] = true
				queue = append(queue, childID)
			}
			if child.Condition == "termination" {
				continue
			}
			var status string
			if err := s.db.GetContext(ctx, &status, "SELECT status FROM board_tasks WHERE id = ? AND board_id = ?", childID, project); err != nil {
				continue
			}
			if status != "pending" {
				continue
			}
			if _, err := s.db.ExecContext(ctx, "UPDATE board_tasks SET status = 'blocked' WHERE id = ? AND board_id = ? AND status = 'pending'", childID, project); err != nil {
				return nil, err
			}
			if task, err := s.getTaskByID(ctx, project, childID); err == nil {
				stalled = append(stalled, *task)
			}
		}
	}
	return stalled, nil
}

// ── Task Dependencies ────────────────────────────────────────────

// GetTaskDependencies returns the blocked_by deps for a task with title/status populated.
func (s *Store) GetTaskDependencies(ctx context.Context, taskID int64) ([]TaskDep, error) {
	var deps []struct {
		BlockedByTaskID  int64   `db:"blocked_by_task_id"`
		BlockedByBoardID string  `db:"blocked_by_board_id"`
		Title            *string `db:"title"`
		Status           *string `db:"status"`
	}
	err := s.reader().SelectContext(ctx, &deps,
		`SELECT td.blocked_by_task_id, td.blocked_by_board_id, bt.title, bt.status
		 FROM task_dependencies td
		 LEFT JOIN board_tasks bt ON bt.id = td.blocked_by_task_id
		 WHERE td.task_id = ?`, taskID)
	if err != nil {
		return nil, err
	}
	result := make([]TaskDep, len(deps))
	for i, d := range deps {
		result[i] = TaskDep{
			TaskID:  d.BlockedByTaskID,
			BoardID: d.BlockedByBoardID,
		}
		if d.Title != nil {
			result[i].Title = *d.Title
		}
		if d.Status != nil {
			result[i].Status = *d.Status
		}
		if err := s.loadDependencyRule(ctx, taskID, &result[i]); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// ListDownstreamTasks returns tasks that directly depend on an upstream task.
// It is used for cancellation diagnostics and does not change readiness.
func (s *Store) ListDownstreamTasks(ctx context.Context, project string, upstreamID int64) ([]Task, error) {
	var rows []struct {
		ID      int64  `db:"id"`
		BoardID string `db:"board_id"`
	}
	if err := s.reader().SelectContext(ctx, &rows, `SELECT DISTINCT b.id, b.board_id
		FROM task_dependencies d JOIN board_tasks b ON b.id = d.task_id
		WHERE d.blocked_by_task_id = ? AND d.blocked_by_board_id = ?`, upstreamID, project); err != nil {
		return nil, err
	}
	result := make([]Task, 0, len(rows))
	for _, row := range rows {
		task, err := s.getTaskByID(ctx, row.BoardID, row.ID)
		if err != nil {
			return nil, err
		}
		if task != nil {
			result = append(result, *task)
		}
	}
	return result, nil
}

// AddTaskDependencies inserts dependency rows and sets status to 'blocked'
// if any blockers are unresolved. Validates circular deps and depth limit.
func (s *Store) AddTaskDependencies(ctx context.Context, project string, taskID int64, deps []TaskDep, maxDepth int) error {
	// Fill in default board_id
	var taskStatus string
	if err := s.db.GetContext(ctx, &taskStatus, "SELECT status FROM board_tasks WHERE id = ? AND board_id = ?", taskID, project); err != nil {
		return err
	}
	if taskStatus == "review_pending" || taskStatus == "in_progress" || taskStatus == "completed" || taskStatus == "skipped" {
		return fmt.Errorf("dependencies can only change before a task starts")
	}
	seen := map[int64]bool{}
	for i := range deps {
		if seen[deps[i].TaskID] {
			return fmt.Errorf("duplicate dependency #%d", deps[i].TaskID)
		}
		seen[deps[i].TaskID] = true
		if deps[i].Condition == "" {
			deps[i].Condition = "success"
		}
		switch deps[i].Condition {
		case "success", "failure", "termination":
		default:
			return fmt.Errorf("dependency condition must be success, failure, or termination")
		}
		if err := validNames(deps[i].RequiredArtifacts); err != nil {
			return err
		}
		if deps[i].BoardID == "" {
			deps[i].BoardID = project
		}
	}

	// Validate all blocked_by tasks exist
	for _, d := range deps {
		var exists int
		err := s.db.GetContext(ctx, &exists,
			"SELECT COUNT(*) FROM board_tasks WHERE id = ? AND board_id = ?", d.TaskID, d.BoardID)
		if err != nil || exists == 0 {
			return fmt.Errorf("blocker task #%d not found on board %s", d.TaskID, d.BoardID)
		}
		// When the producer has declared an output contract, reject a consumer
		// dependency that asks for a name the producer never promised to publish.
		// Older tasks with no declared outputs remain compatible and are checked
		// against their immutable completion artifacts at readiness time.
		if len(d.RequiredArtifacts) > 0 {
			upstream, err := loadWorkflow(ctx, s.db, d.TaskID)
			if err != nil {
				return err
			}
			if len(upstream.RequiredOutputs) > 0 {
				declared := make(map[string]bool, len(upstream.RequiredOutputs))
				for _, name := range upstream.RequiredOutputs {
					declared[name] = true
				}
				for _, name := range d.RequiredArtifacts {
					if !declared[name] {
						return fmt.Errorf("dependency #%d cannot require artifact %q: upstream declares outputs [%s]", d.TaskID, name, strings.Join(upstream.RequiredOutputs, ", "))
					}
				}
			}
		}
	}

	// Validate no circular dependencies
	if err := s.validateNoCycles(ctx, taskID, deps); err != nil {
		return err
	}

	// Validate depth limit
	if err := s.ValidateDependencyDepth(ctx, taskID, deps, maxDepth); err != nil {
		return err
	}

	// Replace dependencies and their conditions atomically.
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A claim may have raced the validation before this transaction.
	if err := tx.GetContext(ctx, &taskStatus, "SELECT status FROM board_tasks WHERE id = ? AND board_id = ?", taskID, project); err != nil {
		return err
	}
	if taskStatus == "review_pending" || taskStatus == "in_progress" || taskStatus == "completed" || taskStatus == "skipped" {
		return fmt.Errorf("dependencies can only change before a task starts")
	}
	if err := validateTaskCycles(ctx, tx, taskID, deps); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM task_dependency_rules WHERE task_id = ?", taskID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM task_dependencies WHERE task_id = ?", taskID); err != nil {
		return err
	}
	for _, d := range deps {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO task_dependencies (task_id, blocked_by_task_id, blocked_by_board_id) VALUES (?, ?, ?)",
			taskID, d.TaskID, d.BoardID); err != nil {
			return err
		}
		if err := saveDependencyRule(ctx, tx, taskID, d); err != nil {
			return err
		}
	}

	_, ready, err := dependencyInputs(ctx, tx, taskID)
	if err != nil {
		return err
	}
	status := "blocked"
	if ready {
		status = "pending"
	}
	if _, err := tx.ExecContext(ctx, "UPDATE board_tasks SET status = ? WHERE id = ? AND board_id = ? AND status IN ('pending', 'blocked')", status, taskID, project); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) validateNoCycles(ctx context.Context, taskID int64, deps []TaskDep) error {
	return validateTaskCycles(ctx, s.db, taskID, deps)
}

func validateTaskCycles(ctx context.Context, db sqlx.QueryerContext, taskID int64, deps []TaskDep) error {
	visited := map[int64]bool{taskID: true}
	var walk func(id int64) error
	walk = func(id int64) error {
		var upstreamDeps []struct {
			BlockedByTaskID int64 `db:"blocked_by_task_id"`
		}
		if err := sqlx.SelectContext(ctx, db, &upstreamDeps,
			"SELECT blocked_by_task_id FROM task_dependencies WHERE task_id = ?", id); err != nil {
			return err
		}
		for _, u := range upstreamDeps {
			if visited[u.BlockedByTaskID] {
				return fmt.Errorf("circular dependency detected: task #%d appears in its own dependency chain", taskID)
			}
			visited[u.BlockedByTaskID] = true
			if err := walk(u.BlockedByTaskID); err != nil {
				return err
			}
			delete(visited, u.BlockedByTaskID)
		}
		return nil
	}

	for _, d := range deps {
		if d.TaskID == taskID {
			return fmt.Errorf("task cannot depend on itself")
		}
		visited[d.TaskID] = true
		if err := walk(d.TaskID); err != nil {
			return err
		}
		delete(visited, d.TaskID)
	}
	return nil
}

// ResolveDownstreamTasks consumes readiness notifications for completedTaskID.
// Readiness itself is committed atomically with completion or cancellation.
// A zero ID drains recovered notifications from all boards after startup.
func (s *Store) ResolveDownstreamTasks(ctx context.Context, project string, completedTaskID int64) ([]Task, error) {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var rows []struct {
		ID      int64  `db:"id"`
		BoardID string `db:"board_id"`
		Status  string `db:"status"`
	}
	if err := tx.SelectContext(ctx, &rows, `SELECT b.id, b.board_id, b.status
	 FROM task_ready_notifications n JOIN board_tasks b ON b.id = n.task_id
	 WHERE ? = 0 OR EXISTS (SELECT 1 FROM task_dependencies d WHERE d.task_id = b.id AND d.blocked_by_task_id = ?)`, completedTaskID, completedTaskID); err != nil {
		return nil, err
	}
	for _, row := range rows {
		if _, err := tx.ExecContext(ctx, "DELETE FROM task_ready_notifications WHERE task_id = ?", row.ID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	var unblocked []Task
	for _, row := range rows {
		if row.Status != "pending" {
			continue
		}
		t, err := s.getTaskByID(ctx, row.BoardID, row.ID)
		if err != nil {
			return nil, err
		}
		if t.Status == "pending" {
			unblocked = append(unblocked, *t)
		}
	}
	return unblocked, nil
}

// ReblockDownstreamTasks checks all tasks that depend on regressedTaskID.
// If the task is pending and has an unresolved blocker, sets it back to blocked.
func (s *Store) ReblockDownstreamTasks(ctx context.Context, project string, regressedTaskID int64) ([]Task, error) {
	var downstreamIDs []int64
	if err := s.db.SelectContext(ctx, &downstreamIDs,
		"SELECT DISTINCT task_id FROM task_dependencies WHERE blocked_by_task_id = ?", regressedTaskID); err != nil {
		return nil, err
	}

	var reblocked []Task
	for _, tid := range downstreamIDs {
		// Check if this task has any unresolved blockers
		_, ready, err := dependencyInputs(ctx, s.db, tid)
		if err != nil {
			continue
		}
		if ready {
			continue
		}

		// Has unresolved blockers — set back to blocked if currently pending. By id alone: a
		// downstream task may be on another board, and tid comes from the dependency table.
		result, err := s.db.ExecContext(ctx,
			"UPDATE board_tasks SET status = 'blocked' WHERE id = ? AND status = 'pending'", tid)
		if err != nil {
			continue
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			continue
		}

		var boardID string
		if err := s.db.GetContext(ctx, &boardID,
			"SELECT board_id FROM board_tasks WHERE id = ?", tid); err != nil {
			continue
		}
		if t, err := s.getTaskByID(ctx, boardID, tid); err == nil {
			reblocked = append(reblocked, *t)
		}
	}
	return reblocked, nil
}

// ValidateDependencyDepth walks the dependency graph and returns an error
// if adding the proposed deps would exceed maxDepth.
func (s *Store) ValidateDependencyDepth(ctx context.Context, taskID int64, deps []TaskDep, maxDepth int) error {
	if maxDepth <= 0 {
		return nil
	}

	// Compute the max upstream depth from each proposed blocker
	var maxUpstream func(id int64, depth int, visited map[int64]bool) (int, error)
	maxUpstream = func(id int64, depth int, visited map[int64]bool) (int, error) {
		if visited[id] {
			return depth, nil
		}
		visited[id] = true

		var upstreamIDs []int64
		if err := s.reader().SelectContext(ctx, &upstreamIDs,
			"SELECT blocked_by_task_id FROM task_dependencies WHERE task_id = ?", id); err != nil {
			return depth, err
		}
		best := depth
		for _, uid := range upstreamIDs {
			d, err := maxUpstream(uid, depth+1, visited)
			if err != nil {
				return 0, err
			}
			if d > best {
				best = d
			}
		}
		return best, nil
	}

	for _, d := range deps {
		visited := map[int64]bool{taskID: true}
		depth, err := maxUpstream(d.TaskID, 1, visited)
		if err != nil {
			return err
		}
		if depth > maxDepth {
			return fmt.Errorf("dependency chain would exceed maximum depth of %d", maxDepth)
		}
	}
	return nil
}

// TaskLiveCost holds real-time cost data for an in-progress task.
type TaskLiveCost struct {
	TaskID           int64   `json:"task_id"`
	SessionID        string  `json:"session_id"`
	CostUSD          float64 `json:"cost_usd"`
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	RequestCount     int     `json:"request_count"`
}

// ErrTaskNotFound: no task with that id on that board (or no such board).
var ErrTaskNotFound = errors.New("task not found")

// GetTaskLiveCost computes the current cost for a task by querying
// token_usage from claimed_at to now. Returns nil if the task has no session_id
// or the sessions DB is not available, and ErrTaskNotFound for a missing task.
func (s *Store) GetTaskLiveCost(ctx context.Context, project string, taskID int64) (*TaskLiveCost, error) {
	if s.sessionsDB == nil {
		return nil, nil
	}

	var task struct {
		SessionID *string `db:"session_id"`
		ClaimedAt *string `db:"claimed_at"`
	}
	if err := s.reader().GetContext(ctx, &task,
		`SELECT session_id, claimed_at FROM board_tasks WHERE id = ? AND board_id = ?`,
		taskID, project); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTaskNotFound
		}
		return nil, fmt.Errorf("task #%d: %w", taskID, err)
	}
	if task.SessionID == nil || task.ClaimedAt == nil {
		return nil, nil
	}

	var costs struct {
		CostUSD          float64 `db:"cost_usd"`
		InputTokens      int     `db:"input_tokens"`
		OutputTokens     int     `db:"output_tokens"`
		CacheReadTokens  int     `db:"cache_read_tokens"`
		CacheWriteTokens int     `db:"cache_write_tokens"`
		RequestCount     int     `db:"request_count"`
	}
	err := s.sessionsDB.GetContext(ctx, &costs,
		`SELECT COALESCE(SUM(cost_usd), 0) AS cost_usd,
		        COALESCE(SUM(input_tokens), 0) AS input_tokens,
		        COALESCE(SUM(output_tokens), 0) AS output_tokens,
		        COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
		        COALESCE(SUM(cache_write_tokens), 0) AS cache_write_tokens,
		        COALESCE(SUM(n_rows), 0) AS request_count
		 FROM token_usage_all
		 WHERE session_id = ? AND recorded_at >= ?`,
		*task.SessionID, *task.ClaimedAt)
	if err != nil {
		return nil, err
	}

	return &TaskLiveCost{
		TaskID:           taskID,
		SessionID:        *task.SessionID,
		CostUSD:          costs.CostUSD,
		InputTokens:      costs.InputTokens,
		OutputTokens:     costs.OutputTokens,
		CacheReadTokens:  costs.CacheReadTokens,
		CacheWriteTokens: costs.CacheWriteTokens,
		RequestCount:     costs.RequestCount,
	}, nil
}

// CoralSender is the system sender of Coral's notices to one agent (#3636: "Coral restarted").
// Such a notice @-names its agent; a subscriber reading everything counts it only when it is the
// one named.
const CoralSender = "Coral"

// likeMentionCandidates is the query ReadMessages used before the mention index (#4465), and still uses for a
// subscriber whose terms the index cannot answer: the messages after a cursor, from others, that match any of
// the LIKE patterns, the first fetchLimit of them, oldest first. The index's results are tested against it.
func (s *Store) likeMentionCandidates(ctx context.Context, project, subscriberID string, lastReadID int64, patterns []string, fetchLimit int) ([]Message, error) {
	whereClauses := make([]string, len(patterns))
	args := []interface{}{project, lastReadID, subscriberID}
	for i, p := range patterns {
		whereClauses[i] = "m.content LIKE ? COLLATE NOCASE"
		args = append(args, p)
	}
	args = append(args, fetchLimit)
	query := fmt.Sprintf(
		`SELECT m.id, m.project, m.subscriber_id, m.session_id, m.content, m.target_group_id, m.external_json, m.origin, m.hub_user, m.is_system, m.created_at,
		        COALESCE(s.job_title, m.subscriber_id, 'Unknown') as job_title
		 FROM board_messages m
		 LEFT JOIN board_subscribers s ON m.project = s.project AND m.subscriber_id = s.subscriber_id
		 WHERE m.project = ? AND m.id > ? AND m.subscriber_id != ? AND (%s)
		 ORDER BY m.id ASC LIMIT ?`,
		strings.Join(whereClauses, " OR "))
	var candidates []Message
	err := s.reader().SelectContext(ctx, &candidates, query, args...)
	return candidates, err
}
