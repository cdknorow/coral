// The board database's schema and its migrations (moved out of store.go, #4998; no behaviour change).

package board

import (
	"context"
	"fmt"
	"log"
	"strings"
)

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
			edited_at TEXT,
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
		"ALTER TABLE board_tasks ADD COLUMN edited_at TEXT",
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
		"ALTER TABLE board_messages ADD COLUMN task_id INTEGER", // the task a reply answers (#5463): null for every message that is not a reply to a task
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
		// The sender of a board's messages by id (#5354): "has the health monitor posted since I read?" is a seek, not a scan of the unread tail.
		// subscriber_id is a migrated column, so this index follows its ALTER too.
		"CREATE INDEX IF NOT EXISTS idx_board_messages_sender ON board_messages(project, subscriber_id, id)",
		// Only the replies to a task are in it, so building it touches no old row (#5463).
		"CREATE INDEX IF NOT EXISTS idx_board_messages_task ON board_messages(project, task_id, id) WHERE task_id IS NOT NULL",
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
	if err := s.migrateHealthDefaultOff(ctx); err != nil {
		return err
	}
	if err := s.ensureHubOperatorActionsSchema(ctx); err != nil {
		return err
	}
	if err := s.ensureSwitchLogSchema(ctx); err != nil {
		return err
	}
	if err := s.ensureTeamLinksSchema(ctx); err != nil {
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
	if err := s.ensureCrossTeamIDSchema(ctx); err != nil {
		return err
	}
	if err := s.ensureGoalsSchema(ctx); err != nil {
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
