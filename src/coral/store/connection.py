"""Database connection manager for the Coral SQLite store."""

from __future__ import annotations

import asyncio
import aiosqlite
from pathlib import Path

from coral.config import DB_BUSY_TIMEOUT_MS, get_data_dir


def get_db_path() -> Path:
    """Return the path to the main Coral database."""
    return get_data_dir() / "sessions.db"


# Kept for backward compatibility — prefer get_db_path() in new code.
DB_DIR = Path.home() / ".coral"
DB_PATH = DB_DIR / "sessions.db"


class DatabaseManager:
    """Manages the shared aiosqlite connection, schema creation, and migrations."""

    def __init__(self, db_path: Path | None = None) -> None:
        db_path = db_path or get_db_path()
        self._db_path = db_path
        self._schema_ensured = False
        self._conn: aiosqlite.Connection | None = None
        self._conn_lock = asyncio.Lock()

    async def _get_conn(self) -> aiosqlite.Connection:
        """Return persistent connection, creating it lazily on first use."""
        if self._conn is not None:
            return self._conn
        async with self._conn_lock:
            if self._conn is not None:
                return self._conn
            self._db_path.parent.mkdir(parents=True, exist_ok=True)
            conn = await aiosqlite.connect(str(self._db_path))
            conn.row_factory = aiosqlite.Row
            await conn.execute("PRAGMA journal_mode=WAL")
            await conn.execute(f"PRAGMA busy_timeout={DB_BUSY_TIMEOUT_MS}")
            await conn.execute("PRAGMA foreign_keys=ON")
            if not self._schema_ensured:
                self._schema_ensured = True
                await self._ensure_schema(conn)
            self._conn = conn
            return conn

    async def close(self) -> None:
        """Close the persistent connection. Call on shutdown."""
        if self._conn is not None:
            await self._conn.close()
            self._conn = None

    async def _ensure_schema(self, conn: aiosqlite.Connection) -> None:
        await conn.executescript("""
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

            -- Job definitions
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

            -- Execution history
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

            -- Per-file change tracking (working tree diff stats)
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
        """)

        # Migrations: add columns that may not exist in older schemas
        _add_columns = [
            ("agent_notes", "session_id", "TEXT"),
            ("agent_tasks", "session_id", "TEXT"),
            ("session_meta", "display_name", "TEXT"),
            ("live_sessions", "resume_from_id", "TEXT"),
            ("live_sessions", "flags", "TEXT"),
            ("live_sessions", "is_job", "INTEGER NOT NULL DEFAULT 0"),
            ("live_sessions", "prompt", "TEXT"),
            ("live_sessions", "board_name", "TEXT"),
            ("live_sessions", "board_server", "TEXT"),
            ("live_sessions", "icon", "TEXT"),
            ("scheduled_jobs", "flags", "TEXT DEFAULT ''"),
            ("scheduled_runs", "trigger_type", "TEXT DEFAULT 'cron'"),
            ("scheduled_runs", "webhook_url", "TEXT"),
            ("scheduled_runs", "display_name", "TEXT"),
        ]
        for table, col, defn in _add_columns:
            try:
                await conn.execute(f"ALTER TABLE {table} ADD COLUMN {col} {defn}")
            except aiosqlite.OperationalError:
                pass  # Column already exists

        # Create agent_live_state if missing (migration)
        await conn.execute("""
            CREATE TABLE IF NOT EXISTS agent_live_state (
                agent_name         TEXT PRIMARY KEY,
                current_session_id TEXT
            )
        """)

        # Migrate git_snapshots UNIQUE constraint from (agent_name, commit_hash)
        # to (session_id, commit_hash) to support multiple sessions in the same directory.
        try:
            cursor = await conn.execute(
                "SELECT sql FROM sqlite_master WHERE type='table' AND name='git_snapshots'"
            )
            row = await cursor.fetchone()
            if row and "UNIQUE(agent_name, commit_hash)" in (row[0] or ""):
                await conn.executescript("""
                    CREATE TABLE IF NOT EXISTS git_snapshots_new (
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
                    INSERT OR IGNORE INTO git_snapshots_new
                        SELECT * FROM git_snapshots;
                    DROP TABLE git_snapshots;
                    ALTER TABLE git_snapshots_new RENAME TO git_snapshots;
                """)
        except Exception:
            pass

        for ddl in [
            "CREATE INDEX IF NOT EXISTS idx_git_snap_session ON git_snapshots(session_id)",
            "CREATE INDEX IF NOT EXISTS idx_session_tags_tag_id ON session_tags(tag_id)",
            "CREATE INDEX IF NOT EXISTS idx_folder_tags_tag_id ON folder_tags(tag_id)",
            "CREATE INDEX IF NOT EXISTS idx_session_index_first_ts ON session_index(first_timestamp)",
            # Performance indexes for frequent queries
            "CREATE INDEX IF NOT EXISTS idx_agent_events_session ON agent_events(session_id)",
            "CREATE INDEX IF NOT EXISTS idx_agent_events_session_type ON agent_events(session_id, event_type)",
            "CREATE INDEX IF NOT EXISTS idx_git_snap_session_time ON git_snapshots(session_id, recorded_at DESC)",
        ]:
            try:
                await conn.execute(ddl)
            except Exception:
                pass

        await conn.commit()
