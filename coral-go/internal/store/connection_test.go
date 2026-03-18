package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpen_CreatesDatabase(t *testing.T) {
	db := openTestDB(t)
	assert.NotNil(t, db)
}

func TestOpen_SetsWALMode(t *testing.T) {
	db := openTestDB(t)
	var mode string
	err := db.GetContext(context.Background(), &mode, "PRAGMA journal_mode")
	require.NoError(t, err)
	assert.Equal(t, "wal", mode)
}

func TestOpen_SetsForeignKeys(t *testing.T) {
	db := openTestDB(t)
	var fk int
	err := db.GetContext(context.Background(), &fk, "PRAGMA foreign_keys")
	require.NoError(t, err)
	assert.Equal(t, 1, fk)
}

func TestSchema_AllTablesExist(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	expectedTables := []string{
		"session_meta",
		"tags",
		"session_tags",
		"folder_tags",
		"session_index",
		"session_fts",
		"summarizer_queue",
		"git_snapshots",
		"agent_tasks",
		"agent_notes",
		"agent_events",
		"live_sessions",
		"user_settings",
		"scheduled_jobs",
		"scheduled_runs",
		"webhook_configs",
		"webhook_deliveries",
		"git_changed_files",
		"agent_live_state",
	}

	for _, table := range expectedTables {
		var count int
		err := db.GetContext(ctx, &count,
			"SELECT COUNT(*) FROM sqlite_master WHERE type IN ('table', 'view') AND name = ?", table)
		require.NoError(t, err, "query for table %s", table)
		assert.Equal(t, 1, count, "table %s should exist", table)
	}
}

func TestSchema_FTS5Table(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Verify session_fts is a virtual table (FTS5)
	var sql string
	err := db.GetContext(ctx, &sql,
		"SELECT sql FROM sqlite_master WHERE name = 'session_fts'")
	require.NoError(t, err)
	assert.Contains(t, sql, "fts5")
	assert.Contains(t, sql, "porter")
}

func TestSchema_MigrationColumns(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Verify migration columns exist by inserting a row that uses them
	_, err := db.ExecContext(ctx, `
		INSERT INTO live_sessions (session_id, agent_type, agent_name, working_dir, created_at,
			resume_from_id, flags, is_job, prompt, board_name, board_server)
		VALUES ('test-id', 'claude', 'test', '/tmp', '2026-01-01',
			'resume-id', 'flag1', 0, 'do stuff', 'my-board', 'http://remote')
	`)
	require.NoError(t, err)

	// Verify scheduled_runs migration columns
	_, err = db.ExecContext(ctx, `
		INSERT INTO scheduled_jobs (id, name, cron_expr, repo_path, prompt, created_at, updated_at)
		VALUES (1, 'test-job', '* * * * *', '/tmp', 'test', '2026-01-01', '2026-01-01')
	`)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `
		INSERT INTO scheduled_runs (job_id, status, scheduled_at, created_at, trigger_type, webhook_url, display_name)
		VALUES (1, 'pending', '2026-01-01', '2026-01-01', 'api', 'http://hook', 'My Run')
	`)
	require.NoError(t, err)
}

func TestSchema_UniqueConstraints(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// git_snapshots: UNIQUE(session_id, commit_hash)
	_, err := db.ExecContext(ctx, `
		INSERT INTO git_snapshots (agent_name, agent_type, working_directory, branch, commit_hash, session_id, recorded_at)
		VALUES ('agent1', 'claude', '/tmp', 'main', 'abc123', 'sess-1', '2026-01-01')
	`)
	require.NoError(t, err)

	// Duplicate should fail
	_, err = db.ExecContext(ctx, `
		INSERT INTO git_snapshots (agent_name, agent_type, working_directory, branch, commit_hash, session_id, recorded_at)
		VALUES ('agent1', 'claude', '/tmp', 'main', 'abc123', 'sess-1', '2026-01-02')
	`)
	assert.Error(t, err, "duplicate (session_id, commit_hash) should fail")

	// Different commit_hash should succeed
	_, err = db.ExecContext(ctx, `
		INSERT INTO git_snapshots (agent_name, agent_type, working_directory, branch, commit_hash, session_id, recorded_at)
		VALUES ('agent1', 'claude', '/tmp', 'main', 'def456', 'sess-1', '2026-01-02')
	`)
	require.NoError(t, err)
}

func TestSchema_ForeignKeyCascade(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Create a tag
	_, err := db.ExecContext(ctx, "INSERT INTO tags (name, color) VALUES ('bug', '#ff0000')")
	require.NoError(t, err)

	// Link it to a session
	_, err = db.ExecContext(ctx, "INSERT INTO session_tags (session_id, tag_id) VALUES ('sess-1', 1)")
	require.NoError(t, err)

	// Delete the tag — should cascade
	_, err = db.ExecContext(ctx, "DELETE FROM tags WHERE id = 1")
	require.NoError(t, err)

	var count int
	err = db.GetContext(ctx, &count, "SELECT COUNT(*) FROM session_tags WHERE tag_id = 1")
	require.NoError(t, err)
	assert.Equal(t, 0, count, "session_tags row should be cascade-deleted")
}

func TestOpen_IdempotentSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	// Open twice — schema creation should be idempotent
	db1, err := Open(dbPath)
	require.NoError(t, err)
	db1.Close()

	db2, err := Open(dbPath)
	require.NoError(t, err)
	db2.Close()
}
