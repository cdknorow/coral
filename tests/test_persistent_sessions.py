"""Tests for persistent live sessions (resume on restart)."""

import os
import pytest
import pytest_asyncio
from unittest.mock import AsyncMock, patch, MagicMock

from corral.session_store import SessionStore


@pytest_asyncio.fixture
async def store(tmp_path):
    """Create a SessionStore backed by a temp DB and close it after the test."""
    s = SessionStore(db_path=tmp_path / "test.db")
    yield s
    await s.close()


# ── SessionStore: live_sessions table ──────────────────────────────────────


@pytest.mark.asyncio
async def test_live_sessions_table_exists(store):
    """The live_sessions table should be created by schema init."""
    conn = await store._get_conn()
    rows = await (await conn.execute(
        "SELECT name FROM sqlite_master WHERE type='table' AND name='live_sessions'"
    )).fetchall()
    assert len(rows) == 1


@pytest.mark.asyncio
async def test_register_and_get_live_session(store):
    """Registering a session should make it appear in get_all_live_sessions."""
    await store.register_live_session(
        "sid-1", "claude", "worktree_1", "/tmp/worktree_1", "My Agent",
    )
    sessions = await store.get_all_live_sessions()
    assert len(sessions) == 1
    s = sessions[0]
    assert s["session_id"] == "sid-1"
    assert s["agent_type"] == "claude"
    assert s["agent_name"] == "worktree_1"
    assert s["working_dir"] == "/tmp/worktree_1"
    assert s["display_name"] == "My Agent"
    assert s["created_at"] is not None


@pytest.mark.asyncio
async def test_register_multiple_sessions(store):
    """Multiple sessions can be registered and retrieved."""
    await store.register_live_session("sid-1", "claude", "wt1", "/tmp/wt1")
    await store.register_live_session("sid-2", "gemini", "wt2", "/tmp/wt2")
    await store.register_live_session("sid-3", "claude", "wt3", "/tmp/wt3")

    sessions = await store.get_all_live_sessions()
    assert len(sessions) == 3
    ids = {s["session_id"] for s in sessions}
    assert ids == {"sid-1", "sid-2", "sid-3"}


@pytest.mark.asyncio
async def test_unregister_live_session(store):
    """Unregistering a session should remove it from the table."""
    await store.register_live_session("sid-1", "claude", "wt1", "/tmp/wt1")
    await store.register_live_session("sid-2", "claude", "wt2", "/tmp/wt2")

    await store.unregister_live_session("sid-1")

    sessions = await store.get_all_live_sessions()
    assert len(sessions) == 1
    assert sessions[0]["session_id"] == "sid-2"


@pytest.mark.asyncio
async def test_unregister_nonexistent_session(store):
    """Unregistering a session that doesn't exist should not raise."""
    await store.unregister_live_session("nonexistent")  # should not raise
    sessions = await store.get_all_live_sessions()
    assert len(sessions) == 0


@pytest.mark.asyncio
async def test_replace_live_session(store):
    """Replacing a session should remove old and insert new atomically."""
    await store.register_live_session("old-sid", "claude", "wt1", "/tmp/wt1", "My Agent")

    await store.replace_live_session(
        "old-sid", "new-sid", "claude", "wt1", "/tmp/wt1", "My Agent",
    )

    sessions = await store.get_all_live_sessions()
    assert len(sessions) == 1
    assert sessions[0]["session_id"] == "new-sid"
    assert sessions[0]["display_name"] == "My Agent"


@pytest.mark.asyncio
async def test_replace_preserves_other_sessions(store):
    """Replacing one session should not affect others."""
    await store.register_live_session("sid-1", "claude", "wt1", "/tmp/wt1")
    await store.register_live_session("sid-2", "claude", "wt2", "/tmp/wt2")

    await store.replace_live_session("sid-1", "sid-3", "claude", "wt1", "/tmp/wt1")

    sessions = await store.get_all_live_sessions()
    assert len(sessions) == 2
    ids = {s["session_id"] for s in sessions}
    assert ids == {"sid-2", "sid-3"}


@pytest.mark.asyncio
async def test_register_overwrites_on_duplicate(store):
    """Registering with the same session_id should overwrite (INSERT OR REPLACE)."""
    await store.register_live_session("sid-1", "claude", "wt1", "/tmp/wt1", "First")
    await store.register_live_session("sid-1", "claude", "wt1", "/tmp/wt1", "Second")

    sessions = await store.get_all_live_sessions()
    assert len(sessions) == 1
    assert sessions[0]["display_name"] == "Second"


@pytest.mark.asyncio
async def test_get_all_returns_empty_initially(store):
    """An empty table should return an empty list."""
    sessions = await store.get_all_live_sessions()
    assert sessions == []


# ── Resume logic (_resume_persistent_sessions) ────────────────────────────


@pytest.mark.asyncio
async def test_resume_skips_already_running(store, tmp_path):
    """Sessions that already have a matching tmux session should not be relaunched."""
    await store.register_live_session("sid-1", "claude", "wt1", str(tmp_path))

    mock_agents = [{"session_id": "sid-1", "agent_type": "claude", "agent_name": "wt1", "working_directory": str(tmp_path)}]

    with patch("corral.web_server.store", store), \
         patch("corral.session_manager.discover_corral_agents", AsyncMock(return_value=mock_agents)), \
         patch("corral.session_manager.launch_claude_session", AsyncMock()) as mock_launch:
        from corral.web_server import _resume_persistent_sessions
        await _resume_persistent_sessions()

    mock_launch.assert_not_called()
    # Session should still be registered
    sessions = await store.get_all_live_sessions()
    assert len(sessions) == 1


@pytest.mark.asyncio
async def test_resume_relaunches_missing_session(store, tmp_path):
    """Sessions not in tmux should be relaunched and old record cleaned up."""
    work_dir = str(tmp_path)
    await store.register_live_session("old-sid", "claude", "wt1", work_dir, "My Agent")

    launch_result = {
        "session_name": "claude-new-sid",
        "session_id": "new-sid",
        "log_file": "/tmp/claude_corral_new-sid.log",
        "working_dir": work_dir,
        "agent_type": "claude",
    }

    with patch("corral.web_server.store", store), \
         patch("corral.session_manager.discover_corral_agents", AsyncMock(return_value=[])), \
         patch("corral.session_manager.launch_claude_session", AsyncMock(return_value=launch_result)) as mock_launch:
        from corral.web_server import _resume_persistent_sessions
        await _resume_persistent_sessions()

    mock_launch.assert_called_once_with(work_dir, "claude", display_name="My Agent", resume_session_id="old-sid")


@pytest.mark.asyncio
async def test_resume_uses_resume_from_id(store, tmp_path):
    """When resume_from_id is set, it should be used instead of session_id."""
    work_dir = str(tmp_path)
    await store.register_live_session(
        "new-sid", "claude", "wt1", work_dir, "My Agent",
        resume_from_id="original-sid",
    )

    launch_result = {
        "session_name": "claude-fresh-sid",
        "session_id": "fresh-sid",
        "log_file": "/tmp/claude_corral_fresh-sid.log",
        "working_dir": work_dir,
        "agent_type": "claude",
    }

    with patch("corral.web_server.store", store), \
         patch("corral.session_manager.discover_corral_agents", AsyncMock(return_value=[])), \
         patch("corral.session_manager.launch_claude_session", AsyncMock(return_value=launch_result)) as mock_launch:
        from corral.web_server import _resume_persistent_sessions
        await _resume_persistent_sessions()

    # Should use the original-sid (resume_from_id), not new-sid (session_id)
    mock_launch.assert_called_once_with(work_dir, "claude", display_name="My Agent", resume_session_id="original-sid")
    # Old session should be cleaned up (new one registered by launch_claude_session)
    sessions = await store.get_all_live_sessions()
    old_ids = {s["session_id"] for s in sessions}
    assert "old-sid" not in old_ids


@pytest.mark.asyncio
async def test_resume_removes_stale_missing_dir(store, tmp_path):
    """Sessions whose working directory no longer exists should be removed."""
    missing_dir = str(tmp_path / "gone")  # does not exist
    await store.register_live_session("sid-1", "claude", "wt1", missing_dir)

    with patch("corral.web_server.store", store), \
         patch("corral.session_manager.discover_corral_agents", AsyncMock(return_value=[])), \
         patch("corral.session_manager.launch_claude_session", AsyncMock()) as mock_launch:
        from corral.web_server import _resume_persistent_sessions
        await _resume_persistent_sessions()

    mock_launch.assert_not_called()
    sessions = await store.get_all_live_sessions()
    assert len(sessions) == 0


@pytest.mark.asyncio
async def test_resume_handles_launch_failure(store, tmp_path):
    """If relaunching fails, the old session record should be cleaned up."""
    work_dir = str(tmp_path)
    await store.register_live_session("sid-1", "claude", "wt1", work_dir)

    with patch("corral.web_server.store", store), \
         patch("corral.session_manager.discover_corral_agents", AsyncMock(return_value=[])), \
         patch("corral.session_manager.launch_claude_session", AsyncMock(return_value={"error": "tmux not found"})):
        from corral.web_server import _resume_persistent_sessions
        await _resume_persistent_sessions()

    sessions = await store.get_all_live_sessions()
    assert len(sessions) == 0


@pytest.mark.asyncio
async def test_resume_multiple_sessions(store, tmp_path):
    """Multiple missing sessions should all be relaunched."""
    dir1 = tmp_path / "wt1"
    dir2 = tmp_path / "wt2"
    dir1.mkdir()
    dir2.mkdir()

    await store.register_live_session("sid-1", "claude", "wt1", str(dir1))
    await store.register_live_session("sid-2", "gemini", "wt2", str(dir2))

    call_count = 0

    async def mock_launch(working_dir, agent_type, display_name=None, resume_session_id=None):
        nonlocal call_count
        call_count += 1
        return {
            "session_name": f"{agent_type}-new-{call_count}",
            "session_id": f"new-{call_count}",
            "log_file": f"/tmp/{agent_type}_corral_new-{call_count}.log",
            "working_dir": working_dir,
            "agent_type": agent_type,
        }

    with patch("corral.web_server.store", store), \
         patch("corral.session_manager.discover_corral_agents", AsyncMock(return_value=[])), \
         patch("corral.session_manager.launch_claude_session", AsyncMock(side_effect=mock_launch)):
        from corral.web_server import _resume_persistent_sessions
        await _resume_persistent_sessions()

    assert call_count == 2


@pytest.mark.asyncio
async def test_resume_no_registered_sessions(store):
    """With no registered sessions, resume should be a no-op."""
    with patch("corral.web_server.store", store), \
         patch("corral.session_manager.discover_corral_agents", AsyncMock()) as mock_discover:
        from corral.web_server import _resume_persistent_sessions
        await _resume_persistent_sessions()

    # discover_corral_agents should not even be called when table is empty
    mock_discover.assert_not_called()


# ── Name & display_name persistence across restarts ───────────────────────


@pytest.mark.asyncio
async def test_register_stores_resume_from_id(store):
    """resume_from_id should be persisted and returned by get_all_live_sessions."""
    await store.register_live_session(
        "sid-1", "claude", "wt1", "/tmp/wt1", "My Agent",
        resume_from_id="original-sid",
    )
    sessions = await store.get_all_live_sessions()
    assert len(sessions) == 1
    assert sessions[0]["resume_from_id"] == "original-sid"


@pytest.mark.asyncio
async def test_register_without_resume_from_id(store):
    """resume_from_id should be None when not provided."""
    await store.register_live_session("sid-1", "claude", "wt1", "/tmp/wt1")
    sessions = await store.get_all_live_sessions()
    assert sessions[0]["resume_from_id"] is None


@pytest.mark.asyncio
async def test_replace_preserves_display_name(store):
    """replace_live_session should preserve display_name when provided."""
    await store.register_live_session("old-sid", "claude", "wt1", "/tmp/wt1", "My Agent")
    await store.replace_live_session(
        "old-sid", "new-sid", "claude", "wt1", "/tmp/wt1",
        display_name="My Agent", resume_from_id="original-sid",
    )

    sessions = await store.get_all_live_sessions()
    assert len(sessions) == 1
    assert sessions[0]["session_id"] == "new-sid"
    assert sessions[0]["display_name"] == "My Agent"
    assert sessions[0]["resume_from_id"] == "original-sid"


@pytest.mark.asyncio
async def test_replace_without_display_name_sets_null(store):
    """replace_live_session without display_name should store NULL."""
    await store.register_live_session("old-sid", "claude", "wt1", "/tmp/wt1", "My Agent")
    await store.replace_live_session(
        "old-sid", "new-sid", "claude", "wt1", "/tmp/wt1",
    )

    sessions = await store.get_all_live_sessions()
    assert sessions[0]["display_name"] is None


@pytest.mark.asyncio
async def test_resume_preserves_display_name_across_multiple_restarts(store, tmp_path):
    """display_name should survive multiple Corral restart cycles.

    Simulates: initial launch → Corral restart → Corral restart again.
    Each cycle the old session is unregistered and a new one registered
    by launch_claude_session. The display_name must carry through.
    """
    work_dir = str(tmp_path)
    call_log = []

    async def mock_launch(working_dir, agent_type, display_name=None, resume_session_id=None):
        """Simulate launch_claude_session: register a new session in the store."""
        new_sid = f"sid-cycle-{len(call_log) + 1}"
        call_log.append({
            "display_name": display_name,
            "resume_session_id": resume_session_id,
            "new_sid": new_sid,
        })
        # Mimic what launch_claude_session does internally
        await store.register_live_session(
            new_sid, agent_type, "wt1", working_dir,
            display_name=display_name,
            resume_from_id=resume_session_id,
        )
        return {
            "session_name": f"{agent_type}-{new_sid}",
            "session_id": new_sid,
            "log_file": f"/tmp/{agent_type}_corral_{new_sid}.log",
            "working_dir": working_dir,
            "agent_type": agent_type,
        }

    # Cycle 0: Initial launch (simulated by direct registration)
    await store.register_live_session("sid-original", "claude", "wt1", work_dir, "My Custom Name")

    # Cycle 1: First Corral restart
    with patch("corral.web_server.store", store), \
         patch("corral.session_manager.discover_corral_agents", AsyncMock(return_value=[])), \
         patch("corral.session_manager.launch_claude_session", AsyncMock(side_effect=mock_launch)):
        from corral.web_server import _resume_persistent_sessions
        await _resume_persistent_sessions()

    assert call_log[0]["display_name"] == "My Custom Name"
    assert call_log[0]["resume_session_id"] == "sid-original"

    # Verify the new record has display_name and resume_from_id
    sessions = await store.get_all_live_sessions()
    assert len(sessions) == 1
    s = sessions[0]
    assert s["session_id"] == "sid-cycle-1"
    assert s["display_name"] == "My Custom Name"
    assert s["resume_from_id"] == "sid-original"

    # Cycle 2: Second Corral restart — should still use original session ID
    with patch("corral.web_server.store", store), \
         patch("corral.session_manager.discover_corral_agents", AsyncMock(return_value=[])), \
         patch("corral.session_manager.launch_claude_session", AsyncMock(side_effect=mock_launch)):
        await _resume_persistent_sessions()

    assert call_log[1]["display_name"] == "My Custom Name"
    assert call_log[1]["resume_session_id"] == "sid-original"  # Still the original!

    sessions = await store.get_all_live_sessions()
    assert len(sessions) == 1
    s = sessions[0]
    assert s["session_id"] == "sid-cycle-2"
    assert s["display_name"] == "My Custom Name"
    assert s["resume_from_id"] == "sid-original"


@pytest.mark.asyncio
async def test_resume_preserves_agent_name_across_restarts(store, tmp_path):
    """agent_name (folder name) should be consistent across restart cycles."""
    work_dir = str(tmp_path / "my_worktree")
    os.makedirs(work_dir)

    await store.register_live_session("sid-1", "claude", "my_worktree", work_dir)

    async def mock_launch(working_dir, agent_type, display_name=None, resume_session_id=None):
        folder = os.path.basename(working_dir)
        await store.register_live_session(
            "sid-2", agent_type, folder, working_dir,
            display_name=display_name, resume_from_id=resume_session_id,
        )
        return {
            "session_name": f"{agent_type}-sid-2",
            "session_id": "sid-2",
            "log_file": f"/tmp/{agent_type}_corral_sid-2.log",
            "working_dir": working_dir,
            "agent_type": agent_type,
        }

    with patch("corral.web_server.store", store), \
         patch("corral.session_manager.discover_corral_agents", AsyncMock(return_value=[])), \
         patch("corral.session_manager.launch_claude_session", AsyncMock(side_effect=mock_launch)):
        from corral.web_server import _resume_persistent_sessions
        await _resume_persistent_sessions()

    sessions = await store.get_all_live_sessions()
    assert len(sessions) == 1
    assert sessions[0]["agent_name"] == "my_worktree"


@pytest.mark.asyncio
async def test_resume_without_display_name_passes_none(store, tmp_path):
    """Sessions without a display_name should pass None through to launch."""
    work_dir = str(tmp_path)
    await store.register_live_session("sid-1", "claude", "wt1", work_dir)

    launch_result = {
        "session_name": "claude-sid-2",
        "session_id": "sid-2",
        "log_file": "/tmp/claude_corral_sid-2.log",
        "working_dir": work_dir,
        "agent_type": "claude",
    }

    with patch("corral.web_server.store", store), \
         patch("corral.session_manager.discover_corral_agents", AsyncMock(return_value=[])), \
         patch("corral.session_manager.launch_claude_session", AsyncMock(return_value=launch_result)) as mock_launch:
        from corral.web_server import _resume_persistent_sessions
        await _resume_persistent_sessions()

    mock_launch.assert_called_once_with(
        work_dir, "claude", display_name=None, resume_session_id="sid-1",
    )


@pytest.mark.asyncio
async def test_update_live_session_display_name(store):
    """update_live_session_display_name should update the live_sessions record."""
    await store.register_live_session("sid-1", "claude", "wt1", "/tmp/wt1")
    sessions = await store.get_all_live_sessions()
    assert sessions[0]["display_name"] is None

    await store.update_live_session_display_name("sid-1", "New Name")
    sessions = await store.get_all_live_sessions()
    assert sessions[0]["display_name"] == "New Name"


@pytest.mark.asyncio
async def test_display_name_set_via_ui_persists_on_resume(store, tmp_path):
    """display_name set via the dashboard UI should survive Corral restarts.

    Simulates: launch (no name) → user sets name via UI → Corral restart.
    The display_name in live_sessions must be updated by the UI endpoint
    so it's available when _resume_persistent_sessions reads it back.
    """
    work_dir = str(tmp_path)

    # Initial launch without display_name
    await store.register_live_session("sid-1", "claude", "wt1", work_dir)

    # User sets display_name via UI (writes to both session_meta and live_sessions)
    await store.set_display_name("sid-1", "Dashboard Name")
    await store.update_live_session_display_name("sid-1", "Dashboard Name")

    # Corral restart: read back from live_sessions
    sessions = await store.get_all_live_sessions()
    assert sessions[0]["display_name"] == "Dashboard Name"

    # Simulate _resume_persistent_sessions picking up the name
    launch_result = {
        "session_name": "claude-sid-2",
        "session_id": "sid-2",
        "log_file": "/tmp/claude_corral_sid-2.log",
        "working_dir": work_dir,
        "agent_type": "claude",
    }

    with patch("corral.web_server.store", store), \
         patch("corral.session_manager.discover_corral_agents", AsyncMock(return_value=[])), \
         patch("corral.session_manager.launch_claude_session", AsyncMock(return_value=launch_result)) as mock_launch:
        from corral.web_server import _resume_persistent_sessions
        await _resume_persistent_sessions()

    mock_launch.assert_called_once_with(
        work_dir, "claude", display_name="Dashboard Name", resume_session_id="sid-1",
    )
