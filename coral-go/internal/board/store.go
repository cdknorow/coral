// Package board provides the message board store and HTTP handlers.
// It uses a separate SQLite database from the main Coral store.
package board

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/cdknorow/coral/internal/dbcrypt"
	"github.com/cdknorow/coral/internal/naming"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

func encryptedKeyHex(key string) string {
	if strings.HasPrefix(key, "rawhex:") {
		return strings.TrimPrefix(key, "rawhex:")
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// Subscriber represents a board subscriber.
// SubscriberID is the stable identity (role/display name, e.g. "Orchestrator").
// SessionName is the current tmux/pty session name (mutable across restarts).
type Subscriber struct {
	ID           int64   `db:"id" json:"id"`
	Project      string  `db:"project" json:"project"`
	SubscriberID string  `db:"subscriber_id" json:"subscriber_id"`
	SessionName  string  `db:"session_name" json:"session_name,omitempty"`
	JobTitle     string  `db:"job_title" json:"job_title"`
	WebhookURL   *string `db:"webhook_url" json:"webhook_url"`
	OriginServer *string `db:"origin_server" json:"origin_server"`
	ReceiveMode  string  `db:"receive_mode" json:"receive_mode"`
	LastReadID   int64   `db:"last_read_id" json:"last_read_id"`
	SubscribedAt string  `db:"subscribed_at" json:"subscribed_at"`
	IsActive     int     `db:"is_active" json:"is_active"`
	CanPeek      int     `db:"can_peek" json:"can_peek"`
	// Legacy column — kept for DB compat, mirrors SubscriberID for new rows.
	SessionID string `db:"session_id" json:"-"`
}

// GroupInfo holds group summary info.
type GroupInfo struct {
	GroupID     string `db:"group_id" json:"group_id"`
	MemberCount int    `db:"member_count" json:"member_count"`
}

// Task represents a board task.
type Task struct {
	ID                int64        `db:"id" json:"id"`
	Revision          int          `db:"revision" json:"revision"`
	BoardID           string       `db:"board_id" json:"board_id"`
	Title             string       `db:"title" json:"title"`
	Body              *string      `db:"body" json:"body,omitempty"`
	Status            string       `db:"status" json:"status"`
	Priority          string       `db:"priority" json:"priority"`
	CreatedBy         string       `db:"created_by" json:"created_by"`
	AssignedTo        *string      `db:"assigned_to" json:"assigned_to"`
	CompletedBy       *string      `db:"completed_by" json:"completed_by"`
	CompletionMessage *string      `db:"completion_message" json:"completion_message,omitempty"`
	CreatedAt         string       `db:"created_at" json:"created_at"`
	ClaimedAt         *string      `db:"claimed_at" json:"claimed_at,omitempty"`
	CompletedAt       *string      `db:"completed_at" json:"completed_at,omitempty"`
	SessionID         *string      `db:"session_id" json:"session_id,omitempty"`
	CostUSD           *float64     `db:"cost_usd" json:"cost_usd,omitempty"`
	InputTokens       *int         `db:"input_tokens" json:"input_tokens,omitempty"`
	OutputTokens      *int         `db:"output_tokens" json:"output_tokens,omitempty"`
	CacheReadTokens   *int         `db:"cache_read_tokens" json:"cache_read_tokens,omitempty"`
	CacheWriteTokens  *int         `db:"cache_write_tokens" json:"cache_write_tokens,omitempty"`
	BlockedBy         []TaskDep    `db:"-" json:"blocked_by,omitempty"`
	Workflow          TaskWorkflow `db:"-" json:"workflow"`
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
	// Legacy column — kept for DB compat, mirrors SubscriberID for new rows.
	SessionID string `db:"session_id" json:"-"`
}

// ProjectInfo holds project summary info.
type ProjectInfo struct {
	Project         string `db:"project" json:"project"`
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
	sessionsDB              *sqlx.DB // optional reference to the main sessions DB for cross-DB queries
	completionCheckRunner   CompletionCheckRunner
	completionChecksEnabled bool
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

// NewStore creates a new board Store with its own database.
func NewStore(dbPath string) (*Store, error) {
	return NewStoreWithKey(dbPath, "")
}

// NewStoreWithKey opens the board database with SQLCipher when key is set.
func NewStoreWithKey(dbPath, key string) (*Store, error) {
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
	dsn := dbPath + "?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)&_pragma=cache_size(-8000)"
	if key != "" {
		driver = "sqlite3"
		q := url.Values{}
		q.Set("_key", encryptedKeyHex(key))
		q.Set("_pragma_busy_timeout", "30000")
		q.Set("_pragma_journal_mode", "WAL")
		q.Set("_pragma_synchronous", "NORMAL")
		q.Set("_pragma_temp_store", "MEMORY")
		q.Set("_pragma_cache_size", "-8000")
		dsn = dbPath + "?" + q.Encode()
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
	return s, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
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
	err := s.db.SelectContext(ctx, &out, `SELECT project,subscriber_id,message,interval_seconds FROM board_reminders`)
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
	}
	for _, ddl := range alterColumns {
		if _, err := s.db.ExecContext(ctx, ddl); err != nil {
			if !strings.Contains(err.Error(), "duplicate column") {
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

	// Migrate CHECK constraint to include 'blocked' status for existing databases.
	// SQLite doesn't support ALTER CONSTRAINT, so we recreate the table.
	if err := s.migrateTasksCheckConstraint(ctx); err != nil {
		return err
	}

	if err := s.ensureWaitsSchema(ctx); err != nil {
		return err
	}

	return s.initTaskWorkflows(ctx)
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
func (s *Store) Subscribe(ctx context.Context, project, subscriberID, jobTitle, sessionName string, webhookURL, originServer *string, receiveMode string, canPeek ...bool) (*Subscriber, error) {
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
		`INSERT INTO board_subscribers (project, session_id, subscriber_id, session_name, job_title, webhook_url, origin_server, receive_mode, last_read_id, subscribed_at, is_active, can_peek)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)
		 ON CONFLICT(project, session_id) DO UPDATE SET
		     job_title = excluded.job_title,
		     webhook_url = excluded.webhook_url,
		     origin_server = excluded.origin_server,
		     receive_mode = excluded.receive_mode,
		     session_name = excluded.session_name,
		     subscriber_id = excluded.subscriber_id,
		     is_active = 1,
		     can_peek = excluded.can_peek`,
		project, subscriberID, subscriberID, sessionName, jobTitle, webhookURL, originServer, receiveMode, carryForwardCursor, now, peekFlag)
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
	err := s.db.SelectContext(ctx, &subs,
		"SELECT * FROM board_subscribers WHERE project = ? AND is_active = 1 ORDER BY subscribed_at", project)
	return subs, err
}

// GetSubscription returns the active subscription for a subscriber.
func (s *Store) GetSubscription(ctx context.Context, subscriberID string) (*Subscriber, error) {
	var sub Subscriber
	err := s.db.GetContext(ctx, &sub,
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
	err := s.db.GetContext(ctx, &sub,
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
	err := s.db.GetContext(ctx, &sub,
		"SELECT * FROM board_subscribers WHERE session_name = ? AND is_active = 1 ORDER BY subscribed_at DESC LIMIT 1", sessionName)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &sub, err
}

// GetAllSubscriptions returns all active subscriptions keyed by session_name
// (the tmux session identifier) for compatibility with live session lookups.
func (s *Store) GetAllSubscriptions(ctx context.Context) (map[string]*Subscriber, error) {
	var subs []Subscriber
	err := s.db.SelectContext(ctx, &subs, "SELECT * FROM board_subscribers WHERE is_active = 1")
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
	now := nowUTC()
	result, err := s.db.ExecContext(ctx,
		"INSERT INTO board_messages (project, session_id, subscriber_id, content, target_group_id, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		project, subscriberID, subscriberID, content, targetGroupID, now)
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()
	msg := &Message{ID: id, Project: project, SubscriberID: subscriberID, Content: content, CreatedAt: now}
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
	err := s.db.GetContext(ctx, &sub,
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

		whereClauses := make([]string, len(patterns))
		args := []interface{}{project, sub.LastReadID, subscriberID}
		for i, p := range patterns {
			whereClauses[i] = "m.content LIKE ? COLLATE NOCASE"
			args = append(args, p)
		}
		fetchLimit := limit * 2
		if fetchLimit < 50 {
			fetchLimit = 50
		}
		args = append(args, fetchLimit)

		query := fmt.Sprintf(
			`SELECT m.id, m.project, m.subscriber_id, m.session_id, m.content, m.target_group_id, m.created_at,
			        COALESCE(s.job_title, m.subscriber_id, 'Unknown') as job_title
			 FROM board_messages m
			 LEFT JOIN board_subscribers s ON m.project = s.project AND m.subscriber_id = s.subscriber_id
			 WHERE m.project = ? AND m.id > ? AND m.subscriber_id != ? AND (%s)
			 ORDER BY m.id ASC LIMIT ?`,
			strings.Join(whereClauses, " OR "))
		var candidates []Message
		err = s.db.SelectContext(ctx, &candidates, query, args...)
		if err != nil {
			return nil, err
		}

		for _, msg := range candidates {
			if containsExplicitTag(msg.Content, subscriberID, sub.JobTitle) {
				messages = append(messages, msg)
				if len(messages) >= limit {
					break
				}
			}
		}
	} else {
		err = s.db.SelectContext(ctx, &messages,
			`SELECT m.id, m.project, m.subscriber_id, m.session_id, m.content, m.target_group_id, m.created_at,
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
	s.db.GetContext(ctx, &ownMax,
		"SELECT COALESCE(MAX(id), 0) FROM board_messages WHERE project = ? AND subscriber_id = ?",
		project, subscriberID)
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
	if err = s.db.GetContext(ctx, &lastReadID,
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
	if err = s.db.GetContext(ctx, &r,
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
		err = s.db.SelectContext(ctx, &messages,
			`SELECT m.id, m.project, m.subscriber_id, m.session_id, m.content, m.created_at,
			        COALESCE(s.job_title, m.subscriber_id, 'Unknown') as job_title,
			        m.target_group_id
			 FROM board_messages m
			 LEFT JOIN board_subscribers s ON m.project = s.project AND m.subscriber_id = s.subscriber_id
			 WHERE m.project = ? AND m.id < ?
			 ORDER BY m.id ASC LIMIT ? OFFSET ?`,
			project, beforeID, limit, offset)
	} else if offset > 0 {
		// Paginated load: use ASC order with offset for consistent pagination
		err = s.db.SelectContext(ctx, &messages,
			`SELECT m.id, m.project, m.subscriber_id, m.session_id, m.content, m.created_at,
			        COALESCE(s.job_title, m.subscriber_id, 'Unknown') as job_title,
			        m.target_group_id
			 FROM board_messages m
			 LEFT JOIN board_subscribers s ON m.project = s.project AND m.subscriber_id = s.subscriber_id
			 WHERE m.project = ?
			 ORDER BY m.id ASC LIMIT ? OFFSET ?`,
			project, limit, offset)
	} else {
		// Initial load (offset=0): get most recent messages
		err = s.db.SelectContext(ctx, &messages,
			`SELECT * FROM (
			    SELECT m.id, m.project, m.subscriber_id, m.session_id, m.content, m.created_at,
			           COALESCE(s.job_title, m.subscriber_id, 'Unknown') as job_title,
			           m.target_group_id
			    FROM board_messages m
			    LEFT JOIN board_subscribers s ON m.project = s.project AND m.subscriber_id = s.subscriber_id
			    WHERE m.project = ?
			    ORDER BY m.id DESC LIMIT ?
			 ) sub ORDER BY id ASC`,
			project, limit)
	}
	return messages, err
}

// GetMessageByID returns a single message by its ID.
func (s *Store) GetMessageByID(ctx context.Context, id int64) (*Message, error) {
	var msg Message
	err := s.db.GetContext(ctx, &msg,
		`SELECT m.id, m.project, m.subscriber_id, m.session_id, m.content, m.created_at,
		        COALESCE(s.job_title, m.subscriber_id, 'Unknown') as job_title,
		        m.target_group_id
		 FROM board_messages m
		 LEFT JOIN board_subscribers s ON m.project = s.project AND m.subscriber_id = s.subscriber_id
		 WHERE m.id = ?`, id)
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
	err := s.db.SelectContext(ctx, &messages,
		`SELECT m.id, m.project, m.subscriber_id, m.session_id, m.content, m.created_at,
		        COALESCE(bs.job_title, m.subscriber_id, 'Unknown') as job_title,
		        m.target_group_id
		 FROM board_messages m
		 LEFT JOIN board_subscribers bs ON m.project = bs.project AND m.subscriber_id = bs.subscriber_id
		 WHERE lower(m.content) LIKE lower(?)
		 ORDER BY m.id DESC LIMIT ?`, "%"+query+"%", limit)
	return messages, err
}

// CountMessages returns the total message count for a project.
func (s *Store) CountMessages(ctx context.Context, project string) (int, error) {
	var count int
	err := s.db.GetContext(ctx, &count,
		"SELECT COUNT(*) FROM board_messages WHERE project = ?", project)
	return count, err
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

func isAlphaNum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

// containsExplicitTag returns true if content explicitly mentions or tags subscriberID/jobTitle,
// or includes broadcast tags like @all, @notify-all.
func containsExplicitTag(content, subscriberID, jobTitle string) bool {
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
			}
			return true
		}
	}
	return false
}

// isOrchestrator returns true if the subscriber identity or job title indicates an orchestrator,
// or if canPeek is enabled.
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
	err := s.db.GetContext(ctx, &sub,
		"SELECT last_read_id, job_title, receive_mode, can_peek FROM board_subscribers WHERE project = ? AND subscriber_id = ?",
		project, subscriberID)
	if err != nil {
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
		var count int
		err := s.db.GetContext(ctx, &count,
			`SELECT COUNT(*) FROM board_messages WHERE project = ? AND id > ? AND subscriber_id != ?`+systemFilter,
			project, sub.LastReadID, subscriberID)
		return count, err
	}

	if receiveMode == "mentions" {
		terms := mentionTerms(subscriberID, sub.JobTitle)
		// Convert terms to SQL LIKE patterns (% wildcard matches any substring)
		patterns := make([]string, len(terms))
		for i, t := range terms {
			patterns[i] = "%" + t + "%"
		}

		whereClauses := make([]string, len(patterns))
		args := []interface{}{project, sub.LastReadID, subscriberID}
		for i, p := range patterns {
			whereClauses[i] = "content LIKE ? COLLATE NOCASE"
			args = append(args, p)
		}

		var count int
		query := fmt.Sprintf(
			`SELECT COUNT(*) FROM board_messages
			 WHERE project = ? AND id > ? AND subscriber_id != ?`+systemFilter+` AND (%s)`,
			strings.Join(whereClauses, " OR "))
		err = s.db.GetContext(ctx, &count, query, args...)
		return count, err
	}

	// Group-based mode: count messages from group members only
	var memberIDs []string
	err = s.db.SelectContext(ctx, &memberIDs,
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
	err = s.db.GetContext(ctx, &count, query, args...)
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
	if err := s.db.GetContext(ctx, &sub, "SELECT last_read_id, job_title, receive_mode, can_peek FROM board_subscribers WHERE project=? AND subscriber_id=?", project, subscriberID); err != nil {
		return 0, nil
	}
	if sub.ReceiveMode == "none" {
		return 0, nil
	}
	args := []interface{}{project, sub.LastReadID, subscriberID}
	where := "m.project=? AND m.id>? AND m.subscriber_id!=? AND m.subscriber_id!='Coral Task Queue'"
	if sub.ReceiveMode != "all" && !isOrchestrator(subscriberID, sub.JobTitle, sub.CanPeek) {
		terms := mentionTerms(subscriberID, sub.JobTitle)
		parts := make([]string, 0, len(terms))
		for _, term := range terms {
			parts = append(parts, "m.content LIKE ? COLLATE NOCASE")
			args = append(args, "%"+term+"%")
		}
		if len(parts) == 0 {
			return 0, nil
		}
		where += " AND (" + strings.Join(parts, " OR ") + ")"
	}
	var latest int64
	err := s.db.GetContext(ctx, &latest, "SELECT COALESCE(MAX(m.id),0) FROM board_messages m WHERE "+where, args...)
	return latest, err
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
	if err := s.db.GetContext(ctx, &sub, "SELECT last_read_id, job_title, receive_mode, can_peek FROM board_subscribers WHERE project=? AND subscriber_id=?", project, subscriberID); err != nil {
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
		terms := mentionTerms(subscriberID, sub.JobTitle)
		if len(terms) == 0 {
			return false, nil
		}
		parts := make([]string, len(terms))
		for i, term := range terms {
			parts[i] = "m.content LIKE ? COLLATE NOCASE"
			args = append(args, "%"+term+"%")
		}
		where += " AND (" + strings.Join(parts, " OR ") + ")"
	}
	var found int
	err := s.db.GetContext(ctx, &found, "SELECT 1 FROM board_messages m WHERE "+where+" LIMIT 1", args...)
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
	if err := s.db.SelectContext(ctx, &subs, s.db.Rebind(query), args...); err != nil {
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
	if err := s.db.SelectContext(ctx, &groupRows, s.db.Rebind(query), args...); err != nil {
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

	// Group subscribers by project
	type subInfo struct {
		SubscriberID string
		SessionName  string
		JobTitle     string
		LastReadID   int64
		ReceiveMode  string
	}
	result := make(map[string]int)
	byProject := make(map[string][]subInfo)
	for _, sub := range subs {
		result[sub.SessionName] = 0
		rm := sub.ReceiveMode
		if rm == "none" {
			continue
		}
		if rm == "" {
			rm = "mentions"
		}
		byProject[sub.Project] = append(byProject[sub.Project], subInfo{
			sub.SubscriberID, sub.SessionName, sub.JobTitle, sub.LastReadID, rm,
		})
	}

	for project, projectSubs := range byProject {
		minCursor := projectSubs[0].LastReadID
		for _, sub := range projectSubs {
			if sub.LastReadID < minCursor {
				minCursor = sub.LastReadID
			}
		}

		// Stream messages instead of retaining every unread body in memory.
		rows, err := s.db.QueryxContext(ctx,
			"SELECT id, subscriber_id, content FROM board_messages WHERE project = ? AND id > ? AND subscriber_id != 'Coral Task Queue' ORDER BY id",
			project, minCursor)
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
			for _, sub := range projectSubs {
				if id <= sub.LastReadID || sender == sub.SubscriberID {
					continue
				}
				eligible := false
				switch sub.ReceiveMode {
				case "all":
					eligible = true
				case "mentions":
					eligible = containsExplicitTag(content, sub.SubscriberID, sub.JobTitle)
				default:
					eligible = groupsByKey[groupKey{project, sub.ReceiveMode}][sender]
				}
				if eligible {
					result[sub.SessionName]++
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
	return result, nil
}

// GetAllUnreadStates returns unread counts together with the latest eligible
// message ID for each active session. The notifier uses this pair when seeding
// after startup so a new same-count batch is not mistaken for the seeded one.
func (s *Store) GetAllUnreadStates(ctx context.Context) (map[string]UnreadState, error) {
	counts, err := s.GetAllUnreadCounts(ctx)
	if err != nil {
		return nil, err
	}
	states := make(map[string]UnreadState, len(counts))
	var subs []struct {
		Project      string `db:"project"`
		SubscriberID string `db:"subscriber_id"`
		SessionName  string `db:"session_name"`
	}
	if err := s.db.SelectContext(ctx, &subs,
		"SELECT project, subscriber_id, session_name FROM board_subscribers WHERE is_active = 1"); err != nil {
		return nil, err
	}
	for _, sub := range subs {
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
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO board_groups (project, group_id, session_id, subscriber_id) VALUES (?, ?, ?, ?)
		 ON CONFLICT(project, group_id, session_id) DO NOTHING`,
		project, groupID, subscriberID, subscriberID)
	return err
}

// RemoveFromGroup removes a subscriber from a board group. Returns true if removed.
func (s *Store) RemoveFromGroup(ctx context.Context, project, groupID, subscriberID string) (bool, error) {
	result, err := s.db.ExecContext(ctx,
		"DELETE FROM board_groups WHERE project = ? AND group_id = ? AND subscriber_id = ?",
		project, groupID, subscriberID)
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	return n > 0, nil
}

// ListGroupMembers returns subscriber_ids in a group.
func (s *Store) ListGroupMembers(ctx context.Context, project, groupID string) ([]string, error) {
	var members []string
	err := s.db.SelectContext(ctx, &members,
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
	err := s.db.SelectContext(ctx, &groups,
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
func (s *Store) DeleteMessage(ctx context.Context, messageID int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, "DELETE FROM board_messages WHERE id = ?", messageID)
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	return n > 0, nil
}

// GetWebhookTargets returns subscribers with webhook URLs (excluding sender).
func (s *Store) GetWebhookTargets(ctx context.Context, project, excludeSubscriberID string) ([]Subscriber, error) {
	var subs []Subscriber
	err := s.db.SelectContext(ctx, &subs,
		`SELECT * FROM board_subscribers
		 WHERE project = ? AND subscriber_id != ? AND webhook_url IS NOT NULL AND webhook_url != '' AND is_active = 1`,
		project, excludeSubscriberID)
	return subs, err
}

// ── Projects ─────────────────────────────────────────────────────────

// ListProjects returns all known projects with subscriber and message counts.
func (s *Store) ListProjects(ctx context.Context) ([]ProjectInfo, error) {
	var projects []ProjectInfo
	err := s.db.SelectContext(ctx, &projects,
		`SELECT project,
		        (SELECT COUNT(*) FROM board_subscribers s WHERE s.project = p.project AND s.is_active = 1) as subscriber_count,
		        (SELECT COUNT(*) FROM board_messages m WHERE m.project = p.project) as message_count
		 FROM (
		     SELECT DISTINCT project FROM board_subscribers WHERE is_active = 1
		     UNION
		     SELECT DISTINCT project FROM board_messages
		 ) p ORDER BY project`)
	return projects, err
}

// EnrichedProject holds project info with timestamps and participant names.
type EnrichedProject struct {
	Project          string  `db:"project" json:"project"`
	SubscriberCount  int     `db:"subscriber_count" json:"subscriber_count"`
	MessageCount     int     `db:"message_count" json:"message_count"`
	FirstMessageAt   *string `db:"first_message_at" json:"first_message_at"`
	LastMessageAt    *string `db:"last_message_at" json:"last_message_at"`
	ParticipantNames *string `db:"participant_names" json:"participant_names"`
}

// ListProjectsEnriched returns board projects with timestamps, subscriber info, and participant names.
func (s *Store) ListProjectsEnriched(ctx context.Context) ([]EnrichedProject, error) {
	var projects []EnrichedProject
	err := s.db.SelectContext(ctx, &projects,
		`SELECT
		     p.project,
		     (SELECT COUNT(*) FROM board_subscribers s WHERE s.project = p.project AND s.is_active = 1) as subscriber_count,
		     (SELECT COUNT(*) FROM board_messages m WHERE m.project = p.project) as message_count,
		     (SELECT MIN(created_at) FROM board_messages m WHERE m.project = p.project) as first_message_at,
		     (SELECT MAX(created_at) FROM board_messages m WHERE m.project = p.project) as last_message_at,
		     (SELECT GROUP_CONCAT(s.job_title, ', ')
		      FROM board_subscribers s WHERE s.project = p.project AND s.is_active = 1
		      ORDER BY s.subscribed_at LIMIT 5) as participant_names
		 FROM (
		     SELECT DISTINCT project FROM board_subscribers WHERE is_active = 1
		     UNION
		     SELECT DISTINCT project FROM board_messages
		 ) p
		 ORDER BY (SELECT MAX(created_at) FROM board_messages m WHERE m.project = p.project) DESC`)
	return projects, err
}

// SearchMessages returns project names that have messages matching the query (LIKE search).
func (s *Store) SearchMessages(ctx context.Context, query string) ([]string, error) {
	var projects []string
	err := s.db.SelectContext(ctx, &projects,
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
	return tx.Commit()
}

// ── Tasks ───────────────────────────────────────────────────────────

// getTaskByID fetches a task by ID and board_id.
func (s *Store) getTaskByID(ctx context.Context, project string, taskID int64) (*Task, error) {
	var t Task
	err := s.db.GetContext(ctx, &t,
		"SELECT id, revision, board_id, title, body, status, priority, created_by, assigned_to, completed_by, completion_message, created_at, claimed_at, completed_at, session_id, cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens FROM board_tasks WHERE id = ? AND board_id = ?", taskID, project)
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
			s.db.ExecContext(ctx, "DELETE FROM board_tasks WHERE id = ?", taskID)
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
	err := s.db.SelectContext(ctx, &tasks,
		`SELECT id, revision, board_id, title, body, status, priority, created_by, assigned_to, completed_by, completion_message, created_at, claimed_at, completed_at, session_id, cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens FROM board_tasks WHERE board_id = ?
		 ORDER BY CASE priority WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 END, id ASC`,
		project)
	if err != nil {
		return nil, err
	}
	s.populateTaskDeps(ctx, tasks)
	return tasks, nil
}

// ListAllTasks returns the most recent tasks across all boards.
func (s *Store) ListAllTasks(ctx context.Context, limit int) ([]Task, error) {
	if limit <= 0 {
		limit = 100
	}
	var tasks []Task
	err := s.db.SelectContext(ctx, &tasks,
		`SELECT id, revision, board_id, title, body, status, priority, created_by, assigned_to, completed_by, completion_message, created_at, claimed_at, completed_at, session_id, cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens FROM board_tasks
		 ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	s.populateTaskDeps(ctx, tasks)
	return tasks, nil
}

// populateTaskDeps batch-loads dependencies for a slice of tasks.
func (s *Store) populateTaskDeps(ctx context.Context, tasks []Task) {
	for i := range tasks {
		s.hydrateWorkflow(ctx, &tasks[i])
	}
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
	if err := s.db.SelectContext(ctx, &rows, query, args...); err != nil {
		return
	}
	for _, r := range rows {
		t := idMap[r.TaskID]
		if t == nil {
			continue
		}
		dep := TaskDep{TaskID: r.BlockedByTaskID, BoardID: r.BlockedByBoardID}
		s.loadDependencyRule(ctx, t.ID, &dep)
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
	err := s.db.GetContext(ctx, &count,
		`SELECT COUNT(1) FROM board_tasks
		 WHERE board_id = ? AND assigned_to = ? AND status = 'in_progress' AND id != ?`,
		project, assignee, excludeTaskID)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// FindIdleSubscriber returns a random active subscriber with no in-progress tasks, or nil.
func (s *Store) FindIdleSubscriber(ctx context.Context, project string) *Subscriber {
	var sub Subscriber
	err := s.db.GetContext(ctx, &sub,
		`SELECT * FROM board_subscribers
		 WHERE project = ? AND is_active = 1 AND session_name != ''
		 AND subscriber_id NOT IN (
		     SELECT assigned_to FROM board_tasks WHERE board_id = ? AND status = 'in_progress' AND assigned_to IS NOT NULL
		 )
		 ORDER BY RANDOM() LIMIT 1`, project, project)
	if err != nil {
		return nil
	}
	return &sub
}

// ActiveTaskForSubscriber returns the subscriber's current in-progress task, or nil.
func (s *Store) ActiveTaskForSubscriber(ctx context.Context, project, subscriberID string) *Task {
	var task Task
	err := s.db.GetContext(ctx, &task,
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
			if _, err := tx.ExecContext(ctx, "UPDATE board_tasks SET status = 'blocked' WHERE id = ? AND status = 'pending'", taskID); err != nil {
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
					s.db.ExecContext(ctx, `UPDATE board_tasks SET session_id = ? WHERE id = ?`, sessionUUID, taskID)
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
	err := s.db.SelectContext(ctx, &tasks, `SELECT id, revision, board_id, title, body, status, priority,
		created_by, assigned_to, completed_by, completion_message, created_at, claimed_at,
		completed_at, session_id, cost_usd, input_tokens, output_tokens, cache_read_tokens,
		cache_write_tokens FROM board_tasks
		WHERE board_id = ? AND status = 'in_progress' AND COALESCE(last_activity_at, claimed_at) IS NOT NULL
		AND COALESCE(last_activity_at, claimed_at) < ?
		AND (idle_snoozed_until IS NULL OR idle_snoozed_until <= ?)`, project, before.UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339))
	return tasks, err
}

// computeAndStoreTaskCost queries the sessions DB for proxy request costs
// incurred during the task's lifetime and stores them on the task.
func (s *Store) computeAndStoreTaskCost(ctx context.Context, taskID int64) {
	if s.sessionsDB == nil {
		return
	}
	// Fetch the task's session_id and time window.
	var task struct {
		SessionID   *string `db:"session_id"`
		ClaimedAt   *string `db:"claimed_at"`
		CompletedAt *string `db:"completed_at"`
	}
	if err := s.db.GetContext(ctx, &task,
		`SELECT session_id, claimed_at, completed_at FROM board_tasks WHERE id = ?`, taskID); err != nil {
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
		 FROM token_usage
		 WHERE session_id = ? AND recorded_at >= ? AND recorded_at <= ?`,
		*task.SessionID, *task.ClaimedAt, *task.CompletedAt)
	if err != nil {
		return
	}

	s.db.ExecContext(ctx,
		`UPDATE board_tasks
		 SET cost_usd = ?, input_tokens = ?, output_tokens = ?,
		     cache_read_tokens = ?, cache_write_tokens = ?
		 WHERE id = ?`,
		costs.CostUSD, costs.InputTokens, costs.OutputTokens,
		costs.CacheReadTokens, costs.CacheWriteTokens, taskID)
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

	s.computeAndStoreTaskCost(ctx, taskID)

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
	err := s.db.SelectContext(ctx, &deps,
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
	if err := s.db.SelectContext(ctx, &rows, `SELECT DISTINCT b.id, b.board_id
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

		// Has unresolved blockers — set back to blocked if currently pending
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
		if err := s.db.SelectContext(ctx, &upstreamIDs,
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

// GetTaskLiveCost computes the current cost for a task by querying
// token_usage from claimed_at to now. Returns nil if the task has no session_id
// or the sessions DB is not available.
func (s *Store) GetTaskLiveCost(ctx context.Context, project string, taskID int64) (*TaskLiveCost, error) {
	if s.sessionsDB == nil {
		return nil, nil
	}

	var task struct {
		SessionID *string `db:"session_id"`
		ClaimedAt *string `db:"claimed_at"`
	}
	if err := s.db.GetContext(ctx, &task,
		`SELECT session_id, claimed_at FROM board_tasks WHERE id = ? AND board_id = ?`,
		taskID, project); err != nil {
		return nil, fmt.Errorf("task #%d not found", taskID)
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
		        COUNT(*) AS request_count
		 FROM token_usage
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
