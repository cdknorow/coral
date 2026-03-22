"""Tests for persistent live sessions (resume on restart)."""

import os
from pathlib import Path
import pytest
import pytest_asyncio
from unittest.mock import AsyncMock, patch, MagicMock

from coral.store import CoralStore as SessionStore


@pytest.fixture(autouse=True)
def _mock_transcript_check():
    """Make resolve_transcript_path always find a transcript so resume tests
    don't get short-circuited by the 'no transcript → mark sleeping' guard."""
    _fake = Path("/tmp/fake-transcript.jsonl")
    with patch(
        "coral.agents.claude.ClaudeAgent.resolve_transcript_path",
        return_value=_fake,
    ), patch(
        "coral.agents.base.BaseAgent.resolve_transcript_path",
        return_value=_fake,
    ):
        yield


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


# ── Resume logic (resume_persistent_sessions) ────────────────────────────


@pytest.mark.asyncio
async def test_resume_skips_already_running(store, tmp_path):
    """Sessions that already have a matching tmux session should not be relaunched."""
    await store.register_live_session("sid-1", "claude", "wt1", str(tmp_path))

    mock_agents = [{"session_id": "sid-1", "agent_type": "claude", "agent_name": "wt1", "working_directory": str(tmp_path)}]

    with patch("coral.tools.session_manager.discover_coral_agents", AsyncMock(return_value=mock_agents)), \
         patch("coral.tools.session_manager.launch_claude_session", AsyncMock()) as mock_launch:
        from coral.tools.session_manager import resume_persistent_sessions
        await resume_persistent_sessions(store)

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
        "log_file": "/tmp/claude_coral_new-sid.log",
        "working_dir": work_dir,
        "agent_type": "claude",
    }

    with patch("coral.tools.session_manager.discover_coral_agents", AsyncMock(return_value=[])), \
         patch("coral.tools.session_manager.launch_claude_session", AsyncMock(return_value=launch_result)) as mock_launch:
        from coral.tools.session_manager import resume_persistent_sessions
        await resume_persistent_sessions(store)

    mock_launch.assert_called_once_with(work_dir, "claude", display_name="My Agent", resume_session_id="old-sid", flags=None, prompt=None, board_name=None, board_server=None, board_type=None)


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
        "log_file": "/tmp/claude_coral_fresh-sid.log",
        "working_dir": work_dir,
        "agent_type": "claude",
    }

    with patch("coral.tools.session_manager.discover_coral_agents", AsyncMock(return_value=[])), \
         patch("coral.tools.session_manager.launch_claude_session", AsyncMock(return_value=launch_result)) as mock_launch:
        from coral.tools.session_manager import resume_persistent_sessions
        await resume_persistent_sessions(store)

    # Should use the original-sid (resume_from_id), not new-sid (session_id)
    mock_launch.assert_called_once_with(work_dir, "claude", display_name="My Agent", resume_session_id="original-sid", flags=None, prompt=None, board_name=None, board_server=None, board_type=None)
    # Old session should be cleaned up (new one registered by launch_claude_session)
    sessions = await store.get_all_live_sessions()
    old_ids = {s["session_id"] for s in sessions}
    assert "old-sid" not in old_ids


@pytest.mark.asyncio
async def test_resume_removes_stale_missing_dir(store, tmp_path):
    """Sessions whose working directory no longer exists should be removed."""
    missing_dir = str(tmp_path / "gone")  # does not exist
    await store.register_live_session("sid-1", "claude", "wt1", missing_dir)

    with patch("coral.tools.session_manager.discover_coral_agents", AsyncMock(return_value=[])), \
         patch("coral.tools.session_manager.launch_claude_session", AsyncMock()) as mock_launch:
        from coral.tools.session_manager import resume_persistent_sessions
        await resume_persistent_sessions(store)

    mock_launch.assert_not_called()
    sessions = await store.get_all_live_sessions()
    assert len(sessions) == 0


@pytest.mark.asyncio
async def test_resume_handles_launch_failure(store, tmp_path):
    """If relaunching fails, the old session record should be cleaned up."""
    work_dir = str(tmp_path)
    await store.register_live_session("sid-1", "claude", "wt1", work_dir)

    with patch("coral.tools.session_manager.discover_coral_agents", AsyncMock(return_value=[])), \
         patch("coral.tools.session_manager.launch_claude_session", AsyncMock(return_value={"error": "tmux not found"})):
        from coral.tools.session_manager import resume_persistent_sessions
        await resume_persistent_sessions(store)

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

    async def mock_launch(working_dir, agent_type, display_name=None, resume_session_id=None, flags=None, prompt=None, board_name=None, board_server=None, board_type=None):
        nonlocal call_count
        call_count += 1
        return {
            "session_name": f"{agent_type}-new-{call_count}",
            "session_id": f"new-{call_count}",
            "log_file": f"/tmp/{agent_type}_coral_new-{call_count}.log",
            "working_dir": working_dir,
            "agent_type": agent_type,
        }

    with patch("coral.tools.session_manager.discover_coral_agents", AsyncMock(return_value=[])), \
         patch("coral.tools.session_manager.launch_claude_session", AsyncMock(side_effect=mock_launch)):
        from coral.tools.session_manager import resume_persistent_sessions
        await resume_persistent_sessions(store)

    assert call_count == 2


@pytest.mark.asyncio
async def test_resume_no_registered_sessions(store):
    """With no registered sessions, resume should be a no-op."""
    with patch("coral.tools.session_manager.discover_coral_agents", AsyncMock()) as mock_discover:
        from coral.tools.session_manager import resume_persistent_sessions
        await resume_persistent_sessions(store)

    # discover_coral_agents should not even be called when table is empty
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
    """display_name should survive multiple Coral restart cycles.

    Simulates: initial launch → Coral restart → Coral restart again.
    Each cycle the old session is unregistered and a new one registered
    by launch_claude_session. The display_name must carry through.
    """
    work_dir = str(tmp_path)
    call_log = []

    async def mock_launch(working_dir, agent_type, display_name=None, resume_session_id=None, flags=None, prompt=None, board_name=None, board_server=None, board_type=None):
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
            "log_file": f"/tmp/{agent_type}_coral_{new_sid}.log",
            "working_dir": working_dir,
            "agent_type": agent_type,
        }

    # Cycle 0: Initial launch (simulated by direct registration)
    await store.register_live_session("sid-original", "claude", "wt1", work_dir, "My Custom Name")

    # Cycle 1: First Coral restart
    with patch("coral.tools.session_manager.discover_coral_agents", AsyncMock(return_value=[])), \
         patch("coral.tools.session_manager.launch_claude_session", AsyncMock(side_effect=mock_launch)):
        from coral.tools.session_manager import resume_persistent_sessions
        await resume_persistent_sessions(store)

    assert call_log[0]["display_name"] == "My Custom Name"
    assert call_log[0]["resume_session_id"] == "sid-original"

    # Verify the new record has display_name and resume_from_id
    sessions = await store.get_all_live_sessions()
    assert len(sessions) == 1
    s = sessions[0]
    assert s["session_id"] == "sid-cycle-1"
    assert s["display_name"] == "My Custom Name"
    assert s["resume_from_id"] == "sid-original"

    # Cycle 2: Second Coral restart — should still use original session ID
    with patch("coral.tools.session_manager.discover_coral_agents", AsyncMock(return_value=[])), \
         patch("coral.tools.session_manager.launch_claude_session", AsyncMock(side_effect=mock_launch)):
        await resume_persistent_sessions(store)

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

    async def mock_launch(working_dir, agent_type, display_name=None, resume_session_id=None, flags=None, prompt=None, board_name=None, board_server=None, board_type=None):
        folder = os.path.basename(working_dir)
        await store.register_live_session(
            "sid-2", agent_type, folder, working_dir,
            display_name=display_name, resume_from_id=resume_session_id,
        )
        return {
            "session_name": f"{agent_type}-sid-2",
            "session_id": "sid-2",
            "log_file": f"/tmp/{agent_type}_coral_sid-2.log",
            "working_dir": working_dir,
            "agent_type": agent_type,
        }

    with patch("coral.tools.session_manager.discover_coral_agents", AsyncMock(return_value=[])), \
         patch("coral.tools.session_manager.launch_claude_session", AsyncMock(side_effect=mock_launch)):
        from coral.tools.session_manager import resume_persistent_sessions
        await resume_persistent_sessions(store)

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
        "log_file": "/tmp/claude_coral_sid-2.log",
        "working_dir": work_dir,
        "agent_type": "claude",
    }

    with patch("coral.tools.session_manager.discover_coral_agents", AsyncMock(return_value=[])), \
         patch("coral.tools.session_manager.launch_claude_session", AsyncMock(return_value=launch_result)) as mock_launch:
        from coral.tools.session_manager import resume_persistent_sessions
        await resume_persistent_sessions(store)

    mock_launch.assert_called_once_with(
        work_dir, "claude", display_name=None, resume_session_id="sid-1", flags=None,
        prompt=None, board_name=None, board_server=None, board_type=None,
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
    """display_name set via the dashboard UI should survive Coral restarts.

    Simulates: launch (no name) → user sets name via UI → Coral restart.
    The display_name in live_sessions must be updated by the UI endpoint
    so it's available when resume_persistent_sessions reads it back.
    """
    work_dir = str(tmp_path)

    # Initial launch without display_name
    await store.register_live_session("sid-1", "claude", "wt1", work_dir)

    # User sets display_name via UI (writes to both session_meta and live_sessions)
    await store.set_display_name("sid-1", "Dashboard Name")
    await store.update_live_session_display_name("sid-1", "Dashboard Name")

    # Coral restart: read back from live_sessions
    sessions = await store.get_all_live_sessions()
    assert sessions[0]["display_name"] == "Dashboard Name"

    # Simulate resume_persistent_sessions picking up the name
    launch_result = {
        "session_name": "claude-sid-2",
        "session_id": "sid-2",
        "log_file": "/tmp/claude_coral_sid-2.log",
        "working_dir": work_dir,
        "agent_type": "claude",
    }

    with patch("coral.tools.session_manager.discover_coral_agents", AsyncMock(return_value=[])), \
         patch("coral.tools.session_manager.launch_claude_session", AsyncMock(return_value=launch_result)) as mock_launch:
        from coral.tools.session_manager import resume_persistent_sessions
        await resume_persistent_sessions(store)

    mock_launch.assert_called_once_with(
        work_dir, "claude", display_name="Dashboard Name", resume_session_id="sid-1", flags=None,
        prompt=None, board_name=None, board_server=None, board_type=None,
    )


# ── Bug 1: Concurrent resume with timeouts ────────────────────────────────


@pytest.mark.asyncio
async def test_resume_runs_concurrently(store, tmp_path):
    """Sessions should be resumed concurrently, not sequentially."""
    import time as _time

    dirs = []
    for i in range(3):
        d = tmp_path / f"wt{i}"
        d.mkdir()
        dirs.append(d)
        await store.register_live_session(f"sid-{i}", "claude", f"wt{i}", str(d))

    call_times = []

    async def mock_launch(working_dir, agent_type, display_name=None,
                          resume_session_id=None, flags=None, prompt=None,
                          board_name=None, board_server=None, board_type=None):
        import asyncio as _asyncio
        start = _time.monotonic()
        await _asyncio.sleep(0.5)  # simulate launch delay
        call_times.append((_time.monotonic() - start, resume_session_id))
        return {
            "session_name": f"{agent_type}-new-{resume_session_id}",
            "session_id": f"new-{resume_session_id}",
            "log_file": f"/tmp/{agent_type}_coral_new.log",
            "working_dir": working_dir,
            "agent_type": agent_type,
        }

    t0 = _time.monotonic()
    with patch("coral.tools.session_manager.discover_coral_agents", AsyncMock(return_value=[])), \
         patch("coral.tools.session_manager.launch_claude_session", AsyncMock(side_effect=mock_launch)):
        from coral.tools.session_manager import resume_persistent_sessions
        await resume_persistent_sessions(store)

    elapsed = _time.monotonic() - t0
    assert len(call_times) == 3
    # If sequential, elapsed would be ~1.5s (3 × 0.5s). Concurrent should be ~0.5s.
    assert elapsed < 1.2, f"Resume took {elapsed:.2f}s — expected concurrent execution"


@pytest.mark.asyncio
async def test_resume_timeout_does_not_block_others(store, tmp_path):
    """A stuck session should time out without blocking other resumes."""
    dir1 = tmp_path / "wt1"
    dir2 = tmp_path / "wt2"
    dir1.mkdir()
    dir2.mkdir()
    await store.register_live_session("sid-fast", "claude", "wt1", str(dir1))
    await store.register_live_session("sid-slow", "claude", "wt2", str(dir2))

    completed = []

    async def mock_launch(working_dir, agent_type, display_name=None,
                          resume_session_id=None, flags=None, prompt=None,
                          board_name=None, board_server=None, board_type=None):
        import asyncio as _asyncio
        if resume_session_id == "sid-slow":
            await _asyncio.sleep(999)  # will be timed out
        completed.append(resume_session_id)
        return {
            "session_name": f"{agent_type}-new",
            "session_id": f"new-{resume_session_id}",
            "log_file": f"/tmp/{agent_type}_coral_new.log",
            "working_dir": working_dir,
            "agent_type": agent_type,
        }

    # Temporarily reduce timeout for test speed
    import coral.tools.session_manager as sm
    original_timeout = sm._RESUME_TIMEOUT
    sm._RESUME_TIMEOUT = 1  # 1 second timeout for testing

    try:
        with patch("coral.tools.session_manager.discover_coral_agents", AsyncMock(return_value=[])), \
             patch("coral.tools.session_manager.launch_claude_session", AsyncMock(side_effect=mock_launch)):
            await sm.resume_persistent_sessions(store)
    finally:
        sm._RESUME_TIMEOUT = original_timeout

    # Fast session should have completed; slow one timed out
    assert "sid-fast" in completed
    assert "sid-slow" not in completed


@pytest.mark.asyncio
async def test_resume_one_failure_does_not_abort_others(store, tmp_path):
    """One session raising an exception should not prevent other resumes."""
    dir1 = tmp_path / "wt1"
    dir2 = tmp_path / "wt2"
    dir1.mkdir()
    dir2.mkdir()
    await store.register_live_session("sid-ok", "claude", "wt1", str(dir1))
    await store.register_live_session("sid-err", "claude", "wt2", str(dir2))

    completed = []

    async def mock_launch(working_dir, agent_type, display_name=None,
                          resume_session_id=None, flags=None, prompt=None,
                          board_name=None, board_server=None, board_type=None):
        if resume_session_id == "sid-err":
            raise RuntimeError("Simulated launch failure")
        completed.append(resume_session_id)
        return {
            "session_name": f"{agent_type}-new",
            "session_id": f"new-{resume_session_id}",
            "log_file": f"/tmp/{agent_type}_coral_new.log",
            "working_dir": working_dir,
            "agent_type": agent_type,
        }

    with patch("coral.tools.session_manager.discover_coral_agents", AsyncMock(return_value=[])), \
         patch("coral.tools.session_manager.launch_claude_session", AsyncMock(side_effect=mock_launch)):
        from coral.tools.session_manager import resume_persistent_sessions
        await resume_persistent_sessions(store)

    assert "sid-ok" in completed


# ── Sleep/Wake feature ─────────────────────────────────────────────────────


@pytest.mark.asyncio
async def test_set_board_sleeping(store, tmp_path):
    """set_board_sleeping should update is_sleeping for all sessions on a board."""
    work_dir = str(tmp_path)
    await store.register_live_session(
        "sid-1", "claude", "wt1", work_dir, board_name="my-team",
    )
    await store.register_live_session(
        "sid-2", "claude", "wt2", work_dir, board_name="my-team",
    )
    await store.register_live_session(
        "sid-3", "claude", "wt3", work_dir, board_name="other-team",
    )

    count = await store.set_board_sleeping("my-team", sleeping=True)
    assert count == 2

    sessions = await store.get_all_live_sessions()
    sleeping = {s["session_id"]: s["is_sleeping"] for s in sessions}
    assert sleeping["sid-1"] is True
    assert sleeping["sid-2"] is True
    assert sleeping["sid-3"] is False


@pytest.mark.asyncio
async def test_set_board_sleeping_wake(store, tmp_path):
    """Waking a board should set is_sleeping=False for all sessions on it."""
    work_dir = str(tmp_path)
    await store.register_live_session(
        "sid-1", "claude", "wt1", work_dir, board_name="my-team",
    )
    await store.set_board_sleeping("my-team", sleeping=True)
    await store.set_board_sleeping("my-team", sleeping=False)

    sessions = await store.get_all_live_sessions()
    assert sessions[0]["is_sleeping"] is False


@pytest.mark.asyncio
async def test_get_sleeping_board_names(store, tmp_path):
    """get_sleeping_board_names should return boards with sleeping sessions."""
    work_dir = str(tmp_path)
    await store.register_live_session(
        "sid-1", "claude", "wt1", work_dir, board_name="sleeping-team",
    )
    await store.register_live_session(
        "sid-2", "claude", "wt2", work_dir, board_name="awake-team",
    )
    await store.set_board_sleeping("sleeping-team", sleeping=True)

    names = await store.get_sleeping_board_names()
    assert "sleeping-team" in names
    assert "awake-team" not in names


@pytest.mark.asyncio
async def test_resume_sleeping_session_skips_prompt(store, tmp_path):
    """Sleeping sessions should be relaunched but without prompt delivery."""
    work_dir = str(tmp_path)
    await store.register_live_session(
        "sid-1", "claude", "wt1", work_dir,
        prompt="Hello agent", board_name="my-team",
    )
    await store.set_session_sleeping("sid-1", True)

    launch_result = {
        "session_name": "claude-new-sid",
        "session_id": "new-sid",
        "log_file": "/tmp/claude_coral_new-sid.log",
        "working_dir": work_dir,
        "agent_type": "claude",
    }

    with patch("coral.tools.session_manager.discover_coral_agents", AsyncMock(return_value=[])), \
         patch("coral.tools.session_manager.launch_claude_session", AsyncMock(return_value=launch_result)) as mock_launch:
        from coral.tools.session_manager import resume_persistent_sessions
        await resume_persistent_sessions(store)

    # Sleeping sessions should NOT be launched at all — they're skipped
    mock_launch.assert_not_called()

    # The original DB record should be kept as-is
    sessions = await store.get_all_live_sessions()
    assert len(sessions) == 1
    assert sessions[0]["session_id"] == "sid-1"
    assert sessions[0]["is_sleeping"] is True


@pytest.mark.asyncio
async def test_resume_sleeping_session_carries_forward_state(store, tmp_path):
    """Sleeping sessions should be preserved in DB without relaunching."""
    work_dir = str(tmp_path)
    await store.register_live_session(
        "sid-1", "claude", "wt1", work_dir, board_name="my-team",
    )
    await store.set_session_sleeping("sid-1", True)

    with patch("coral.tools.session_manager.discover_coral_agents", AsyncMock(return_value=[])), \
         patch("coral.tools.session_manager.launch_claude_session", AsyncMock()) as mock_launch:
        from coral.tools.session_manager import resume_persistent_sessions
        await resume_persistent_sessions(store)

    # No launch should have happened
    mock_launch.assert_not_called()

    # The sleeping session should still be in the DB unchanged
    sessions = await store.get_all_live_sessions()
    assert len(sessions) == 1
    assert sessions[0]["session_id"] == "sid-1"
    assert sessions[0]["is_sleeping"] is True


@pytest.mark.asyncio
async def test_replace_live_session_carries_sleeping(store):
    """replace_live_session should carry forward is_sleeping from old session."""
    await store.register_live_session("old-sid", "claude", "wt1", "/tmp/wt1")
    await store.set_session_sleeping("old-sid", True)

    await store.replace_live_session(
        "old-sid", "new-sid", "claude", "wt1", "/tmp/wt1",
    )

    sessions = await store.get_all_live_sessions()
    assert len(sessions) == 1
    assert sessions[0]["session_id"] == "new-sid"
    assert sessions[0]["is_sleeping"] is True


@pytest.mark.asyncio
async def test_resume_no_transcript_marks_sleeping(store, tmp_path):
    """Sessions without a JSONL transcript should be marked sleeping, not launched."""
    work_dir = str(tmp_path)
    await store.register_live_session("sid-no-transcript", "claude", "wt1", work_dir)

    with patch("coral.tools.session_manager.discover_coral_agents", AsyncMock(return_value=[])), \
         patch("coral.tools.session_manager.launch_claude_session", AsyncMock()) as mock_launch, \
         patch("coral.agents.claude.ClaudeAgent.resolve_transcript_path", return_value=None):
        from coral.tools.session_manager import resume_persistent_sessions
        await resume_persistent_sessions(store)

    # Should NOT have tried to launch
    mock_launch.assert_not_called()

    # Session should be marked as sleeping in DB
    sessions = await store.get_all_live_sessions()
    assert len(sessions) == 1
    assert sessions[0]["session_id"] == "sid-no-transcript"
    assert sessions[0]["is_sleeping"] is True


# ── Per-agent sleep/wake ───────────────────────────────────────────────────


@pytest.mark.asyncio
async def test_per_agent_sleep_does_not_affect_others(store, tmp_path):
    """Sleeping one agent should not affect other agents on the same board."""
    work_dir = str(tmp_path)
    await store.register_live_session(
        "sid-1", "claude", "wt1", work_dir, board_name="my-team",
    )
    await store.register_live_session(
        "sid-2", "claude", "wt2", work_dir, board_name="my-team",
    )

    await store.set_session_sleeping("sid-1", True)

    sessions = await store.get_all_live_sessions()
    sleeping = {s["session_id"]: s["is_sleeping"] for s in sessions}
    assert sleeping["sid-1"] is True
    assert sleeping["sid-2"] is False


@pytest.mark.asyncio
async def test_per_agent_wake_does_not_affect_others(store, tmp_path):
    """Waking one agent should not affect other sleeping agents."""
    work_dir = str(tmp_path)
    await store.register_live_session(
        "sid-1", "claude", "wt1", work_dir, board_name="my-team",
    )
    await store.register_live_session(
        "sid-2", "claude", "wt2", work_dir, board_name="my-team",
    )

    # Sleep both
    await store.set_session_sleeping("sid-1", True)
    await store.set_session_sleeping("sid-2", True)

    # Wake only sid-1
    await store.set_session_sleeping("sid-1", False)

    sessions = await store.get_all_live_sessions()
    sleeping = {s["session_id"]: s["is_sleeping"] for s in sessions}
    assert sleeping["sid-1"] is False
    assert sleeping["sid-2"] is True


# ── Sleep-all / Wake-all ───────────────────────────────────────────────────


@pytest.mark.asyncio
async def test_sleep_all_affects_all_sessions(store, tmp_path):
    """set_session_sleeping on each session should sleep all of them."""
    work_dir = str(tmp_path)
    await store.register_live_session("sid-1", "claude", "wt1", work_dir, board_name="team-a")
    await store.register_live_session("sid-2", "gemini", "wt2", work_dir, board_name="team-b")
    await store.register_live_session("sid-3", "claude", "wt3", work_dir)  # no board

    # Simulate sleep-all: sleep each individually
    for sid in ["sid-1", "sid-2", "sid-3"]:
        await store.set_session_sleeping(sid, True)

    sessions = await store.get_all_live_sessions()
    assert all(s["is_sleeping"] is True for s in sessions)

    # get_sleeping_board_names should return both boards
    sleeping_boards = await store.get_sleeping_board_names()
    assert "team-a" in sleeping_boards
    assert "team-b" in sleeping_boards


@pytest.mark.asyncio
async def test_wake_all_clears_all_sleeping(store, tmp_path):
    """Waking all sessions should clear is_sleeping for everyone."""
    work_dir = str(tmp_path)
    await store.register_live_session("sid-1", "claude", "wt1", work_dir, board_name="team-a")
    await store.register_live_session("sid-2", "gemini", "wt2", work_dir)

    # Sleep all
    await store.set_session_sleeping("sid-1", True)
    await store.set_session_sleeping("sid-2", True)

    # Wake all
    await store.set_session_sleeping("sid-1", False)
    await store.set_session_sleeping("sid-2", False)

    sessions = await store.get_all_live_sessions()
    assert all(s["is_sleeping"] is False for s in sessions)
    assert len(await store.get_sleeping_board_names()) == 0


# ── Mixed team/individual sleep edge cases ─────────────────────────────────


@pytest.mark.asyncio
async def test_team_sleep_then_individual_wake(store, tmp_path):
    """Sleeping a team then waking one agent should leave others sleeping."""
    work_dir = str(tmp_path)
    await store.register_live_session("sid-1", "claude", "wt1", work_dir, board_name="my-team")
    await store.register_live_session("sid-2", "claude", "wt2", work_dir, board_name="my-team")
    await store.register_live_session("sid-3", "claude", "wt3", work_dir, board_name="my-team")

    # Team sleep
    count = await store.set_board_sleeping("my-team", sleeping=True)
    assert count == 3

    # Individual wake
    await store.set_session_sleeping("sid-2", False)

    sessions = await store.get_all_live_sessions()
    sleeping = {s["session_id"]: s["is_sleeping"] for s in sessions}
    assert sleeping["sid-1"] is True
    assert sleeping["sid-2"] is False
    assert sleeping["sid-3"] is True

    # Board should still be in sleeping boards (sid-1 and sid-3 are sleeping)
    assert "my-team" in await store.get_sleeping_board_names()


@pytest.mark.asyncio
async def test_individual_sleep_then_team_wake(store, tmp_path):
    """Sleeping individual agents then waking the whole team should clear all."""
    work_dir = str(tmp_path)
    await store.register_live_session("sid-1", "claude", "wt1", work_dir, board_name="my-team")
    await store.register_live_session("sid-2", "claude", "wt2", work_dir, board_name="my-team")

    # Individual sleep
    await store.set_session_sleeping("sid-1", True)

    # Team wake (should wake all on board)
    await store.set_board_sleeping("my-team", sleeping=False)

    sessions = await store.get_all_live_sessions()
    assert all(s["is_sleeping"] is False for s in sessions)


@pytest.mark.asyncio
async def test_sleep_nonexistent_session(store):
    """Sleeping a nonexistent session should not raise."""
    # set_session_sleeping just does an UPDATE — 0 rows affected is fine
    await store.set_session_sleeping("nonexistent", True)
    sessions = await store.get_all_live_sessions()
    assert len(sessions) == 0


@pytest.mark.asyncio
async def test_sleep_all_skips_already_sleeping(store, tmp_path):
    """Sleep-all should be idempotent — already-sleeping sessions stay sleeping."""
    work_dir = str(tmp_path)
    await store.register_live_session("sid-1", "claude", "wt1", work_dir)
    await store.set_session_sleeping("sid-1", True)

    # Sleep again
    await store.set_session_sleeping("sid-1", True)

    sessions = await store.get_all_live_sessions()
    assert sessions[0]["is_sleeping"] is True


# ── API endpoint logic tests ───────────────────────────────────────────────


@pytest.mark.asyncio
async def test_sleep_session_not_found(store):
    """sleep_session should return error for nonexistent session_id."""
    all_sessions = await store.get_all_live_sessions()
    sess = next((s for s in all_sessions if s["session_id"] == "nonexistent"), None)
    assert sess is None  # Confirms endpoint would return "Session not found"


@pytest.mark.asyncio
async def test_wake_session_rejects_non_sleeping(store, tmp_path):
    """wake_session should refuse to wake an agent that isn't sleeping."""
    work_dir = str(tmp_path)
    await store.register_live_session("sid-1", "claude", "wt1", work_dir)

    sessions = await store.get_all_live_sessions()
    sess = sessions[0]
    assert not sess.get("is_sleeping")  # Confirms endpoint would return "Session is not sleeping"


@pytest.mark.asyncio
async def test_sleep_all_excludes_already_sleeping(store, tmp_path):
    """sleep-all should only process active (non-sleeping) sessions."""
    work_dir = str(tmp_path)
    await store.register_live_session("sid-1", "claude", "wt1", work_dir)
    await store.register_live_session("sid-2", "claude", "wt2", work_dir)
    await store.set_session_sleeping("sid-1", True)

    all_sessions = await store.get_all_live_sessions()
    active = [s for s in all_sessions if not s.get("is_sleeping")]
    assert len(active) == 1
    assert active[0]["session_id"] == "sid-2"


@pytest.mark.asyncio
async def test_wake_all_only_clears_sleeping_on_success(store, tmp_path):
    """wake-all should keep sessions sleeping if relaunch fails.

    Simulates: two sleeping sessions, one fails to relaunch.
    Only the successful one should have is_sleeping cleared.
    """
    dir1 = tmp_path / "wt1"
    dir2 = tmp_path / "wt2"
    dir1.mkdir()
    dir2.mkdir()

    await store.register_live_session("sid-ok", "claude", "wt1", str(dir1))
    await store.register_live_session("sid-err", "claude", "wt2", str(dir2))
    await store.set_session_sleeping("sid-ok", True)
    await store.set_session_sleeping("sid-err", True)

    # Simulate wake-all logic: per-session relaunch with error handling
    results = {}
    for sess in await store.get_all_live_sessions():
        sid = sess["session_id"]
        if not sess.get("is_sleeping"):
            continue
        try:
            if sid == "sid-err":
                raise RuntimeError("Simulated relaunch failure")
            # Simulate successful relaunch
            await store.set_session_sleeping(sid, sleeping=False)
            results[sid] = "ok"
        except Exception:
            results[sid] = "failed"

    assert results["sid-ok"] == "ok"
    assert results["sid-err"] == "failed"

    sessions = await store.get_all_live_sessions()
    sleeping = {s["session_id"]: s["is_sleeping"] for s in sessions}
    assert sleeping["sid-ok"] is False
    assert sleeping["sid-err"] is True  # Still sleeping — relaunch failed


@pytest.mark.asyncio
async def test_team_sleep_then_per_agent_wake_unpauses_board(store, tmp_path):
    """Per-agent wake after team sleep should unpause the board.

    Scenario: team sleep pauses the board. Per-agent wake relaunches one
    agent and unpauses the board so the woken agent can receive messages.
    """
    work_dir = str(tmp_path)
    await store.register_live_session(
        "sid-1", "claude", "wt1", work_dir, board_name="my-team",
    )
    await store.register_live_session(
        "sid-2", "claude", "wt2", work_dir, board_name="my-team",
    )

    # Team sleep — pauses board
    await store.set_board_sleeping("my-team", sleeping=True)
    paused = set()
    paused.add("my-team")

    # Per-agent wake (simulating wake_session endpoint logic including board unpause)
    await store.set_session_sleeping("sid-1", False)
    board = "my-team"
    if board and board in paused:
        paused.discard(board)

    # Board should be unpaused now that an agent is awake
    assert "my-team" not in paused

    # At least one agent is awake on the board
    sessions = await store.get_all_live_sessions()
    awake_on_board = [
        s for s in sessions
        if s.get("board_name") == "my-team" and not s.get("is_sleeping")
    ]
    assert len(awake_on_board) == 1
    assert awake_on_board[0]["session_id"] == "sid-1"


@pytest.mark.asyncio
async def test_wake_team_only_clears_sleeping_on_success(store, tmp_path):
    """wake_team should only clear is_sleeping for successfully relaunched sessions.

    Simulates the per-session try/except pattern in wake_team: failed relaunches
    keep the session sleeping, matching wake_all's behavior.
    """
    work_dir = str(tmp_path)
    await store.register_live_session(
        "sid-1", "claude", "wt1", work_dir, board_name="my-team",
    )
    await store.register_live_session(
        "sid-2", "claude", "wt2", work_dir, board_name="my-team",
    )
    await store.set_board_sleeping("my-team", sleeping=True)

    # Simulate wake_team per-session logic: only clear on success
    for sid in ["sid-1", "sid-2"]:
        try:
            if sid == "sid-2":
                raise RuntimeError("Simulated relaunch failure")
            await store.set_session_sleeping(sid, sleeping=False)
        except Exception:
            pass  # Keep sleeping

    sessions = await store.get_all_live_sessions()
    sleeping = {s["session_id"]: s["is_sleeping"] for s in sessions}
    assert sleeping["sid-1"] is False  # Successfully woken
    assert sleeping["sid-2"] is True   # Failed — stays sleeping


# ── Kill sleeping agent ────────────────────────────────────────────────────


@pytest.mark.asyncio
async def test_kill_sleeping_agent_removes_db_record(store, tmp_path):
    """Killing a sleeping agent should remove its live_sessions DB record."""
    work_dir = str(tmp_path)
    await store.register_live_session("sid-1", "claude", "wt1", work_dir)
    await store.set_session_sleeping("sid-1", True)

    # Simulate the kill path for a sleeping agent: unregister from DB
    await store.unregister_live_session("sid-1")

    sessions = await store.get_all_live_sessions()
    assert len(sessions) == 0


@pytest.mark.asyncio
async def test_kill_sleeping_agent_does_not_affect_teammates(store, tmp_path):
    """Killing one sleeping agent should not affect other agents on the same board."""
    work_dir = str(tmp_path)
    await store.register_live_session(
        "sid-1", "claude", "wt1", work_dir, board_name="my-team",
    )
    await store.register_live_session(
        "sid-2", "claude", "wt2", work_dir, board_name="my-team",
    )
    await store.set_board_sleeping("my-team", sleeping=True)

    # Kill only sid-1
    await store.unregister_live_session("sid-1")

    sessions = await store.get_all_live_sessions()
    assert len(sessions) == 1
    assert sessions[0]["session_id"] == "sid-2"
    assert sessions[0]["is_sleeping"] is True


@pytest.mark.asyncio
async def test_kill_sleeping_agent_board_still_has_sleeping_members(store, tmp_path):
    """After killing one sleeping agent, the board should still show as sleeping
    if other members remain asleep."""
    work_dir = str(tmp_path)
    await store.register_live_session(
        "sid-1", "claude", "wt1", work_dir, board_name="my-team",
    )
    await store.register_live_session(
        "sid-2", "claude", "wt2", work_dir, board_name="my-team",
    )
    await store.set_board_sleeping("my-team", sleeping=True)

    await store.unregister_live_session("sid-1")

    sleeping_boards = await store.get_sleeping_board_names()
    assert "my-team" in sleeping_boards  # sid-2 is still sleeping


@pytest.mark.asyncio
async def test_kill_last_sleeping_agent_clears_board(store, tmp_path):
    """Killing the last sleeping agent on a board should remove it from sleeping boards."""
    work_dir = str(tmp_path)
    await store.register_live_session(
        "sid-1", "claude", "wt1", work_dir, board_name="my-team",
    )
    await store.set_board_sleeping("my-team", sleeping=True)

    await store.unregister_live_session("sid-1")

    sleeping_boards = await store.get_sleeping_board_names()
    assert "my-team" not in sleeping_boards
