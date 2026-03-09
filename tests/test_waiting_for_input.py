"""Tests for the waiting-for-input detection feature."""

import pytest
import pytest_asyncio

from corral.store import CorralStore as SessionStore


@pytest_asyncio.fixture
async def store(tmp_path):
    """Create a SessionStore backed by a temp DB and close it after the test."""
    s = SessionStore(db_path=tmp_path / "test.db")
    yield s
    await s.close()


@pytest.mark.asyncio
async def test_empty_session_ids(store):
    """Empty input returns empty dict."""
    result = await store.get_latest_event_types([])
    assert result == {}


@pytest.mark.asyncio
async def test_nonexistent_session(store):
    """Nonexistent session_id returns empty dict."""
    result = await store.get_latest_event_types(["nonexistent"])
    assert result == {}


@pytest.mark.asyncio
async def test_stop_event_detected(store):
    """A stop event as the latest should be returned."""
    await store.insert_agent_event("agent1", "tool_use", "Used Read", tool_name="Read", session_id="sess-1")
    await store.insert_agent_event("agent1", "stop", "Agent stopped: end_turn", session_id="sess-1")

    result = await store.get_latest_event_types(["sess-1"])
    assert result["sess-1"] == "stop"


@pytest.mark.asyncio
async def test_tool_use_clears_waiting(store):
    """A tool_use event after a stop should show tool_use, not stop."""
    await store.insert_agent_event("agent1", "stop", "Agent stopped: end_turn", session_id="sess-1")
    await store.insert_agent_event("agent1", "tool_use", "Used Read", tool_name="Read", session_id="sess-1")

    result = await store.get_latest_event_types(["sess-1"])
    assert result["sess-1"] == "tool_use"


@pytest.mark.asyncio
async def test_status_goal_events_ignored(store):
    """Status and goal events should not affect the waiting detection."""
    await store.insert_agent_event("agent1", "stop", "Agent stopped: end_turn", session_id="sess-1")
    # Status/goal events come in after stop but shouldn't change the result
    await store.insert_agent_event("agent1", "status", "Some status", session_id="sess-1")
    await store.insert_agent_event("agent1", "goal", "Some goal", session_id="sess-1")

    result = await store.get_latest_event_types(["sess-1"])
    assert result["sess-1"] == "stop"


@pytest.mark.asyncio
async def test_multiple_sessions(store):
    """Should return correct latest event for multiple sessions."""
    # sess-1: waiting (last event is stop)
    await store.insert_agent_event("agent1", "tool_use", "Used Read", tool_name="Read", session_id="sess-1")
    await store.insert_agent_event("agent1", "stop", "Agent stopped: end_turn", session_id="sess-1")

    # sess-2: active (last event is tool_use)
    await store.insert_agent_event("agent2", "stop", "Agent stopped: end_turn", session_id="sess-2")
    await store.insert_agent_event("agent2", "tool_use", "Used Bash", tool_name="Bash", session_id="sess-2")

    result = await store.get_latest_event_types(["sess-1", "sess-2"])
    assert result["sess-1"] == "stop"
    assert result["sess-2"] == "tool_use"


@pytest.mark.asyncio
async def test_notification_event(store):
    """A notification event should also be returned as latest."""
    await store.insert_agent_event("agent1", "stop", "Agent stopped", session_id="sess-1")
    await store.insert_agent_event("agent1", "notification", "Some notification", session_id="sess-1")

    result = await store.get_latest_event_types(["sess-1"])
    assert result["sess-1"] == "notification"
