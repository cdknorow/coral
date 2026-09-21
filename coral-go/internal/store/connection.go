// Package store provides the SQLite storage layer for Coral.
package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

// DB wraps an sqlx.DB with schema management and migration support.
type DB struct {
	*sqlx.DB
}

// Open creates a new DB connection to the given SQLite path.
// It ensures the parent directory exists, sets WAL mode, and runs migrations.
func Open(dbPath string) (*DB, error) {
	return OpenWithContext(context.Background(), dbPath)
}

// OpenWithContext creates a new DB connection with context support.
func OpenWithContext(ctx context.Context, dbPath string) (*DB, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	db, err := sqlx.Open("sqlite", dbPath+"?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)&_pragma=cache_size(-8000)")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// SQLite should use a single connection to avoid locking issues.
	db.SetMaxOpenConns(1)

	d := &DB{
		DB: db,
	}

	if err := d.ensureSchema(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ensure schema: %w", err)
	}

	return d, nil
}

// ensureSchema creates all tables, indexes, and runs migrations.
func (d *DB) ensureSchema(ctx context.Context) error {
	// Create all tables
	if _, err := d.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	// Run column migrations (ignore errors for already-existing columns)
	for _, m := range columnMigrations {
		sql := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", m.table, m.column, m.definition)
		d.ExecContext(ctx, sql) // Ignore error — column may already exist
	}

	// Fix double-encoded detail_json rows in agent_events (one-time fixup).
	// Prior to the fix, makeToolDetail returned a JSON string which was then
	// re-marshaled, producing '"{\"key\":\"val\"}"' instead of '{"key":"val"}'.
	d.ExecContext(ctx, `UPDATE agent_events SET detail_json = json_extract(detail_json, '$')
		WHERE detail_json IS NOT NULL AND typeof(json_extract(detail_json, '$')) = 'text'
		AND json_valid(json_extract(detail_json, '$'))`)

	return nil
}

var columnMigrations = []struct {
	table, column, definition string
}{
	{"agent_notes", "session_id", "TEXT"},
	{"agent_tasks", "session_id", "TEXT"},
	{"session_meta", "display_name", "TEXT"},
	{"live_sessions", "resume_from_id", "TEXT"},
	{"live_sessions", "flags", "TEXT"},
	{"live_sessions", "is_job", "INTEGER NOT NULL DEFAULT 0"},
	{"live_sessions", "prompt", "TEXT"},
	{"live_sessions", "board_name", "TEXT"},
	{"live_sessions", "board_server", "TEXT"},
	{"live_sessions", "backend", "TEXT DEFAULT 'tmux'"},
	{"live_sessions", "icon", "TEXT"},
	{"scheduled_jobs", "flags", "TEXT DEFAULT ''"},
	{"scheduled_runs", "trigger_type", "TEXT DEFAULT 'cron'"},
	{"scheduled_runs", "webhook_url", "TEXT"},
	{"scheduled_runs", "display_name", "TEXT"},
	{"live_sessions", "is_sleeping", "INTEGER NOT NULL DEFAULT 0"},
	{"live_sessions", "board_type", "TEXT"},
	{"live_sessions", "git_diff_mode", "TEXT"},
	{"live_sessions", "capabilities", "TEXT"},
	{"live_sessions", "model", "TEXT"},
	{"live_sessions", "pid", "INTEGER NOT NULL DEFAULT 0"},
	{"live_sessions", "tools", "TEXT"},
	{"live_sessions", "mcp_servers", "TEXT"},
	{"scheduled_jobs", "job_type", "TEXT DEFAULT 'prompt'"},
	{"scheduled_jobs", "workflow_id", "INTEGER"},
	{"live_sessions", "worktree_path", "TEXT"},
	{"live_sessions", "worktree_repo", "TEXT"},
	{"live_sessions", "team_id", "INTEGER REFERENCES teams(id) ON DELETE SET NULL"},
	{"live_sessions", "status", "TEXT NOT NULL DEFAULT 'active'"},
	{"live_sessions", "context_window", "INTEGER NOT NULL DEFAULT 0"},
	{"live_sessions", "stopped_at", "TEXT"},
	{"session_index", "agent_name", "TEXT"},
	{"session_index", "display_name", "TEXT"},
	{"token_usage", "cache_read_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"token_usage", "cache_write_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"token_usage", "session_start_at", "TEXT"},
	{"token_usage", "last_activity_at", "TEXT"},
	{"token_usage", "source", "TEXT NOT NULL DEFAULT 'jsonl'"},
	{"agent_tasks", "started_at", "TEXT"},
	{"agent_tasks", "completed_at", "TEXT"},
	{"agent_tasks", "cost_usd", "REAL NOT NULL DEFAULT 0"},
	{"agent_tasks", "input_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"agent_tasks", "output_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"agent_tasks", "cache_read_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"agent_tasks", "cache_write_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"agent_tasks", "display_name", "TEXT"},
	{"git_snapshots", "pr_number", "INTEGER"},
	{"git_changed_files", "diff_mode", "TEXT DEFAULT ''"},
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS session_meta (
	session_id   TEXT PRIMARY KEY,
	notes_md     TEXT DEFAULT '',
	auto_summary TEXT DEFAULT '',
	is_user_edited INTEGER DEFAULT 0,
	display_name TEXT,
	created_at   TEXT NOT NULL,
	updated_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tags (
	id    INTEGER PRIMARY KEY AUTOINCREMENT,
	name  TEXT UNIQUE NOT NULL,
	color TEXT NOT NULL DEFAULT '#58a6ff'
);

CREATE TABLE IF NOT EXISTS session_tags (
	session_id TEXT NOT NULL,
	tag_id     INTEGER NOT NULL,
	PRIMARY KEY (session_id, tag_id),
	FOREIGN KEY (tag_id) REFERENCES tags(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS folder_tags (
	folder_name TEXT NOT NULL,
	tag_id      INTEGER NOT NULL,
	PRIMARY KEY (folder_name, tag_id),
	FOREIGN KEY (tag_id) REFERENCES tags(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS session_index (
	session_id      TEXT PRIMARY KEY,
	source_type     TEXT NOT NULL,
	source_file     TEXT NOT NULL,
	first_timestamp TEXT,
	last_timestamp  TEXT,
	message_count   INTEGER DEFAULT 0,
	display_summary TEXT DEFAULT '',
	indexed_at      TEXT NOT NULL,
	file_mtime      REAL NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_session_index_last_ts
	ON session_index(last_timestamp DESC);

CREATE VIRTUAL TABLE IF NOT EXISTS session_fts
	USING fts5(session_id, body, tokenize='porter');

CREATE TABLE IF NOT EXISTS summarizer_queue (
	session_id   TEXT PRIMARY KEY,
	status       TEXT NOT NULL DEFAULT 'pending',
	attempted_at TEXT,
	error_msg    TEXT
);

CREATE TABLE IF NOT EXISTS git_snapshots (
	id                INTEGER PRIMARY KEY AUTOINCREMENT,
	agent_name        TEXT NOT NULL,
	agent_type        TEXT NOT NULL,
	working_directory TEXT NOT NULL,
	branch            TEXT NOT NULL,
	commit_hash       TEXT NOT NULL,
	commit_subject    TEXT DEFAULT '',
	commit_timestamp  TEXT,
	session_id        TEXT,
	remote_url        TEXT,
	recorded_at       TEXT NOT NULL,
	UNIQUE(session_id, commit_hash)
);

CREATE TABLE IF NOT EXISTS agent_tasks (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	agent_name  TEXT NOT NULL,
	session_id  TEXT,
	title       TEXT NOT NULL,
	completed   INTEGER DEFAULT 0,
	sort_order  INTEGER DEFAULT 0,
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_notes (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	agent_name  TEXT NOT NULL,
	session_id  TEXT,
	content     TEXT NOT NULL,
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_events (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	agent_name  TEXT NOT NULL,
	session_id  TEXT,
	event_type  TEXT NOT NULL,
	tool_name   TEXT,
	summary     TEXT NOT NULL,
	detail_json TEXT,
	created_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS live_sessions (
	session_id    TEXT PRIMARY KEY,
	agent_type    TEXT NOT NULL,
	agent_name    TEXT NOT NULL,
	working_dir   TEXT NOT NULL,
	display_name  TEXT,
	resume_from_id TEXT,
	flags         TEXT,
	is_job        INTEGER NOT NULL DEFAULT 0,
	created_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS user_settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS scheduled_jobs (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	name            TEXT NOT NULL,
	description     TEXT DEFAULT '',
	cron_expr       TEXT NOT NULL,
	timezone        TEXT NOT NULL DEFAULT 'UTC',
	agent_type      TEXT NOT NULL DEFAULT 'claude',
	repo_path       TEXT NOT NULL,
	base_branch     TEXT DEFAULT 'main',
	prompt          TEXT NOT NULL,
	enabled         INTEGER NOT NULL DEFAULT 1,
	max_duration_s  INTEGER NOT NULL DEFAULT 3600,
	cleanup_worktree INTEGER NOT NULL DEFAULT 1,
	flags           TEXT DEFAULT '',
	created_at      TEXT NOT NULL,
	updated_at      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_scheduled_jobs_enabled
	ON scheduled_jobs(enabled, id);

CREATE TABLE IF NOT EXISTS scheduled_runs (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	job_id          INTEGER NOT NULL REFERENCES scheduled_jobs(id) ON DELETE CASCADE,
	session_id      TEXT,
	worktree_path   TEXT,
	status          TEXT NOT NULL DEFAULT 'pending',
	scheduled_at    TEXT NOT NULL,
	started_at      TEXT,
	finished_at     TEXT,
	exit_reason     TEXT,
	error_msg       TEXT,
	created_at      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_scheduled_runs_job
	ON scheduled_runs(job_id, scheduled_at DESC);

CREATE INDEX IF NOT EXISTS idx_scheduled_runs_session
	ON scheduled_runs(session_id);

CREATE INDEX IF NOT EXISTS idx_scheduled_runs_status
	ON scheduled_runs(status, scheduled_at DESC);

CREATE TABLE IF NOT EXISTS webhook_configs (
	id                     INTEGER PRIMARY KEY AUTOINCREMENT,
	name                   TEXT NOT NULL,
	platform               TEXT NOT NULL,
	url                    TEXT NOT NULL,
	enabled                INTEGER NOT NULL DEFAULT 1,
	event_filter           TEXT NOT NULL DEFAULT '*',
	idle_threshold_seconds INTEGER NOT NULL DEFAULT 0,
	agent_filter           TEXT,
	low_confidence_only    INTEGER NOT NULL DEFAULT 0,
	consecutive_failures   INTEGER NOT NULL DEFAULT 0,
	created_at             TEXT NOT NULL,
	updated_at             TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	webhook_id    INTEGER NOT NULL,
	agent_name    TEXT NOT NULL,
	session_id    TEXT,
	event_type    TEXT NOT NULL,
	event_summary TEXT NOT NULL,
	status        TEXT NOT NULL DEFAULT 'pending',
	http_status   INTEGER,
	error_msg     TEXT,
	attempt_count INTEGER NOT NULL DEFAULT 0,
	next_retry_at TEXT,
	delivered_at  TEXT,
	created_at    TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_webhook
	ON webhook_deliveries(webhook_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_pending
	ON webhook_deliveries(status, next_retry_at)
	WHERE status = 'pending';

CREATE TABLE IF NOT EXISTS git_changed_files (
	id                INTEGER PRIMARY KEY AUTOINCREMENT,
	agent_name        TEXT NOT NULL,
	session_id        TEXT,
	working_directory TEXT NOT NULL,
	filepath          TEXT NOT NULL,
	additions         INTEGER NOT NULL DEFAULT 0,
	deletions         INTEGER NOT NULL DEFAULT 0,
	status            TEXT NOT NULL DEFAULT 'M',
	recorded_at       TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_git_changed_files_session
	ON git_changed_files(session_id);

CREATE INDEX IF NOT EXISTS idx_git_changed_files_agent
	ON git_changed_files(agent_name);

CREATE TABLE IF NOT EXISTS workflows (
	id                 INTEGER PRIMARY KEY AUTOINCREMENT,
	name               TEXT NOT NULL UNIQUE,
	description        TEXT DEFAULT '',
	steps_json         TEXT NOT NULL DEFAULT '[]',
	default_agent_json TEXT DEFAULT '',
	repo_path          TEXT DEFAULT '',
	base_branch        TEXT DEFAULT 'main',
	max_duration_s     INTEGER NOT NULL DEFAULT 3600,
	cleanup_worktree   INTEGER NOT NULL DEFAULT 1,
	enabled            INTEGER NOT NULL DEFAULT 1,
	created_at         TEXT NOT NULL,
	updated_at         TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS workflow_runs (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	workflow_id     INTEGER NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
	trigger_type    TEXT NOT NULL DEFAULT 'api',
	trigger_context TEXT,
	status          TEXT NOT NULL DEFAULT 'pending',
	current_step    INTEGER NOT NULL DEFAULT 0,
	step_results    TEXT NOT NULL DEFAULT '[]',
	worktree_path   TEXT DEFAULT '',
	started_at      TEXT,
	finished_at     TEXT,
	error_msg       TEXT,
	created_at      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_workflow_runs_workflow
	ON workflow_runs(workflow_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_workflow_runs_status
	ON workflow_runs(status);

CREATE TABLE IF NOT EXISTS connected_apps (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	provider_id     TEXT NOT NULL,
	name            TEXT NOT NULL,
	client_id       TEXT NOT NULL,
	client_secret   TEXT NOT NULL,
	scopes          TEXT NOT NULL DEFAULT '',
	access_token    TEXT NOT NULL DEFAULT '',
	refresh_token   TEXT NOT NULL DEFAULT '',
	token_expiry    TEXT,
	account_email   TEXT DEFAULT '',
	account_name    TEXT DEFAULT '',
	status          TEXT NOT NULL DEFAULT 'active',
	created_at      TEXT NOT NULL,
	updated_at      TEXT NOT NULL,
	UNIQUE(provider_id, name)
);

CREATE TABLE IF NOT EXISTS custom_views (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT NOT NULL,
	prompt     TEXT NOT NULL DEFAULT '',
	html       TEXT NOT NULL DEFAULT '',
	tab_order  INTEGER NOT NULL DEFAULT 0,
	scope      TEXT NOT NULL DEFAULT 'global',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS teams (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL UNIQUE,
    config_json     TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'running'
        CHECK (status IN ('running', 'sleeping', 'stopped')),
    working_dir     TEXT NOT NULL,
    is_worktree     INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    stopped_at      TEXT
);

CREATE INDEX IF NOT EXISTS idx_teams_status ON teams(status);

CREATE TABLE IF NOT EXISTS team_members (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    team_id           INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    agent_name        TEXT NOT NULL,
    agent_config_json TEXT NOT NULL,
    session_id        TEXT,
    status            TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'sleeping', 'stopped')),
    created_at        TEXT NOT NULL,
    stopped_at        TEXT
);

CREATE INDEX IF NOT EXISTS idx_team_members_team ON team_members(team_id, status);

-- Subagents launched by a main agent. Kept separate from live_sessions: a
-- subagent is not a Coral-managed agent (no tmux session, board, or prompt),
-- it is work a main agent delegated. session_id is the relational tie back to
-- the main agent that launched it.
CREATE TABLE IF NOT EXISTS subagents (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id         TEXT NOT NULL REFERENCES live_sessions(session_id) ON DELETE CASCADE,
    subagent_id        TEXT NOT NULL,
    subagent_type      TEXT,
    description        TEXT,
    tool_use_id        TEXT,
    model              TEXT,
    spawn_depth        INTEGER NOT NULL DEFAULT 0,
    api_calls          INTEGER NOT NULL DEFAULT 0,
    input_tokens       INTEGER NOT NULL DEFAULT 0,
    output_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd           REAL NOT NULL DEFAULT 0,
    started_at         TEXT,
    last_activity_at   TEXT,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    UNIQUE(session_id, subagent_id)
);
CREATE INDEX IF NOT EXISTS idx_subagents_session ON subagents(session_id);

CREATE TABLE IF NOT EXISTS token_usage (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id      TEXT NOT NULL,
    agent_name      TEXT NOT NULL,
    agent_type      TEXT NOT NULL DEFAULT 'claude',
    team_id         INTEGER,
    board_name      TEXT,
    input_tokens    INTEGER NOT NULL DEFAULT 0,
    output_tokens   INTEGER NOT NULL DEFAULT 0,
    total_tokens    INTEGER NOT NULL DEFAULT 0,
    cost_usd        REAL NOT NULL DEFAULT 0,
    num_turns       INTEGER NOT NULL DEFAULT 0,
    recorded_at     TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_token_usage_session_time ON token_usage(session_id, recorded_at);
CREATE INDEX IF NOT EXISTS idx_token_usage_team ON token_usage(team_id);
CREATE INDEX IF NOT EXISTS idx_token_usage_time ON token_usage(recorded_at);

CREATE INDEX IF NOT EXISTS idx_git_snap_session ON git_snapshots(session_id);
CREATE INDEX IF NOT EXISTS idx_session_tags_tag_id ON session_tags(tag_id);
CREATE INDEX IF NOT EXISTS idx_folder_tags_tag_id ON folder_tags(tag_id);
CREATE INDEX IF NOT EXISTS idx_session_index_first_ts ON session_index(first_timestamp);
CREATE INDEX IF NOT EXISTS idx_agent_events_session ON agent_events(session_id);
CREATE INDEX IF NOT EXISTS idx_agent_events_session_type ON agent_events(session_id, event_type);
CREATE INDEX IF NOT EXISTS idx_git_snap_session_time ON git_snapshots(session_id, recorded_at DESC);
`
