"""Standalone SQLite store for the inter-agent message board.

Manages its own database (~/.coral/messageboard.db) with no dependencies
on Coral internals.
"""

from __future__ import annotations

from datetime import datetime, timezone
from pathlib import Path
from typing import Any

import aiosqlite

from coral.config import get_data_dir


def get_db_path() -> Path:
    return get_data_dir() / "messageboard.db"


# Kept for backward compatibility
DB_DIR = Path.home() / ".coral"
DB_PATH = DB_DIR / "messageboard.db"


class MessageBoardStore:
    """Self-contained store for the message board feature."""

    def __init__(self, db_path: Path | None = None) -> None:
        db_path = db_path or get_db_path()
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
        from coral.config import DB_BUSY_TIMEOUT_MS
        await conn.execute(f"PRAGMA busy_timeout={DB_BUSY_TIMEOUT_MS}")
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
        # Migration: add origin_server column to track remote subscribers
        try:
            await conn.execute("ALTER TABLE board_subscribers ADD COLUMN origin_server TEXT")
        except Exception:
            pass  # Column already exists

        # Migration: add receive_mode column for notification control
        try:
            await conn.execute(
                "ALTER TABLE board_subscribers ADD COLUMN receive_mode TEXT NOT NULL DEFAULT 'mentions'"
            )
        except Exception:
            pass  # Column already exists

        # Migration: add target_group_id column for Subgent API compatibility
        try:
            await conn.execute("ALTER TABLE board_messages ADD COLUMN target_group_id TEXT")
        except Exception:
            pass  # Column already exists

        # Board groups table for group-based receive modes
        await conn.executescript("""
            CREATE TABLE IF NOT EXISTS board_groups (
                id         INTEGER PRIMARY KEY AUTOINCREMENT,
                project    TEXT NOT NULL,
                group_id   TEXT NOT NULL,
                session_id TEXT NOT NULL,
                UNIQUE(project, group_id, session_id)
            );
            CREATE INDEX IF NOT EXISTS idx_board_groups_project_group
                ON board_groups(project, group_id);
        """)

    # ── Subscribers ──────────────────────────────────────────────────────

    async def subscribe(
        self,
        project: str,
        session_id: str,
        job_title: str,
        webhook_url: str | None = None,
        origin_server: str | None = None,
        receive_mode: str = "mentions",
    ) -> dict[str, Any]:
        conn = await self._get_conn()
        now = datetime.now(timezone.utc).isoformat()
        await conn.execute(
            """INSERT INTO board_subscribers (project, session_id, job_title, webhook_url, origin_server, receive_mode, subscribed_at)
               VALUES (?, ?, ?, ?, ?, ?, ?)
               ON CONFLICT(project, session_id)
               DO UPDATE SET job_title = excluded.job_title,
                             webhook_url = excluded.webhook_url,
                             origin_server = excluded.origin_server,
                             receive_mode = excluded.receive_mode""",
            (project, session_id, job_title, webhook_url, origin_server, receive_mode, now),
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

    async def transfer_subscription(self, project: str, old_session_id: str, new_session_id: str) -> None:
        """Transfer a subscription from one session to another, preserving last_read_id.

        Used during session resume when the session_id changes but the agent
        should not re-read old messages.
        """
        conn = await self._get_conn()
        rows = await conn.execute_fetchall(
            "SELECT last_read_id, job_title, webhook_url, origin_server, receive_mode FROM board_subscribers WHERE project = ? AND session_id = ?",
            (project, old_session_id),
        )
        if not rows:
            return
        old = dict(rows[0])
        now = datetime.now(timezone.utc).isoformat()
        await conn.execute(
            """INSERT INTO board_subscribers (project, session_id, job_title, webhook_url, origin_server, receive_mode, last_read_id, subscribed_at)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?)
               ON CONFLICT(project, session_id)
               DO UPDATE SET job_title = excluded.job_title,
                             webhook_url = excluded.webhook_url,
                             origin_server = excluded.origin_server,
                             receive_mode = excluded.receive_mode,
                             last_read_id = excluded.last_read_id""",
            (project, new_session_id, old["job_title"], old["webhook_url"],
             old["origin_server"], old["receive_mode"], old["last_read_id"], now),
        )
        # Remove old subscription
        await conn.execute(
            "DELETE FROM board_subscribers WHERE project = ? AND session_id = ?",
            (project, old_session_id),
        )
        await conn.commit()

    async def get_all_subscriptions(self) -> dict[str, dict[str, Any]]:
        """Return all active subscriptions keyed by session_id."""
        conn = await self._get_conn()
        rows = await conn.execute_fetchall("SELECT * FROM board_subscribers")
        return {row["session_id"]: dict(row) for row in rows}

    # ── Messages ─────────────────────────────────────────────────────────

    async def post_message(
        self, project: str, session_id: str, content: str,
        target_group_id: str | None = None,
    ) -> dict[str, Any]:
        conn = await self._get_conn()
        now = datetime.now(timezone.utc).isoformat()
        cursor = await conn.execute(
            "INSERT INTO board_messages (project, session_id, content, created_at, target_group_id) VALUES (?, ?, ?, ?, ?)",
            (project, session_id, content, now, target_group_id),
        )
        msg_id = cursor.lastrowid
        await conn.commit()

        return {
            "id": msg_id, "project": project, "session_id": session_id,
            "content": content, "created_at": now, "target_group_id": target_group_id,
        }

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
                      m.target_group_id,
                      COALESCE(s.job_title, 'Unknown') as job_title
               FROM board_messages m
               LEFT JOIN board_subscribers s ON m.project = s.project AND m.session_id = s.session_id
               WHERE m.project = ? AND m.id > ? AND m.session_id != ?
               ORDER BY m.id ASC LIMIT ?""",
            (project, last_read_id, session_id, limit),
        )
        messages = [dict(r) for r in rows]

        # Advance cursor past returned messages and past our own messages
        # (so we don't re-scan our own posts), but never past unseen messages
        # from other agents.
        candidates = [last_read_id]
        if messages:
            candidates.append(max(m["id"] for m in messages))
        # Skip past our own messages so they don't block the cursor
        own_max_rows = await conn.execute_fetchall(
            "SELECT COALESCE(MAX(id), 0) as max_id FROM board_messages WHERE project = ? AND session_id = ?",
            (project, session_id),
        )
        candidates.append(own_max_rows[0]["max_id"])
        new_cursor = max(candidates)
        if new_cursor > last_read_id:
            await conn.execute(
                "UPDATE board_subscribers SET last_read_id = ? WHERE project = ? AND session_id = ?",
                (new_cursor, project, session_id),
            )
            await conn.commit()

        return messages

    async def list_messages(
        self, project: str, limit: int = 200, offset: int = 0,
        before_id: int | None = None,
    ) -> list[dict[str, Any]]:
        """Return recent messages for a project (no cursor, no side effects)."""
        conn = await self._get_conn()
        where = "m.project = ?"
        params: list[Any] = [project]
        if before_id is not None:
            where += " AND m.id < ?"
            params.append(before_id)
        params.extend([limit, offset])
        rows = await conn.execute_fetchall(
            f"""SELECT m.id, m.project, m.session_id, m.content, m.created_at,
                      m.target_group_id,
                      COALESCE(s.job_title, 'Unknown') as job_title
               FROM board_messages m
               LEFT JOIN board_subscribers s ON m.project = s.project AND m.session_id = s.session_id
               WHERE {where}
               ORDER BY m.id ASC LIMIT ? OFFSET ?""",
            tuple(params),
        )
        return [dict(r) for r in rows]

    async def count_messages(self, project: str) -> int:
        """Return total message count for a project."""
        conn = await self._get_conn()
        rows = await conn.execute_fetchall(
            "SELECT COUNT(*) as cnt FROM board_messages WHERE project = ?",
            (project,),
        )
        return rows[0]["cnt"] if rows else 0

    async def check_unread(self, project: str, session_id: str) -> int:
        """Return count of unread messages based on the subscriber's receive_mode.

        Modes:
        - ``none``     → always 0
        - ``all``      → all unread messages from others
        - ``mentions`` → only messages with @notify-all, @<session_id>, or @<job_title>
        - anything else → treat as group-id, count only messages from group members
        """
        conn = await self._get_conn()
        sub_rows = await conn.execute_fetchall(
            "SELECT last_read_id, job_title, receive_mode FROM board_subscribers WHERE project = ? AND session_id = ?",
            (project, session_id),
        )
        if not sub_rows:
            return 0
        last_read_id = sub_rows[0]["last_read_id"]
        job_title = sub_rows[0]["job_title"]
        receive_mode = sub_rows[0]["receive_mode"] or "mentions"

        if receive_mode == "none":
            return 0

        if receive_mode == "all":
            count_rows = await conn.execute_fetchall(
                """SELECT COUNT(*) as cnt FROM board_messages
                    WHERE project = ? AND id > ? AND session_id != ?""",
                (project, last_read_id, session_id),
            )
            return count_rows[0]["cnt"]

        if receive_mode == "mentions":
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

        # Group-based mode: count messages from group members only
        group_rows = await conn.execute_fetchall(
            "SELECT session_id FROM board_groups WHERE project = ? AND group_id = ?",
            (project, receive_mode),
        )
        group_member_ids = {r["session_id"] for r in group_rows}
        if not group_member_ids:
            return 0
        placeholders = ",".join("?" for _ in group_member_ids)
        count_rows = await conn.execute_fetchall(
            f"""SELECT COUNT(*) as cnt FROM board_messages
                WHERE project = ? AND id > ? AND session_id != ?
                AND session_id IN ({placeholders})""",
            (project, last_read_id, session_id, *group_member_ids),
        )
        return count_rows[0]["cnt"]

    async def get_all_unread_counts(self) -> dict[str, int]:
        """Return unread counts for ALL subscribers in one pass.

        Returns a dict keyed by session_id with unread counts.
        Respects each subscriber's receive_mode.
        """
        conn = await self._get_conn()

        # Fetch all subscribers
        sub_rows = await conn.execute_fetchall(
            "SELECT project, session_id, job_title, last_read_id, receive_mode FROM board_subscribers"
        )
        if not sub_rows:
            return {}

        # Group subscribers by project for efficient querying
        by_project: dict[str, list[dict]] = {}
        for row in sub_rows:
            r = dict(row)
            by_project.setdefault(r["project"], []).append(r)

        # Pre-load all group memberships for group-based modes
        group_rows = await conn.execute_fetchall(
            "SELECT project, group_id, session_id FROM board_groups"
        )
        # groups_by_project_group[(project, group_id)] = {session_id, ...}
        groups_by_key: dict[tuple[str, str], set[str]] = {}
        for gr in group_rows:
            key = (gr["project"], gr["group_id"])
            groups_by_key.setdefault(key, set()).add(gr["session_id"])

        result: dict[str, int] = {}

        for project, subs in by_project.items():
            # Find the minimum last_read_id to fetch all potentially unread messages
            min_cursor = min(s["last_read_id"] for s in subs)

            # Fetch all messages after the minimum cursor in one query
            msg_rows = await conn.execute_fetchall(
                "SELECT id, session_id, content FROM board_messages "
                "WHERE project = ? AND id > ? ORDER BY id",
                (project, min_cursor),
            )
            if not msg_rows:
                for s in subs:
                    result[s["session_id"]] = 0
                continue

            messages = [dict(r) for r in msg_rows]

            # Count per subscriber based on their receive_mode
            for sub in subs:
                last_read = sub["last_read_id"]
                sid = sub["session_id"]
                job_title = sub["job_title"] or ""
                receive_mode = sub["receive_mode"] or "mentions"

                if receive_mode == "none":
                    result[sid] = 0
                    continue

                count = 0
                if receive_mode == "all":
                    for msg in messages:
                        if msg["id"] <= last_read:
                            continue
                        if msg["session_id"] == sid:
                            continue
                        count += 1
                elif receive_mode == "mentions":
                    mention_terms = [
                        "@notify-all", "@notify_all", "@notifyall", "@all",
                        f"@{sid}",
                    ]
                    if job_title:
                        mention_terms.append(f"@{job_title}")

                    for msg in messages:
                        if msg["id"] <= last_read:
                            continue
                        if msg["session_id"] == sid:
                            continue
                        content_lower = msg["content"].lower()
                        if any(term.lower() in content_lower for term in mention_terms):
                            count += 1
                else:
                    # Group-based mode
                    group_members = groups_by_key.get((project, receive_mode), set())
                    for msg in messages:
                        if msg["id"] <= last_read:
                            continue
                        if msg["session_id"] == sid:
                            continue
                        if msg["session_id"] in group_members:
                            count += 1

                result[sid] = count

        return result

    # ── Groups ─────────────────────────────────────────────────────────

    async def add_to_group(self, project: str, group_id: str, session_id: str) -> None:
        """Add a session to a board group."""
        conn = await self._get_conn()
        await conn.execute(
            """INSERT INTO board_groups (project, group_id, session_id)
               VALUES (?, ?, ?)
               ON CONFLICT(project, group_id, session_id) DO NOTHING""",
            (project, group_id, session_id),
        )
        await conn.commit()

    async def remove_from_group(self, project: str, group_id: str, session_id: str) -> bool:
        """Remove a session from a board group. Returns True if removed."""
        conn = await self._get_conn()
        cursor = await conn.execute(
            "DELETE FROM board_groups WHERE project = ? AND group_id = ? AND session_id = ?",
            (project, group_id, session_id),
        )
        await conn.commit()
        return cursor.rowcount > 0

    async def list_group_members(self, project: str, group_id: str) -> list[str]:
        """Return session_ids in a group."""
        conn = await self._get_conn()
        rows = await conn.execute_fetchall(
            "SELECT session_id FROM board_groups WHERE project = ? AND group_id = ? ORDER BY session_id",
            (project, group_id),
        )
        return [r["session_id"] for r in rows]

    async def list_groups(self, project: str) -> list[dict[str, Any]]:
        """Return all groups for a project with member counts."""
        conn = await self._get_conn()
        rows = await conn.execute_fetchall(
            """SELECT group_id, COUNT(*) as member_count
               FROM board_groups WHERE project = ?
               GROUP BY group_id ORDER BY group_id""",
            (project,),
        )
        return [dict(r) for r in rows]

    # ── Delete individual message ───────────────────────────────────────

    async def delete_message(self, message_id: int) -> bool:
        """Delete a single message by ID. Returns True if a row was removed."""
        conn = await self._get_conn()
        cursor = await conn.execute(
            "DELETE FROM board_messages WHERE id = ?", (message_id,)
        )
        await conn.commit()
        return cursor.rowcount > 0

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

    # ── Search ─────────────────────────────────────────────────────────

    async def search_messages(self, query: str) -> list[str]:
        """Return project names that have messages matching the query (LIKE search)."""
        conn = await self._get_conn()
        rows = await conn.execute_fetchall(
            "SELECT DISTINCT project FROM board_messages WHERE content LIKE ? COLLATE NOCASE",
            (f"%{query}%",),
        )
        return [r["project"] for r in rows]

    async def list_projects_enriched(self) -> list[dict[str, Any]]:
        """Return board projects with timestamps, subscriber info, and message counts."""
        conn = await self._get_conn()
        rows = await conn.execute_fetchall(
            """SELECT
                   p.project,
                   (SELECT COUNT(*) FROM board_subscribers s WHERE s.project = p.project) as subscriber_count,
                   (SELECT COUNT(*) FROM board_messages m WHERE m.project = p.project) as message_count,
                   (SELECT MIN(created_at) FROM board_messages m WHERE m.project = p.project) as first_message_at,
                   (SELECT MAX(created_at) FROM board_messages m WHERE m.project = p.project) as last_message_at,
                   (SELECT GROUP_CONCAT(s.job_title, ', ')
                    FROM board_subscribers s WHERE s.project = p.project
                    ORDER BY s.subscribed_at LIMIT 5) as participant_names
               FROM (
                   SELECT DISTINCT project FROM board_subscribers
                   UNION
                   SELECT DISTINCT project FROM board_messages
               ) p
               ORDER BY (SELECT MAX(created_at) FROM board_messages m WHERE m.project = p.project) DESC"""
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
        await conn.execute("DELETE FROM board_groups WHERE project = ?", (project,))
        await conn.commit()
        return True
