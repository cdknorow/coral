// Package store provides the SQLite storage layer for Coral.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/dbcrypt"
	"github.com/subgentic/coral/internal/gitutil"
	"github.com/subgentic/coral/internal/testguard"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

// DB wraps an sqlx.DB with schema management and migration support.
type DB struct {
	*sqlx.DB
	readDB     *sqlx.DB
	TaskEngine *board.Store

	callMetricsInit sync.Once
	callMetrics     *callMetricBuffer

	// tokenOwners caches SessionTokenOwnerOf (#4303); see session_tokens.go.
	tokenOwners tokenOwnerCache
	proofKeys   proofKeySet
}

// Reader returns the read-only connection pool, or the primary connection
// if no separate read pool exists (such as for in-memory databases).
func (d *DB) Reader() *sqlx.DB {
	if d.readDB != nil {
		return d.readDB
	}
	return d.DB
}

// ReaderStats returns database stats for the read-only connection pool.
func (d *DB) ReaderStats() sql.DBStats {
	return d.Reader().Stats()
}

// Close writes queued request metrics, then closes the database and read pool.
func (d *DB) Close() error {
	d.closeCallMetrics()
	var firstErr error
	if d.readDB != nil && d.readDB != d.DB {
		if err := d.readDB.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := d.DB.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// OpenWithKey opens a plaintext database when key is empty, or a SQLCipher
// database when key is present. The caller must resolve the key before this
// function is called so no startup path can silently fall back to plaintext.
func OpenWithKey(ctx context.Context, dbPath, key string) (*DB, error) {
	return openWithDriver(ctx, dbPath, key, false)
}

// OpenPrepared opens a plaintext sessions database file that is a copy of one this build already migrated (storetest makes it): the schema step is not run again. It is for
// tests, which open hundreds of databases: parsing the whole schema each time was a large part of a heavy test's CPU. A file that is not a copy of this build's migrated
// database must not be opened with it.
func OpenPrepared(ctx context.Context, dbPath string) (*DB, error) {
	return openWithDriver(ctx, dbPath, "", true)
}

func openWithDriver(ctx context.Context, dbPath, key string, schemaDone bool) (*DB, error) {
	testguard.CheckDataPath(dbPath)
	if key == "" {
		if err := dbcrypt.RejectEncryptedFile(dbPath); err != nil {
			return nil, err
		}
	}
	if key != "" {
		// Fail before touching the filesystem: a standard build has no
		// SQLCipher driver and must not create or open anything with a key.
		if err := dbcrypt.RequireAvailable(); err != nil {
			return nil, err
		}
	}
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	driver := "sqlite"
	dsn := dbPath + "?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)&_pragma=cache_size(-8000)" + journalSizeLimitParam
	if key != "" {
		// The SQLCipher driver's own DSN keys: it ignores _pragma_ ones (#3454).
		driver = dbcrypt.EncryptedDriver
		dsn = dbcrypt.EncryptedDSN(dbPath, key, dbcrypt.DSNOptions{ForeignKeys: true})
	}
	db, err := sqlx.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// SQLite should use a single connection to avoid locking issues.
	db.SetMaxOpenConns(1)

	d := &DB{
		DB: db,
	}

	if !schemaDone {
		if err := d.ensureSchema(ctx); err != nil {
			db.Close()
			return nil, fmt.Errorf("ensure schema: %w", err)
		}
	}
	scrubRemoteURLCredentials(ctx, d)
	if err := d.initAgentTaskEngine(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize personal task workflows: %w", err)
	}
	if key != "" {
		reportForeignKeyOrphans(ctx, dbPath, key)
	}

	if isInMemoryPath(dbPath) {
		d.readDB = db
	} else {
		readDSN := dbPath + "?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)&_pragma=cache_size(-8000)&_pragma=query_only(1)"
		if key != "" {
			readDSN = dbcrypt.EncryptedDSN(dbPath, key, dbcrypt.DSNOptions{ReadOnly: true, ForeignKeys: true})
		}
		readDB, err := sqlx.Open(driver, readDSN)
		if err != nil {
			d.Close()
			return nil, fmt.Errorf("open read database: %w", err)
		}
		readDB.SetMaxOpenConns(4)
		readDB.SetMaxIdleConns(4)
		d.readDB = readDB
	}

	return d, nil
}

func isInMemoryPath(dbPath string) bool {
	if dbPath == "" || dbPath == ":memory:" {
		return true
	}
	if strings.Contains(dbPath, ":memory:") {
		return true
	}
	if strings.HasPrefix(dbPath, "file:") && strings.Contains(dbPath, "mode=memory") {
		return true
	}
	return false
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

	// A board bookmark belongs to its board's team (#4792); an agent's takes the team it was saved with.
	d.ExecContext(ctx, `UPDATE saved_messages SET team = ref WHERE kind = 'board' AND team = ''`)

	// Archived implies sleeping. A Coral older than is_archived can wake an
	// archived agent without clearing the flag; restore the invariant on open.
	d.ExecContext(ctx, `UPDATE live_sessions SET is_archived = 0 WHERE is_archived = 1 AND is_sleeping = 0`)

	// A tag's colour is #rgb, #rrggbb or #rrggbbaa (#4875): a row stored before that was checked is given the default colour, so that no client of
	// the API (the dashboard checks on read; Coral Hub's own pages may not) gets a colour that is CSS. Only the colour value changes.
	if n, err := d.ResetInvalidTagColors(ctx); err != nil {
		slog.Warn("could not check the tag colours", "error", err)
	} else if n > 0 {
		slog.Warn("tag colours that were not hex colours were reset to the default", "tags", n)
	}

	// Indexes on migrated columns cannot live in schemaSQL: on an older
	// database the column does not exist yet when schemaSQL runs.
	d.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_agent_events_ref
		ON agent_events(session_id, event_type, ref_id) WHERE ref_id IS NOT NULL`)

	// The (session_id, created_at) and (session_id, event_type, created_at)
	// indexes in schemaSQL cover these prefixes; the rowid that SQLite appends
	// to every index entry lets "latest per session" queries seek backwards
	// instead of sorting the history.
	d.ExecContext(ctx, `DROP INDEX IF EXISTS idx_agent_events_session`)
	d.ExecContext(ctx, `DROP INDEX IF EXISTS idx_agent_events_session_type`)
	// Pre-release name of idx_agent_events_edited_files, without json_valid.
	d.ExecContext(ctx, `DROP INDEX IF EXISTS idx_agent_events_file_edits`)
	// Nothing filters call_metrics by agent or board (the summary reads a
	// created_at range), and these two were ~550 MB on a busy server and made
	// every insert update three indexes instead of one (#3261).
	d.ExecContext(ctx, `DROP INDEX IF EXISTS idx_call_metrics_agent`)
	d.ExecContext(ctx, `DROP INDEX IF EXISTS idx_call_metrics_board`)

	// Indexes on migrated columns cannot live in schemaSQL (see above).
	// sourceDedup's NOT IN subquery reads every jsonl session id.
	d.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_token_usage_source ON token_usage(source, session_id)`)
	if err := d.ensureTokenUsageRollup(ctx); err != nil {
		return err
	}
	if err := d.ensureTokenUsageHourAgg(ctx); err != nil {
		return err
	}
	if err := d.ensureTokenUsageThinkingModel(ctx); err != nil {
		return err
	}

	// Fix double-encoded detail_json rows in agent_events (one-time fixup).
	// Prior to the fix, makeToolDetail returned a JSON string which was then
	// re-marshaled, producing '"{\"key\":\"val\"}"' instead of '{"key":"val"}'.
	// It reads every event, so it runs once (#4129: ~450 ms on 200k events, cold, on every
	// start, growing with the history): a store_meta row marks it done.
	fixDoubleEncodedDetailJSON(ctx, d)

	// After the fixup above, so a new table is filled from the fixed rows (#3846).
	if err := d.ensureEditedFiles(ctx); err != nil {
		return err
	}
	return nil
}

var columnMigrations = []struct {
	table, column, definition string
}{
	// Why a run failed, as a fixed word, and the agent CLI it concerns (#5469): "not_signed_in" and "claude". NULL on every other run.
	{"workflow_runs", "error_code", "TEXT"},
	{"workflow_runs", "agent_type", "TEXT"},
	{"scheduled_runs", "error_code", "TEXT"},
	{"scheduled_runs", "agent_type", "TEXT"},
	// Existing views have unknown provenance, so they migrate as "api": the dashboard asks once before running them.
	{"custom_views", "created_via", "TEXT NOT NULL DEFAULT 'api'"},
	// Where a bookmark belongs (#4792): its team (the board), and for a board message who wrote it (NULL: not looked up yet).
	{"saved_messages", "team", "TEXT NOT NULL DEFAULT ''"},
	{"saved_messages", "author", "TEXT"},
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
	{"live_sessions", "list_at", "TEXT"},  // the time the agent's place in the list was taken; a replacement session keeps its predecessor's (#4943)
	{"live_sessions", "thinking", "TEXT"}, // the thinking level the agent was started with; NULL or "" is the CLI's default (#4969)
	{"live_sessions", "mcp_servers", "TEXT"},
	{"scheduled_jobs", "job_type", "TEXT DEFAULT 'prompt'"},
	{"scheduled_jobs", "workflow_id", "INTEGER"},
	// Why the scheduler paused a job itself (failing repeatedly); cleared when it is resumed (#3171).
	{"scheduled_jobs", "paused_reason", "TEXT DEFAULT ''"},
	// When the job was last enabled again (0 -> 1): the auto-pause counts only runs after it (#3171).
	{"scheduled_jobs", "resumed_at", "TEXT DEFAULT ''"},
	{"live_sessions", "worktree_path", "TEXT"},
	{"live_sessions", "worktree_repo", "TEXT"},
	{"live_sessions", "team_id", "INTEGER REFERENCES teams(id) ON DELETE SET NULL"},
	{"live_sessions", "status", "TEXT NOT NULL DEFAULT 'active'"},
	{"live_sessions", "context_window", "INTEGER NOT NULL DEFAULT 0"},
	// The model a CLI without a transcript model (agy) last reported in a hook payload. Kept apart from model, which a
	// restart passes back to the CLI as its --model: an observed id is for display only.
	{"live_sessions", "observed_model", "TEXT"},
	{"live_sessions", "stopped_at", "TEXT"},
	// User-chosen colour for the agent's name in the sidebar (#rrggbb); NULL = palette default.
	{"live_sessions", "name_color", "TEXT"},
	// Archived: put away by the operator. Always set together with is_sleeping=1 (so every "running" check already
	// excludes it); unlike plain sleeping it is left out of Wake All, team wake and orphan cleanup.
	{"live_sessions", "is_archived", "INTEGER NOT NULL DEFAULT 0"},
	// Why Coral put a sleeping agent to sleep, when it was not the user (#3344): 'terminal_gone'
	// when its terminal session no longer exists. NULL for a sleep the user asked for; cleared on wake.
	{"live_sessions", "sleep_reason", "TEXT"},
	// Auto-accept (#3182): answer this agent's approve-once permission prompts. A new row (launch, restart, resume)
	// starts off, and sleep/wake/archive clear it, so it is never on for a new agent run. The paused code is set when
	// Coral turns it off (loop guard, expiry, hub access withdrawn, Stop all); enabling again clears it.
	{"live_sessions", "auto_accept", "INTEGER NOT NULL DEFAULT 0"},
	{"live_sessions", "auto_accept_until", "INTEGER"},    // unix seconds; NULL = no time limit
	{"live_sessions", "auto_accept_source", "TEXT"},      // 'local' (dashboard) or 'hub' (Coral Hub)
	{"live_sessions", "auto_accept_paused_code", "TEXT"}, // why Coral turned it off (see store/auto_accept.go)
	// When Coral turned it off (#4521): unix seconds, set and cleared with the paused code. A client tells a pause it has
	// already shown from a new one by it (the code alone is the same on every reload).
	{"live_sessions", "auto_accept_paused_at", "INTEGER"},
	// The version of the CLI the agent was launched with (#3481), e.g. "2.1.300"; NULL = unknown.
	{"live_sessions", "cli_version", "TEXT"},
	// The hook set a Claude agent loaded at launch (#3665), e.g. 3; NULL = unknown.
	{"live_sessions", "hooks_rev", "INTEGER"},
	// The folder the agent was launched with its Coral tools (coral-board and the coral-hook-* programs) on its PATH, e.g. the app bundle (#6089); NULL or "" = none recorded.
	{"live_sessions", "hook_dir", "TEXT"},
	// The tag the processes of an agent started on tmux carry in their environment (CORAL_PTY_RUN, #6141): random, kept with the session (and handed on when it is restarted or resumed) so a Kill finds what an
	// earlier run of the same session left behind; NULL for an agent started before this or on the built-in terminal (which has a tag of its own per terminal).
	{"live_sessions", "run_tag", "TEXT"},
	// The proof key derived from a session token (#6103): Coral proves it is Coral to a tool holding the token before the tool sends it; NULL on a token minted before this (those tools do not ask).
	{"session_tokens", "proof_key", "TEXT"},
	{"live_sessions", "permission_mode", "TEXT"}, // the launch's effective permission mode (#5188)
	{"cloud_connection", "route_key", "TEXT"},    // the hub's routing hint for this enrollment (#3683)
	{"cloud_connection", "hub_owner", "TEXT"},    // the Coral Hub account that owns this enrollment (#3966)
	// The owner's hub-user-<8 hex> label as the hub stamps it, recorded at enrolment only (#5280); NULL = unknown.
	{"cloud_connection", "hub_owner_user", "TEXT"},
	{"cloud_connection", "hub_owner_adopted_at", "TEXT"}, // set only when the owner label was adopted locally by the person at this computer (#6108)
	// A Hub sign-in's grant (#3966): empty for every other browser session.
	{"browser_sessions", "hub_grant", "TEXT NOT NULL DEFAULT ''"},
	{"browser_sessions", "hub_account", "TEXT NOT NULL DEFAULT ''"},
	{"browser_sessions", "hub_display", "TEXT NOT NULL DEFAULT ''"},
	{"browser_sessions", "hub_last_ok", "INTEGER NOT NULL DEFAULT 0"},
	{"session_index", "agent_name", "TEXT"},
	{"session_index", "display_name", "TEXT"},
	{"session_index", "index_version", "INTEGER NOT NULL DEFAULT 0"},
	{"token_usage", "cache_read_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"token_usage", "cache_write_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"token_usage", "session_start_at", "TEXT"},
	{"token_usage", "last_activity_at", "TEXT"},
	{"token_usage", "source", "TEXT NOT NULL DEFAULT 'jsonl'"},
	// The model that produced the row's tokens, as reported by the agent. NULL
	// when it was not observed, and for every row written before this column
	// existed. Without it a stored cost cannot be re-derived from its tokens.
	{"token_usage", "model", "TEXT"},
	{"agent_tasks", "started_at", "TEXT"},
	{"agent_tasks", "completed_at", "TEXT"},
	{"agent_tasks", "cost_usd", "REAL NOT NULL DEFAULT 0"},
	{"agent_tasks", "input_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"agent_tasks", "output_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"agent_tasks", "cache_read_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"agent_tasks", "cache_write_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"agent_tasks", "display_name", "TEXT"},
	// The task's details, for tasks the operator gives an agent (shown when
	// the agent claims it).
	{"agent_tasks", "body", "TEXT"},
	// Board-task parity for the coral-agent CLI (task add --priority,
	// complete/cancel --message).
	{"agent_tasks", "priority", "TEXT DEFAULT 'medium'"},
	{"agent_tasks", "completion_message", "TEXT"},
	// Databases created before `finished` was part of the subagents table.
	{"subagents", "finished", "INTEGER NOT NULL DEFAULT 0"},
	{"subagents", "transcript_path", "TEXT"},
	{"git_snapshots", "pr_number", "INTEGER"},
	{"git_snapshots", "is_worktree", "INTEGER DEFAULT 0"},
	{"git_snapshots", "base_branch", "TEXT DEFAULT ''"},
	{"git_snapshots", "ahead", "INTEGER DEFAULT 0"},
	{"git_snapshots", "behind", "INTEGER DEFAULT 0"},
	{"git_snapshots", "dirty_count", "INTEGER DEFAULT 0"},
	{"git_changed_files", "diff_mode", "TEXT DEFAULT ''"},
	// How long the activity took, and the id it is known by at its source
	// (tool_use_id for tool calls, API request id for thinking turns).
	{"agent_events", "duration_ms", "INTEGER"},
	{"agent_events", "ref_id", "TEXT"},
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS agent_ui_panels (
 session_id TEXT NOT NULL, id TEXT NOT NULL, title TEXT NOT NULL,
 html TEXT NOT NULL, revision INTEGER NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY(session_id,id)
);
CREATE TABLE IF NOT EXISTS agent_ui_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL,
 panel_id TEXT NOT NULL, revision INTEGER NOT NULL, action TEXT NOT NULL,
 payload TEXT NOT NULL, created_at TEXT NOT NULL,
 FOREIGN KEY(session_id,panel_id) REFERENCES agent_ui_panels(session_id,id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_agent_ui_events ON agent_ui_events(session_id,panel_id,id);

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

-- Folders the Operator pinned to the Agents list (#3630): shown even with no agents in them.
CREATE TABLE IF NOT EXISTS pinned_folders (
	path      TEXT PRIMARY KEY,
	pinned_at TEXT NOT NULL
);

-- Messages the Operator saved for later (#4510): a bookmark on one message of an agent's chat or a board, with a snippet, until it is removed.
-- msg_key is the message's stable key (the chat route's "key", a board message's id), so a bookmark survives a reload and transcript paging.
-- Custom models (#5480): a model id the operator adds for a provider (family: claude, codex, agy, pi) with the thinking levels it takes (efforts, a JSON array). The set is
-- replaced as a whole (position keeps the order sent); validation is agentmodels.ValidateCustomSet, and a row read back is checked again.
CREATE TABLE IF NOT EXISTS custom_models (
	family       TEXT NOT NULL,
	value        TEXT NOT NULL,
	display_name TEXT NOT NULL DEFAULT '',
	efforts      TEXT NOT NULL DEFAULT '[]',
	position     INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (family, value)
);

CREATE TABLE IF NOT EXISTS saved_messages (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	kind       TEXT NOT NULL,
	ref        TEXT NOT NULL,
	msg_key    TEXT NOT NULL,
	agent_type TEXT NOT NULL DEFAULT '',
	title      TEXT NOT NULL DEFAULT '',
	snippet    TEXT NOT NULL DEFAULT '',
	said_at    TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	done_at    TEXT,
	UNIQUE (kind, ref, msg_key)
);

-- Comments on an artifact (#5176): a comment belongs to that exact artifact id (content-addressed, so its text never changes). parent_id is one level of replies. anchor_json is the
-- quote, its context and line range (or whole-document), kept as sent. The author is (author_issuer, author_subject): who vouches for the identity and who it is, so ownership is one
-- comparison: 'local' and the local user for the owner not signed in to Coral Hub, the hub's origin and its account for a hub identity (today a hub viewer's hub-user id), an agent's
-- session for an agent; author_kind is owner, hub or agent and author_name an optional display name (text, capped, empty until the hub sends one).
-- A deleted comment is a tombstone (deleted_at, an empty body) so its replies keep their place. The owner's lock switch is per artifact or per board (artifact_comment_locks).
CREATE TABLE IF NOT EXISTS artifact_comments (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	artifact_id TEXT NOT NULL,
	board       TEXT NOT NULL DEFAULT '',
	parent_id   INTEGER,
	anchor_json TEXT NOT NULL DEFAULT '',
	body        TEXT NOT NULL,
	author_issuer  TEXT NOT NULL,
	author_subject TEXT NOT NULL,
	author_name    TEXT NOT NULL DEFAULT '',
	author_kind    TEXT NOT NULL,
	created_at  TEXT NOT NULL,
	edited_at   TEXT,
	resolved_by_issuer  TEXT,
	resolved_by_subject TEXT,
	resolved_at TEXT,
	deleted_at  TEXT
);
CREATE INDEX IF NOT EXISTS idx_artifact_comments_artifact ON artifact_comments(artifact_id, created_at);
CREATE INDEX IF NOT EXISTS idx_artifact_comments_parent ON artifact_comments(parent_id);
CREATE TABLE IF NOT EXISTS artifact_comment_locks (
	scope TEXT NOT NULL,
	key   TEXT NOT NULL,
	PRIMARY KEY (scope, key)
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
	created_at  TEXT NOT NULL,
	duration_ms INTEGER,
	ref_id      TEXT
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

-- Derived metadata only: excluded rollout files still stay on disk.
CREATE TABLE IF NOT EXISTS history_exclusions (
 source_type TEXT NOT NULL,
 source_file TEXT NOT NULL,
 file_mtime REAL NOT NULL,
 index_version INTEGER NOT NULL,
 PRIMARY KEY (source_type, source_file)
);

-- When a live session's model or thinking level was last set by Coral and the CLI took it: confirmed from its own entry, or put on its command line at a wake (#6225). A turn the transcript
-- recorded before that is older than the setting.
CREATE TABLE IF NOT EXISTS live_session_settings (
 session_id TEXT PRIMARY KEY,
 set_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS store_meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
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

-- Retained remote-board subscriptions used by the background poller and
-- remote-board API. This table is intentionally separate from retired
-- provider credentials and proxy state.
CREATE TABLE IF NOT EXISTS remote_board_subscriptions (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id           TEXT NOT NULL,
    remote_server        TEXT NOT NULL,
    project              TEXT NOT NULL,
    job_title            TEXT NOT NULL,
    last_notified_unread INTEGER NOT NULL DEFAULT 0,
    created_at           TEXT NOT NULL,
    UNIQUE(session_id, remote_server, project)
);

-- Registered remote Coral servers for the multi-server hub. api_key holds a
-- "v1:" AES-256-GCM envelope (see internal/secretbox), never plaintext.
CREATE TABLE IF NOT EXISTS remote_servers (
    id            TEXT PRIMARY KEY,
    label         TEXT NOT NULL,
    url           TEXT NOT NULL,
    api_key       TEXT NOT NULL,
    allow_private INTEGER NOT NULL DEFAULT 0,
    status        TEXT NOT NULL DEFAULT 'unknown',
    created_at    TEXT NOT NULL,
    last_seen     TEXT,
    last_error    TEXT
);

-- This server's single Cloud Hub enrollment. token holds a "v1:" secretbox
-- envelope (see pkg/secretbox), never plaintext.
CREATE TABLE IF NOT EXISTS cloud_connection (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    hub_url    TEXT NOT NULL,
    server_id  TEXT NOT NULL,
    label      TEXT NOT NULL,
    token      TEXT NOT NULL,
    state      TEXT NOT NULL DEFAULT 'active',
    created_at TEXT NOT NULL,
    last_error TEXT
);

-- Coral Hub enrollment in progress, and its rate limit (one row). The device code is
-- sealed like the server token. Kept across restarts so a restart loop reuses one code
-- instead of asking the hub for a new one each time.
CREATE TABLE IF NOT EXISTS cloud_enrollment (
    id               INTEGER PRIMARY KEY CHECK (id = 1),
    hub_url          TEXT NOT NULL,
    device_code      TEXT NOT NULL DEFAULT '',
    user_code        TEXT NOT NULL DEFAULT '',
    verification_url TEXT NOT NULL DEFAULT '',
    interval_s       INTEGER NOT NULL DEFAULT 5,
    expires_at       TEXT NOT NULL DEFAULT '',
    next_start_at    TEXT NOT NULL DEFAULT '',
    failures         INTEGER NOT NULL DEFAULT 0
);

-- Artifacts the Operator keeps (#3554): never pruned, whatever their age or references.
CREATE TABLE IF NOT EXISTS artifact_keep (
    hex     TEXT PRIMARY KEY,
    kept_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS custom_views (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT NOT NULL,
	prompt     TEXT NOT NULL DEFAULT '',
	html       TEXT NOT NULL DEFAULT '',
	tab_order  INTEGER NOT NULL DEFAULT 0,
	scope      TEXT NOT NULL DEFAULT 'global',
	created_via TEXT NOT NULL DEFAULT 'api',
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
    finished           INTEGER NOT NULL DEFAULT 0,
    transcript_path    TEXT,
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

CREATE TABLE IF NOT EXISTS call_metrics (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    call_type TEXT NOT NULL,
    operation TEXT NOT NULL,
    agent_name TEXT,
    session_id TEXT,
    team_id INTEGER,
    board_name TEXT,
    method TEXT,
    status_code INTEGER,
    duration_ms INTEGER,
    is_error INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_call_metrics_created ON call_metrics(created_at);

-- Raw call metrics older than a day, one row per hour and kind of call (#4098).
CREATE TABLE IF NOT EXISTS call_metrics_hourly (
    hour TEXT NOT NULL,
    call_type TEXT NOT NULL,
    operation TEXT NOT NULL,
    agent_name TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL DEFAULT '',
    board_name TEXT NOT NULL DEFAULT '',
    calls INTEGER NOT NULL,
    errors INTEGER NOT NULL DEFAULT 0,
    duration_sum_ms INTEGER NOT NULL DEFAULT 0,
    duration_n INTEGER NOT NULL DEFAULT 0,
    p50_ms INTEGER NOT NULL DEFAULT 0,
    p95_ms INTEGER NOT NULL DEFAULT 0,
    UNIQUE(hour, call_type, operation, agent_name, session_id, board_name)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_token_usage_session_time ON token_usage(session_id, recorded_at);

-- The Cost view's breakdown by thinking level (#4969): the usage of the calls whose turn recorded a level, summed by UTC hour, agent type, model
-- (the call's model, as the By Model table has it; '' for a call that named none and for the rows kept from before the model was in the key, #5120) and
-- level, kept when a call is recorded (once: a repeated report of the same call is ignored, as the usage rows ignore it). Calls that
-- recorded no level are not here: the view shows them as "Not reported". It is small and kept as the usage history is.
CREATE TABLE IF NOT EXISTS token_usage_thinking (
    hour               TEXT NOT NULL,
    agent_type         TEXT NOT NULL,
    model              TEXT NOT NULL DEFAULT '',
    thinking           TEXT NOT NULL,
    input_tokens       INTEGER NOT NULL DEFAULT 0,
    output_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    total_tokens       INTEGER NOT NULL DEFAULT 0,
    thinking_tokens    INTEGER NOT NULL DEFAULT 0,
    cost_usd           REAL NOT NULL DEFAULT 0,
    n_calls            INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (hour, agent_type, model, thinking)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_token_usage_team ON token_usage(team_id);
CREATE INDEX IF NOT EXISTS idx_token_usage_time ON token_usage(recorded_at);

-- One row per goal generation attempt (background GoalGenerator), kept for
-- GET /api/goals/metrics and pruned after GoalMetricsRetention.
CREATE TABLE IF NOT EXISTS goal_generations (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id         TEXT NOT NULL,
    agent_name         TEXT NOT NULL DEFAULT '',
    agent_type         TEXT NOT NULL DEFAULT '',
    trigger            TEXT NOT NULL,
    outcome            TEXT NOT NULL,
    goal               TEXT NOT NULL DEFAULT '',
    error              TEXT NOT NULL DEFAULT '',
    duration_ms        INTEGER NOT NULL DEFAULT 0,
    input_tokens       INTEGER NOT NULL DEFAULT 0,
    output_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    cost_usd           REAL NOT NULL DEFAULT 0,
    transcript_bytes   INTEGER NOT NULL DEFAULT 0,
    created_at         TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_goal_generations_time ON goal_generations(created_at);

-- Per-session agent tokens (#3387, identity phase 1). Only the SHA-256 of a token is
-- kept; minting for a session revokes its previous token.
CREATE TABLE IF NOT EXISTS session_tokens (
    token_hash   TEXT PRIMARY KEY,
    session_id   TEXT NOT NULL,
    session_name TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL,
    revoked_at   TEXT
);
CREATE INDEX IF NOT EXISTS idx_session_tokens_session ON session_tokens(session_id);

-- Browser sign-ins (#3949): a sign-in link, the API-key form or the password. Only the
-- SHA-256 of the cookie value is kept; times are unix seconds; expires_at is absolute.
CREATE TABLE IF NOT EXISTS browser_sessions (
    id_hash     TEXT PRIMARY KEY,
    label       TEXT NOT NULL DEFAULT '',
    method      TEXT NOT NULL DEFAULT '',
    persistent  INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    last_seen   INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_browser_sessions_expires ON browser_sessions(expires_at);
CREATE INDEX IF NOT EXISTS idx_goal_generations_session ON goal_generations(session_id, created_at);

CREATE INDEX IF NOT EXISTS idx_git_snap_session ON git_snapshots(session_id);
CREATE INDEX IF NOT EXISTS idx_session_tags_tag_id ON session_tags(tag_id);
CREATE INDEX IF NOT EXISTS idx_folder_tags_tag_id ON folder_tags(tag_id);
CREATE INDEX IF NOT EXISTS idx_session_index_first_ts ON session_index(first_timestamp);
CREATE INDEX IF NOT EXISTS idx_agent_events_session_time ON agent_events(session_id, created_at);
CREATE INDEX IF NOT EXISTS idx_agent_events_session_type_time ON agent_events(session_id, event_type, created_at);
CREATE INDEX IF NOT EXISTS idx_agent_events_agent ON agent_events(agent_name, session_id);
-- An agent's events newest first without sorting all of them (the agent poll, #3846).
CREATE INDEX IF NOT EXISTS idx_agent_events_agent_time ON agent_events(agent_name, created_at);
-- Named by INDEXED BY in the edited-file queries; keep the WHERE in step with them.
-- json_valid keeps a malformed detail_json row out of the index: json_extract
-- would otherwise fail the CREATE (and so startup) and the row's INSERT.
CREATE INDEX IF NOT EXISTS idx_agent_events_edited_files ON agent_events(session_id, json_extract(detail_json, '$.file_path'))
	WHERE event_type = 'tool_use' AND tool_name IN ('Write', 'Edit') AND detail_json IS NOT NULL AND json_valid(detail_json);
CREATE INDEX IF NOT EXISTS idx_git_snap_session_time ON git_snapshots(session_id, recorded_at DESC);
CREATE INDEX IF NOT EXISTS idx_git_snap_agent_time ON git_snapshots(agent_name, recorded_at DESC);
`

// reportForeignKeyOrphans logs rows of an encrypted database that break a foreign key
// (#3454). Until then encrypted databases ran with foreign keys OFF (the driver ignored
// the setting), so cascades didn't apply and orphans may exist. They are kept, never
// a reason to refuse to start; foreign keys are enforced from now on. The check runs on
// its own read-only connection, so it never holds the writer, within a time budget.
func reportForeignKeyOrphans(ctx context.Context, dbPath, key string) {
	ctx, cancel := context.WithTimeout(ctx, fkCheckBudget)
	defer cancel()
	db, err := sql.Open(dbcrypt.EncryptedDriver, dbcrypt.EncryptedDSN(dbPath, key, dbcrypt.DSNOptions{ReadOnly: true}))
	if err != nil {
		slog.Warn("[store] foreign key check skipped", "error", err)
		return
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		slog.Warn("[store] foreign key check skipped", "error", err)
		return
	}
	defer rows.Close()
	byTable := map[string]int{}
	total := 0
	for rows.Next() {
		var table, parent string
		var rowid sql.NullInt64
		var fkid int
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			slog.Warn("[store] foreign key check skipped", "error", err)
			return
		}
		byTable[table+"->"+parent]++
		total++
	}
	if err := rows.Err(); err != nil {
		slog.Warn("[store] foreign key check incomplete", "error", err, "rows_so_far", total)
		return
	}
	if total > 0 {
		slog.Warn("[store] rows that break a foreign key were found and kept; foreign keys are enforced from now on", "rows", total, "by_table", byTable)
	}
}

// fkCheckBudget bounds the startup foreign key check (a test seam).
var fkCheckBudget = 5 * time.Second

// detailJSONFixupKey marks, in store_meta, that agent_events' double-encoded detail_json rows
// (written before makeToolDetail was fixed) were repaired.
const detailJSONFixupKey = "agent_events_detail_json_fixed"

// fixDoubleEncodedDetailJSON repairs those rows once. New rows are never double-encoded, so the
// marker is set only after the update succeeded; a failure is retried at the next start.
func fixDoubleEncodedDetailJSON(ctx context.Context, d *DB) {
	var done int
	if err := d.GetContext(ctx, &done, `SELECT COUNT(*) FROM store_meta WHERE key = ?`, detailJSONFixupKey); err != nil || done > 0 {
		return
	}
	if _, err := d.ExecContext(ctx, `UPDATE agent_events SET detail_json = json_extract(detail_json, '$')
		WHERE detail_json IS NOT NULL AND typeof(json_extract(detail_json, '$')) = 'text'
		AND json_valid(json_extract(detail_json, '$'))`); err != nil {
		slog.Warn("[store] repairing double-encoded event details", "error", err)
		return
	}
	if _, err := d.ExecContext(ctx, `INSERT OR IGNORE INTO store_meta (key, value) VALUES (?, ?)`, detailJSONFixupKey, time.Now().UTC().Format(time.RFC3339)); err != nil {
		slog.Warn("[store] repairing double-encoded event details: write marker", "error", err)
	}
}

// journalSizeLimitParam: the writer's WAL is truncated back to JournalSizeLimit after a
// checkpoint (#4142).
var journalSizeLimitParam = fmt.Sprintf("&_pragma=journal_size_limit(%d)", JournalSizeLimit)

// remoteURLScrubKey marks, in store_meta, that remote URLs recorded before #3558 were cleaned.
// A new key makes the scrub run once more when it learns a new form: "_2" adds scp-style
// user:pass@host:path rows (#3561 (a)).
const remoteURLScrubKey = "remote_url_credentials_scrubbed_2"

// scrubRemoteURLCredentials removes credentials (and any ?query or #fragment) from remote URLs
// recorded before #3558 (https://user:TOKEN@host/...). Rows recorded now are stored without
// them, so this runs once: a store_meta row marks it done, and it is set only when every row
// was cleaned, so a failure is retried at the next start.
func scrubRemoteURLCredentials(ctx context.Context, d *DB) {
	var done int
	if err := d.GetContext(ctx, &done, `SELECT COUNT(*) FROM store_meta WHERE key = ?`, remoteURLScrubKey); err != nil {
		slog.Warn("[store] removing credentials from stored git remote URLs: read marker", "error", err)
		return
	}
	if done > 0 {
		return
	}
	var rows []struct {
		ID  int64  `db:"id"`
		URL string `db:"remote_url"`
	}
	if err := d.SelectContext(ctx, &rows, `SELECT id, remote_url FROM git_snapshots WHERE remote_url LIKE '%://%' OR remote_url LIKE '%@%'`); err != nil {
		slog.Warn("[store] removing credentials from stored git remote URLs: read rows", "error", err)
		return
	}
	n := 0
	for _, r := range rows {
		if clean := gitutil.StripURLCredentials(r.URL); clean != r.URL {
			if _, err := d.ExecContext(ctx, `UPDATE git_snapshots SET remote_url = ? WHERE id = ?`, clean, r.ID); err != nil {
				slog.Warn("[store] removing credentials from stored git remote URLs: update", "id", r.ID, "error", err)
				return
			}
			n++
		}
	}
	if _, err := d.ExecContext(ctx, `INSERT OR IGNORE INTO store_meta (key, value) VALUES (?, ?)`, remoteURLScrubKey, time.Now().UTC().Format(time.RFC3339)); err != nil {
		slog.Warn("[store] removing credentials from stored git remote URLs: write marker", "error", err)
	}
	if n > 0 {
		slog.Info("[store] removed credentials from stored git remote URLs", "rows", n)
	}
}
