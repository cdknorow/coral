"""Tests for SessionStore persistent connection behavior."""

import pytest
import pytest_asyncio
import aiosqlite
from pathlib import Path

from corral.store import CorralStore as SessionStore


@pytest_asyncio.fixture
async def store(tmp_path):
    """Create a SessionStore backed by a temp DB and close it after the test."""
    s = SessionStore(db_path=tmp_path / "test.db")
    yield s
    await s.close()


# ── Connection lifecycle ────────────────────────────────────────────────────


@pytest.mark.asyncio
async def test_connection_is_lazy(tmp_path):
    """Connection should not exist until the first operation."""
    store = SessionStore(db_path=tmp_path / "test.db")
    assert store._conn is None
    await store.close()


@pytest.mark.asyncio
async def test_connection_created_on_first_call(store):
    """First _get_conn() should create and cache a connection."""
    assert store._conn is None
    conn = await store._get_conn()
    assert conn is not None
    assert store._conn is conn


@pytest.mark.asyncio
async def test_connection_reused_across_calls(store):
    """Subsequent _get_conn() calls should return the same connection object."""
    conn1 = await store._get_conn()
    conn2 = await store._get_conn()
    assert conn1 is conn2


@pytest.mark.asyncio
async def test_connection_reused_across_operations(store):
    """Different store methods should share the same underlying connection."""
    await store.save_session_notes("sess-1", "note")
    conn_after_write = store._conn

    await store.get_session_notes("sess-1")
    conn_after_read = store._conn

    assert conn_after_write is conn_after_read


@pytest.mark.asyncio
async def test_close_sets_conn_to_none(store):
    """close() should close the connection and reset the reference."""
    await store._get_conn()
    assert store._conn is not None

    await store.close()
    assert store._conn is None


@pytest.mark.asyncio
async def test_close_is_idempotent(store):
    """Calling close() multiple times should not raise."""
    await store._get_conn()
    await store.close()
    await store.close()  # should not raise


@pytest.mark.asyncio
async def test_reconnects_after_close(store):
    """A new connection should be created if the store is used after close()."""
    conn1 = await store._get_conn()
    await store.close()

    conn2 = await store._get_conn()
    assert conn2 is not None
    assert conn2 is not conn1


# ── Schema and WAL mode ────────────────────────────────────────────────────


@pytest.mark.asyncio
async def test_wal_mode_enabled(store):
    """The persistent connection should use WAL journal mode."""
    conn = await store._get_conn()
    row = await (await conn.execute("PRAGMA journal_mode")).fetchone()
    assert row[0] == "wal"


@pytest.mark.asyncio
async def test_schema_created(store):
    """Schema tables should exist after first connection."""
    conn = await store._get_conn()
    rows = await (await conn.execute(
        "SELECT name FROM sqlite_master WHERE type='table' ORDER BY name"
    )).fetchall()
    table_names = {r[0] for r in rows}
    assert "session_meta" in table_names
    assert "tags" in table_names
    assert "session_index" in table_names
    assert "agent_tasks" in table_names


# ── CRUD operations through persistent connection ───────────────────────────


@pytest.mark.asyncio
async def test_notes_roundtrip(store):
    """Save and retrieve session notes."""
    await store.save_session_notes("sess-1", "# Hello\nSome notes")
    result = await store.get_session_notes("sess-1")
    assert result["notes_md"] == "# Hello\nSome notes"
    assert result["is_user_edited"] is True


@pytest.mark.asyncio
async def test_notes_missing_session(store):
    """Getting notes for a nonexistent session returns defaults."""
    result = await store.get_session_notes("nonexistent")
    assert result["notes_md"] == ""
    assert result["is_user_edited"] is False
    assert result["updated_at"] is None


@pytest.mark.asyncio
async def test_tags_crud(store):
    """Create, list, and delete tags."""
    tag = await store.create_tag("bug", "#ff0000")
    assert tag["name"] == "bug"
    assert tag["id"] is not None

    tags = await store.list_tags()
    assert len(tags) == 1
    assert tags[0]["name"] == "bug"

    await store.delete_tag(tag["id"])
    tags = await store.list_tags()
    assert len(tags) == 0


@pytest.mark.asyncio
async def test_session_index_upsert(store):
    """Upsert into session_index and query back."""
    await store.upsert_session_index(
        session_id="sess-1",
        source_type="claude",
        source_file="/tmp/test.jsonl",
        first_timestamp="2024-01-01T00:00:00Z",
        last_timestamp="2024-01-01T01:00:00Z",
        message_count=10,
        display_summary="Test session",
        file_mtime=1700000000.0,
    )
    mtimes = await store.get_indexed_mtimes()
    assert "/tmp/test.jsonl" in mtimes


@pytest.mark.asyncio
async def test_many_sequential_operations_reuse_connection(store):
    """Many sequential operations should all reuse the same connection."""
    for i in range(20):
        await store.save_session_notes(f"sess-{i}", f"note {i}")
    for i in range(20):
        result = await store.get_session_notes(f"sess-{i}")
        assert result["notes_md"] == f"note {i}"

    # Still the same single connection
    conn = await store._get_conn()
    assert store._conn is conn
