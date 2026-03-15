"""Standalone SQLite store for the inter-agent message board.

Manages its own database (~/.coral/messageboard.db) with no dependencies
on Coral internals.
"""

from __future__ import annotations

from datetime import datetime, timezone
from pathlib import Path
from typing import Any

import aiosqlite

DB_DIR = Path.home() / ".coral"
DB_PATH = DB_DIR / "messageboard.db"


class MessageBoardStore:
    """Self-contained store for the message board feature."""

    def __init__(self, db_path: Path = DB_PATH) -> None:
        self._db_path = db_path
        self._conn: aiosqlite.Connection | None = None
        self._schema_ensured = False

    async def _get_conn(self) -> aiosqlite.Connection:
        if self._conn is not None:
            return self._conn
        self._db_path.parent.mkdir(parents=True, exist_ok=True)
        conn = await aiosqlite.connect(str(self._db_path))
        conn.row_factory = aiosqlite.Row
        await conn.execute("PRAGMA journal_mode=WAL")
        if not self._schema_ensured:
            self._schema_ensured = True
            await self._ensure_schema(conn)
        self._conn = conn
        return conn

    async def close(self) -> None:
        if self._conn is not None:
            await self._conn.close()
            self._conn = None

    async def _ensure_schema(self, conn: aiosqlite.Connection) -> None:
        await conn.executescript("""
            CREATE TABLE IF NOT EXISTS board_subscribers (
                id            INTEGER PRIMARY KEY AUTOINCREMENT,
                project       TEXT NOT NULL,
                session_id    TEXT NOT NULL,
                job_title     TEXT NOT NULL,
                webhook_url   TEXT,
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

            CREATE INDEX IF NOT EXISTS idx_board_messages_project
                ON board_messages(project, id);
        """)

    # ── Subscribers ──────────────────────────────────────────────────────

    async def subscribe(
        self,
        project: str,
        session_id: str,
        job_title: str,
        webhook_url: str | None = None,
    ) -> dict[str, Any]:
        conn = await self._get_conn()
        now = datetime.now(timezone.utc).isoformat()
        await conn.execute(
            """INSERT INTO board_subscribers (project, session_id, job_title, webhook_url, subscribed_at)
               VALUES (?, ?, ?, ?, ?)
               ON CONFLICT(project, session_id)
               DO UPDATE SET job_title = excluded.job_title,
                             webhook_url = excluded.webhook_url""",
            (project, session_id, job_title, webhook_url, now),
        )
        await conn.commit()
        row = await conn.execute_fetchall(
            "SELECT * FROM board_subscribers WHERE project = ? AND session_id = ?",
            (project, session_id),
        )
        return dict(row[0])

    async def unsubscribe(self, project: str, session_id: str) -> bool:
        conn = await self._get_conn()
        cursor = await conn.execute(
            "DELETE FROM board_subscribers WHERE project = ? AND session_id = ?",
            (project, session_id),
        )
        await conn.commit()
        return cursor.rowcount > 0

    async def list_subscribers(self, project: str) -> list[dict[str, Any]]:
        conn = await self._get_conn()
        rows = await conn.execute_fetchall(
            "SELECT * FROM board_subscribers WHERE project = ? ORDER BY subscribed_at",
            (project,),
        )
        return [dict(r) for r in rows]

    async def get_subscription(self, session_id: str) -> dict[str, Any] | None:
        """Return the active subscription for a session, or None."""
        conn = await self._get_conn()
        rows = await conn.execute_fetchall(
            "SELECT * FROM board_subscribers WHERE session_id = ? LIMIT 1",
            (session_id,),
        )
        return dict(rows[0]) if rows else None

    async def get_all_subscriptions(self) -> dict[str, dict[str, Any]]:
        """Return all active subscriptions keyed by session_id."""
        conn = await self._get_conn()
        rows = await conn.execute_fetchall("SELECT * FROM board_subscribers")
        return {row["session_id"]: dict(row) for row in rows}

    # ── Messages ─────────────────────────────────────────────────────────

    async def post_message(
        self, project: str, session_id: str, content: str
    ) -> dict[str, Any]:
        conn = await self._get_conn()
        now = datetime.now(timezone.utc).isoformat()
        cursor = await conn.execute(
            "INSERT INTO board_messages (project, session_id, content, created_at) VALUES (?, ?, ?, ?)",
            (project, session_id, content, now),
        )
        msg_id = cursor.lastrowid
        await conn.commit()

        return {"id": msg_id, "project": project, "session_id": session_id, "content": content, "created_at": now}

    async def read_messages(
        self, project: str, session_id: str, limit: int = 50
    ) -> list[dict[str, Any]]:
        conn = await self._get_conn()

        # Get subscriber's cursor
        sub_rows = await conn.execute_fetchall(
            "SELECT last_read_id FROM board_subscribers WHERE project = ? AND session_id = ?",
            (project, session_id),
        )
        if not sub_rows:
            return []
        last_read_id = sub_rows[0]["last_read_id"]

        # Fetch new messages from others
        rows = await conn.execute_fetchall(
            """SELECT m.id, m.project, m.session_id, m.content, m.created_at,
                      COALESCE(s.job_title, 'Unknown') as job_title
               FROM board_messages m
               LEFT JOIN board_subscribers s ON m.project = s.project AND m.session_id = s.session_id
               WHERE m.project = ? AND m.id > ? AND m.session_id != ?
               ORDER BY m.id ASC LIMIT ?""",
            (project, last_read_id, session_id, limit),
        )
        messages = [dict(r) for r in rows]

        # Advance cursor to the max message ID in the project (even if no messages returned)
        max_rows = await conn.execute_fetchall(
            "SELECT COALESCE(MAX(id), 0) as max_id FROM board_messages WHERE project = ?",
            (project,),
        )
        new_cursor = max_rows[0]["max_id"]
        if new_cursor > last_read_id:
            await conn.execute(
                "UPDATE board_subscribers SET last_read_id = ? WHERE project = ? AND session_id = ?",
                (new_cursor, project, session_id),
            )
            await conn.commit()

        return messages

    async def list_messages(
        self, project: str, limit: int = 200
    ) -> list[dict[str, Any]]:
        """Return recent messages for a project (no cursor, no side effects)."""
        conn = await self._get_conn()
        rows = await conn.execute_fetchall(
            """SELECT m.id, m.project, m.session_id, m.content, m.created_at,
                      COALESCE(s.job_title, 'Unknown') as job_title
               FROM board_messages m
               LEFT JOIN board_subscribers s ON m.project = s.project AND m.session_id = s.session_id
               WHERE m.project = ?
               ORDER BY m.id ASC LIMIT ?""",
            (project, limit),
        )
        return [dict(r) for r in rows]

    async def check_unread(self, project: str, session_id: str) -> int:
        """Return count of unread messages that mention this agent.

        Only counts messages containing ``@notify-all``, ``@<session_id>``,
        or ``@<job_title>`` (case-insensitive).  Messages without a relevant
        mention are silently skipped so agents aren't spammed.
        """
        conn = await self._get_conn()
        sub_rows = await conn.execute_fetchall(
            "SELECT last_read_id, job_title FROM board_subscribers WHERE project = ? AND session_id = ?",
            (project, session_id),
        )
        if not sub_rows:
            return 0
        last_read_id = sub_rows[0]["last_read_id"]
        job_title = sub_rows[0]["job_title"]

        # Build mention patterns: @notify-all (and variants), @<session_id>, @<job_title>
        patterns = [
            "%@notify-all%",
            "%@notify_all%",
            "%@notifyall%",
            "%@all%",
            f"%@{session_id}%",
        ]
        if job_title:
            patterns.append(f"%@{job_title}%")

        where_clauses = " OR ".join("content LIKE ? COLLATE NOCASE" for _ in patterns)
        count_rows = await conn.execute_fetchall(
            f"""SELECT COUNT(*) as cnt FROM board_messages
                WHERE project = ? AND id > ? AND session_id != ?
                AND ({where_clauses})""",
            (project, last_read_id, session_id, *patterns),
        )
        return count_rows[0]["cnt"]

    # ── Webhooks ─────────────────────────────────────────────────────────

    async def get_webhook_targets(
        self, project: str, exclude_session_id: str
    ) -> list[dict[str, Any]]:
        conn = await self._get_conn()
        rows = await conn.execute_fetchall(
            """SELECT session_id, webhook_url FROM board_subscribers
               WHERE project = ? AND session_id != ? AND webhook_url IS NOT NULL AND webhook_url != ''""",
            (project, exclude_session_id),
        )
        return [dict(r) for r in rows]

    # ── Projects ─────────────────────────────────────────────────────────

    async def list_projects(self) -> list[dict[str, Any]]:
        conn = await self._get_conn()
        rows = await conn.execute_fetchall(
            """SELECT project,
                      (SELECT COUNT(*) FROM board_subscribers s WHERE s.project = p.project) as subscriber_count,
                      (SELECT COUNT(*) FROM board_messages m WHERE m.project = p.project) as message_count
               FROM (
                   SELECT DISTINCT project FROM board_subscribers
                   UNION
                   SELECT DISTINCT project FROM board_messages
               ) p
               ORDER BY project"""
        )
        return [dict(r) for r in rows]

    async def delete_project(self, project: str) -> bool:
        conn = await self._get_conn()
        await conn.execute("DELETE FROM board_messages WHERE project = ?", (project,))
        await conn.execute("DELETE FROM board_subscribers WHERE project = ?", (project,))
        await conn.commit()
        return True
