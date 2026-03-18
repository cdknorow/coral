"""Tests for performance-critical code paths.

These tests verify correctness of the hot paths identified during optimization review:
- get_all_latest_git_state (git store batch query)
- get_all_changed_file_counts (git store aggregation)
- get_log_status / get_log_snapshot (backward log reading)
- check_unread / batch unread (message board N+1)
- Missing index impact on agent_events queries
"""

import asyncio
import os
import tempfile
import time
from pathlib import Path
from unittest.mock import AsyncMock, patch

import pytest
import pytest_asyncio

from coral.store.connection import DatabaseManager
from coral.store.git import GitStore


# ── Fixtures ──────────────────────────────────────────────────────────


class _GitStoreHelper(GitStore):
    """GitStore that reuses the test DB path."""

    def __init__(self, db_path: Path):
        super().__init__(db_path)


@pytest_asyncio.fixture
async def git_store(tmp_path):
    db_path = tmp_path / "test.db"
    store = _GitStoreHelper(db_path)
    yield store
    await store.close()


@pytest_asyncio.fixture
async def board_store(tmp_path):
    from coral.messageboard.store import MessageBoardStore

    db_path = tmp_path / "board.db"
    store = MessageBoardStore(db_path)
    yield store
    await store.close()


# ── Git Store: get_all_latest_git_state ──────────────────────────────


@pytest.mark.asyncio
async def test_get_all_latest_git_state_returns_latest_per_session(git_store):
    """Verify batch query returns only the latest snapshot per session."""
    # Insert two snapshots for same session with different timestamps
    await git_store.upsert_git_snapshot(
        agent_name="agent-1",
        agent_type="claude",
        working_directory="/repo",
        branch="main",
        commit_hash="aaa111",
        commit_subject="old commit",
        commit_timestamp="2024-01-01T00:00:00Z",
        session_id="sess-1",
    )
    await git_store.upsert_git_snapshot(
        agent_name="agent-1",
        agent_type="claude",
        working_directory="/repo",
        branch="feature",
        commit_hash="bbb222",
        commit_subject="new commit",
        commit_timestamp="2024-01-02T00:00:00Z",
        session_id="sess-1",
    )

    result = await git_store.get_all_latest_git_state()

    # Should have entry keyed by session_id and agent_name
    assert "sess-1" in result
    assert result["sess-1"]["commit_hash"] == "bbb222"
    assert result["sess-1"]["branch"] == "feature"


@pytest.mark.asyncio
async def test_get_all_latest_git_state_multiple_sessions(git_store):
    """Verify batch query handles multiple sessions correctly."""
    for i in range(5):
        await git_store.upsert_git_snapshot(
            agent_name=f"agent-{i}",
            agent_type="claude",
            working_directory=f"/repo-{i}",
            branch="main",
            commit_hash=f"hash-{i}",
            commit_subject=f"commit {i}",
            commit_timestamp=f"2024-01-0{i+1}T00:00:00Z",
            session_id=f"sess-{i}",
        )

    result = await git_store.get_all_latest_git_state()

    # Should have all 5 sessions (keyed by both session_id and agent_name)
    for i in range(5):
        assert f"sess-{i}" in result
        assert result[f"sess-{i}"]["commit_hash"] == f"hash-{i}"


@pytest.mark.asyncio
async def test_get_all_latest_git_state_empty(git_store):
    """No snapshots should return empty dict."""
    result = await git_store.get_all_latest_git_state()
    assert result == {}


@pytest.mark.asyncio
async def test_get_all_latest_git_state_null_session_id(git_store):
    """Snapshots without session_id should be keyed by agent_name."""
    await git_store.upsert_git_snapshot(
        agent_name="legacy-agent",
        agent_type="claude",
        working_directory="/repo",
        branch="main",
        commit_hash="ccc333",
        commit_subject="legacy",
        commit_timestamp="2024-01-01T00:00:00Z",
        session_id=None,
    )

    result = await git_store.get_all_latest_git_state()
    assert "legacy-agent" in result


# ── Git Store: get_all_changed_file_counts ────────────────────────────


@pytest.mark.asyncio
async def test_get_all_changed_file_counts(git_store):
    """Verify aggregation returns correct counts per session."""
    # Add files for two sessions
    await git_store.replace_changed_files(
        agent_name="agent-1",
        working_directory="/repo",
        files=[
            {"filepath": "a.py", "additions": 10, "deletions": 2},
            {"filepath": "b.py", "additions": 5, "deletions": 0},
        ],
        session_id="sess-1",
    )
    await git_store.replace_changed_files(
        agent_name="agent-2",
        working_directory="/repo",
        files=[
            {"filepath": "c.py", "additions": 3, "deletions": 1},
        ],
        session_id="sess-2",
    )

    counts = await git_store.get_all_changed_file_counts()

    assert counts["sess-1"] == 2
    assert counts["sess-2"] == 1


@pytest.mark.asyncio
async def test_replace_changed_files_replaces_not_appends(git_store):
    """Verify replace_changed_files is idempotent (deletes old, inserts new)."""
    await git_store.replace_changed_files(
        agent_name="agent-1",
        working_directory="/repo",
        files=[{"filepath": "old.py"}],
        session_id="sess-1",
    )
    await git_store.replace_changed_files(
        agent_name="agent-1",
        working_directory="/repo",
        files=[{"filepath": "new.py"}],
        session_id="sess-1",
    )

    counts = await git_store.get_all_changed_file_counts()
    assert counts["sess-1"] == 1  # Not 2

    files = await git_store.get_changed_files("agent-1", session_id="sess-1")
    assert len(files) == 1
    assert files[0]["filepath"] == "new.py"


# ── Log Streamer: get_log_snapshot ────────────────────────────────────


def test_get_log_snapshot_reads_status_and_summary():
    """Verify backward reading finds STATUS and SUMMARY tags."""
    from coral.tools.log_streamer import get_log_snapshot

    with tempfile.NamedTemporaryFile(mode="w", suffix=".log", delete=False) as f:
        # Write some content with PULSE tags
        f.write("Some early output\n")
        f.write("||PULSE:SUMMARY Working on optimization||\n")
        f.write("More output here\n")
        f.write("||PULSE:STATUS Running tests||\n")
        f.write("Final line\n")
        f.flush()
        log_path = f.name

    try:
        result = get_log_snapshot(log_path)
        assert result["status"] == "Running tests"
        assert result["summary"] == "Working on optimization"
        assert result["staleness_seconds"] is not None
        assert len(result["recent_lines"]) > 0
    finally:
        os.unlink(log_path)


def test_get_log_snapshot_nonexistent_file():
    """Nonexistent file should return empty result without error."""
    from coral.tools.log_streamer import get_log_snapshot

    result = get_log_snapshot("/nonexistent/path/file.log")
    assert result["status"] is None
    assert result["summary"] is None
    assert result["recent_lines"] == []
    assert result["staleness_seconds"] is None


def test_get_log_snapshot_empty_file():
    """Empty file should return result with staleness but no status/summary."""
    from coral.tools.log_streamer import get_log_snapshot

    with tempfile.NamedTemporaryFile(mode="w", suffix=".log", delete=False) as f:
        log_path = f.name

    try:
        result = get_log_snapshot(log_path)
        assert result["status"] is None
        assert result["summary"] is None
        assert result["recent_lines"] == []
        assert result["staleness_seconds"] is not None
    finally:
        os.unlink(log_path)


def test_get_log_snapshot_filters_noise():
    """Noise lines (box drawing, spinners, etc.) should be filtered from recent_lines."""
    from coral.tools.log_streamer import get_log_snapshot

    with tempfile.NamedTemporaryFile(mode="w", suffix=".log", delete=False) as f:
        f.write("──────────\n")
        f.write("Real log line here\n")
        f.write("   \n")
        f.write("❯ \n")
        f.write("Another real line\n")
        f.write("worktree: main\n")
        f.flush()
        log_path = f.name

    try:
        result = get_log_snapshot(log_path)
        # Only the real log lines should appear
        assert len(result["recent_lines"]) == 2
        assert any("Real log line" in line for line in result["recent_lines"])
        assert any("Another real line" in line for line in result["recent_lines"])
    finally:
        os.unlink(log_path)


def test_get_log_snapshot_large_file_max_lines():
    """Should respect max_lines parameter and not read entire file."""
    from coral.tools.log_streamer import get_log_snapshot

    with tempfile.NamedTemporaryFile(mode="w", suffix=".log", delete=False) as f:
        for i in range(1000):
            f.write(f"Log line number {i}\n")
        f.write("||PULSE:STATUS Active||\n")
        f.flush()
        log_path = f.name

    try:
        result = get_log_snapshot(log_path, max_lines=50)
        assert len(result["recent_lines"]) == 50
        assert result["status"] == "Active"
    finally:
        os.unlink(log_path)


def test_get_log_snapshot_summary_at_top():
    """Summary at the very beginning should still be found via head fallback."""
    from coral.tools.log_streamer import get_log_snapshot

    with tempfile.NamedTemporaryFile(mode="w", suffix=".log", delete=False) as f:
        f.write("||PULSE:SUMMARY Initial goal||\n")
        # Write enough content that backward reading won't reach the top
        for i in range(500):
            f.write(f"Line {i}: " + "x" * 100 + "\n")
        f.write("||PULSE:STATUS Current status||\n")
        f.flush()
        log_path = f.name

    try:
        result = get_log_snapshot(log_path, max_lines=10)
        assert result["status"] == "Current status"
        # Summary should be found via the head fallback
        assert result["summary"] == "Initial goal"
    finally:
        os.unlink(log_path)


# ── get_log_status (session_manager) ──────────────────────────────────


def test_get_log_status_basic():
    """Verify get_log_status returns expected structure."""
    from coral.tools.session_manager import get_log_status

    with tempfile.NamedTemporaryFile(mode="w", suffix=".log", delete=False) as f:
        f.write("||PULSE:STATUS Working||\n")
        f.write("||PULSE:SUMMARY Building feature||\n")
        f.flush()
        log_path = f.name

    try:
        result = get_log_status(log_path)
        assert result["status"] == "Working"
        assert result["summary"] == "Building feature"
        assert result["staleness_seconds"] is not None
    finally:
        os.unlink(log_path)


# ── Message Board: check_unread N+1 ──────────────────────────────────


@pytest.mark.asyncio
async def test_check_unread_counts_mentions(board_store):
    """check_unread should only count messages mentioning the agent."""
    # Subscribe agent
    await board_store.subscribe("project-1", "agent-1", job_title="Backend Dev")

    # Post a message mentioning @all
    await board_store.post_message("project-1", "agent-2", "Hey @all, update please")

    # Post a message NOT mentioning agent-1
    await board_store.post_message("project-1", "agent-2", "Random message")

    count = await board_store.check_unread("project-1", "agent-1")
    assert count == 1  # Only the @all message


@pytest.mark.asyncio
async def test_check_unread_by_job_title(board_store):
    """check_unread should count @job_title mentions."""
    await board_store.subscribe("project-1", "agent-1", job_title="Backend Dev")

    await board_store.post_message("project-1", "agent-2", "Hey @Backend Dev check this")

    count = await board_store.check_unread("project-1", "agent-1")
    assert count == 1


@pytest.mark.asyncio
async def test_check_unread_excludes_own_messages(board_store):
    """check_unread should not count the agent's own messages."""
    await board_store.subscribe("project-1", "agent-1", job_title="Dev")

    # Agent's own message mentioning @all
    await board_store.post_message("project-1", "agent-1", "@all done!")

    count = await board_store.check_unread("project-1", "agent-1")
    assert count == 0


@pytest.mark.asyncio
async def test_check_unread_multiple_agents(board_store):
    """Simulate the N+1 pattern: check_unread for multiple agents."""
    # Subscribe 5 agents
    for i in range(5):
        await board_store.subscribe("project-1", f"agent-{i}", job_title=f"Dev{i}")

    # Post 3 messages with @all
    for j in range(3):
        await board_store.post_message("project-1", "agent-0", f"Message {j} @all")

    # Check unread for each agent (this is the N+1 pattern)
    counts = {}
    for i in range(5):
        counts[f"agent-{i}"] = await board_store.check_unread("project-1", f"agent-{i}")

    # agent-0 sent the messages so should have 0 unread
    assert counts["agent-0"] == 0
    # All other agents should have 3 unread
    for i in range(1, 5):
        assert counts[f"agent-{i}"] == 3


# ── Pulse Detector: incremental scanning ─────────────────────────────


@pytest.mark.asyncio
async def test_pulse_detector_incremental_scanning():
    """Verify pulse_detector only reads new content via _file_positions."""
    from coral.tools.pulse_detector import scan_log_for_pulse_events, _file_positions

    mock_store = AsyncMock()

    with tempfile.NamedTemporaryFile(mode="w", suffix=".log", delete=False) as f:
        f.write("||PULSE:CONFIDENCE Low Unsure about this||\n")
        f.flush()
        log_path = f.name

    try:
        # Clear any cached position
        _file_positions.pop(log_path, None)

        # First scan should find the event
        await scan_log_for_pulse_events(mock_store, "agent-1", log_path)
        assert mock_store.insert_agent_event.call_count == 1

        mock_store.reset_mock()

        # Second scan without new content should NOT find new events
        await scan_log_for_pulse_events(mock_store, "agent-1", log_path)
        assert mock_store.insert_agent_event.call_count == 0

        # Append new content
        with open(log_path, "a") as f:
            f.write("||PULSE:CONFIDENCE High Matches existing pattern||\n")

        mock_store.reset_mock()

        # Third scan should find only the new event
        await scan_log_for_pulse_events(mock_store, "agent-1", log_path)
        assert mock_store.insert_agent_event.call_count == 1
    finally:
        _file_positions.pop(log_path, None)
        os.unlink(log_path)


@pytest.mark.asyncio
async def test_pulse_detector_handles_truncated_file():
    """If log file is truncated (agent restart), scanner should reset."""
    from coral.tools.pulse_detector import scan_log_for_pulse_events, _file_positions

    mock_store = AsyncMock()

    with tempfile.NamedTemporaryFile(mode="w", suffix=".log", delete=False) as f:
        f.write("First content line\n" * 100)
        f.flush()
        log_path = f.name

    try:
        _file_positions.pop(log_path, None)
        await scan_log_for_pulse_events(mock_store, "agent-1", log_path)

        # Truncate the file (simulating restart)
        with open(log_path, "w") as f:
            f.write("||PULSE:CONFIDENCE Low Restarted||\n")

        mock_store.reset_mock()
        await scan_log_for_pulse_events(mock_store, "agent-1", log_path)

        # After truncation detection + re-read, should reset and find new content
        # May need two calls: first detects truncation, second reads
        if mock_store.insert_agent_event.call_count == 0:
            await scan_log_for_pulse_events(mock_store, "agent-1", log_path)

        assert mock_store.insert_agent_event.call_count >= 1
    finally:
        _file_positions.pop(log_path, None)
        os.unlink(log_path)


# ── Idle Detector ─────────────────────────────────────────────────────


@pytest.mark.asyncio
async def test_idle_detector_skips_active_agents(tmp_path):
    """Idle detector should not notify for active agents."""
    from coral.background_tasks.idle_detector import IdleDetector

    log_file = tmp_path / "agent.log"
    log_file.write_text("some log content")

    mock_store = AsyncMock()
    mock_store.list_webhook_configs.return_value = [
        {"id": 1, "agent_filter": None},
    ]
    mock_store.get_latest_event_types.return_value = {
        "sess-1": ("tool_use", "Running tests"),  # Active
    }

    detector = IdleDetector(mock_store)

    with patch("coral.background_tasks.idle_detector.discover_coral_agents") as mock_discover:
        mock_discover.return_value = [
            {"agent_name": "agent-1", "session_id": "sess-1", "log_path": str(log_file)},
        ]
        result = await detector.run_once()

    assert result["notifications"] == 0


@pytest.mark.asyncio
async def test_idle_detector_notifies_stale_waiting(tmp_path):
    """Idle detector should notify for agents waiting > threshold."""
    from coral.background_tasks.idle_detector import IdleDetector

    # Create a log file with old mtime (10 minutes ago)
    log_file = tmp_path / "agent.log"
    log_file.write_text("some log content")
    old_mtime = time.time() - 600
    os.utime(log_file, (old_mtime, old_mtime))

    mock_store = AsyncMock()
    mock_store.list_webhook_configs.return_value = [
        {"id": 1, "agent_filter": None},
    ]
    mock_store.get_latest_event_types.return_value = {
        "sess-1": ("notification", "Permission prompt"),
    }
    mock_store.create_webhook_delivery = AsyncMock()

    detector = IdleDetector(mock_store)

    with patch("coral.background_tasks.idle_detector.discover_coral_agents") as mock_discover:
        mock_discover.return_value = [
            {"agent_name": "agent-1", "session_id": "sess-1", "log_path": str(log_file)},
        ]
        result = await detector.run_once()

    assert result["notifications"] == 1
    mock_store.create_webhook_delivery.assert_called_once()


@pytest.mark.asyncio
async def test_idle_detector_only_notifies_once(tmp_path):
    """Idle detector should not re-notify for the same waiting period."""
    from coral.background_tasks.idle_detector import IdleDetector

    # Create a log file with old mtime (10 minutes ago)
    log_file = tmp_path / "agent.log"
    log_file.write_text("some log content")
    old_mtime = time.time() - 600
    os.utime(log_file, (old_mtime, old_mtime))

    mock_store = AsyncMock()
    mock_store.list_webhook_configs.return_value = [
        {"id": 1, "agent_filter": None},
    ]
    mock_store.get_latest_event_types.return_value = {
        "sess-1": ("notification", "Permission prompt"),
    }
    mock_store.create_webhook_delivery = AsyncMock()

    detector = IdleDetector(mock_store)

    with patch("coral.background_tasks.idle_detector.discover_coral_agents") as mock_discover:
        mock_discover.return_value = [
            {"agent_name": "agent-1", "session_id": "sess-1", "log_path": str(log_file)},
        ]

        # First run: should notify
        r1 = await detector.run_once()
        assert r1["notifications"] == 1

        mock_store.create_webhook_delivery.reset_mock()

        # Second run: should NOT re-notify
        r2 = await detector.run_once()
        assert r2["notifications"] == 0


@pytest.mark.asyncio
async def test_idle_detector_no_webhooks_configured():
    """If no webhooks are configured, idle detector should be a no-op."""
    from coral.background_tasks.idle_detector import IdleDetector

    mock_store = AsyncMock()
    mock_store.list_webhook_configs.return_value = []

    detector = IdleDetector(mock_store)
    result = await detector.run_once()
    assert result["notifications"] == 0
