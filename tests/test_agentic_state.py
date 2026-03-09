"""Tests for the agentic state feature: events DB, API endpoints, and summary generation."""

import pytest
import pytest_asyncio
from httpx import ASGITransport, AsyncClient

from corral.web_server import app
from corral.store import CorralStore as SessionStore
from corral.hook_agentic_state import _make_summary, _make_detail_json, _truncate


# ── Fixtures ──────────────────────────────────────────────────────────────


@pytest_asyncio.fixture
async def tmp_store(tmp_path):
    """Create a SessionStore backed by a temp SQLite database."""
    db_path = tmp_path / "test.db"
    s = SessionStore(db_path=db_path)
    await s._get_conn()  # Force schema creation
    yield s
    await s.close()


@pytest_asyncio.fixture
async def client(tmp_store, monkeypatch):
    """AsyncClient wired to the real FastAPI app with a temp database."""
    import corral.web_server as ws
    ws._set_store(tmp_store)
    monkeypatch.setattr(ws, "store", tmp_store)
    transport = ASGITransport(app=app)
    async with AsyncClient(transport=transport, base_url="http://test") as c:
        yield c


# ── Database Tests ────────────────────────────────────────────────────────


@pytest.mark.asyncio
async def test_insert_and_list_events(tmp_store):
    """insert_agent_event + list_agent_events round-trip."""
    event = await tmp_store.insert_agent_event(
        agent_name="agent1",
        event_type="tool_use",
        summary="Read main.py",
        tool_name="Read",
        detail_json='{"file_path": "main.py"}',
    )
    assert event["id"] is not None
    assert event["summary"] == "Read main.py"
    assert event["tool_name"] == "Read"

    events = await tmp_store.list_agent_events("agent1")
    assert len(events) == 1
    assert events[0]["summary"] == "Read main.py"
    assert events[0]["event_type"] == "tool_use"


@pytest.mark.asyncio
async def test_events_newest_first(tmp_store):
    """Events are returned newest-first."""
    await tmp_store.insert_agent_event("agent1", "tool_use", "First", tool_name="Read")
    await tmp_store.insert_agent_event("agent1", "tool_use", "Second", tool_name="Write")
    await tmp_store.insert_agent_event("agent1", "tool_use", "Third", tool_name="Edit")

    events = await tmp_store.list_agent_events("agent1")
    assert len(events) == 3
    assert events[0]["summary"] == "Third"
    assert events[2]["summary"] == "First"


@pytest.mark.asyncio
async def test_event_counts_aggregation(tmp_store):
    """get_agent_event_counts groups by tool_name."""
    await tmp_store.insert_agent_event("agent1", "tool_use", "Read a", tool_name="Read")
    await tmp_store.insert_agent_event("agent1", "tool_use", "Read b", tool_name="Read")
    await tmp_store.insert_agent_event("agent1", "tool_use", "Edit a", tool_name="Edit")
    await tmp_store.insert_agent_event("agent1", "stop", "Stopped")

    counts = await tmp_store.get_agent_event_counts("agent1")
    counts_dict = {c["tool_name"]: c["count"] for c in counts}
    assert counts_dict["Read"] == 2
    assert counts_dict["Edit"] == 1
    # Stop event has no tool_name, so not in counts
    assert "None" not in counts_dict


@pytest.mark.asyncio
async def test_retention_limit(tmp_store):
    """Auto-prune keeps at most 500 events per agent."""
    for i in range(550):
        await tmp_store.insert_agent_event("agent1", "tool_use", f"Event {i}", tool_name="Read")

    events = await tmp_store.list_agent_events("agent1", limit=600)
    assert len(events) == 500


@pytest.mark.asyncio
async def test_clear_agent_events(tmp_store):
    """clear_agent_events removes all events for an agent."""
    await tmp_store.insert_agent_event("agent1", "tool_use", "Event 1", tool_name="Read")
    await tmp_store.insert_agent_event("agent1", "tool_use", "Event 2", tool_name="Write")
    await tmp_store.insert_agent_event("agent2", "tool_use", "Other agent", tool_name="Bash")

    await tmp_store.clear_agent_events("agent1")

    events1 = await tmp_store.list_agent_events("agent1")
    events2 = await tmp_store.list_agent_events("agent2")
    assert len(events1) == 0
    assert len(events2) == 1


@pytest.mark.asyncio
async def test_events_per_agent_isolation(tmp_store):
    """Events for different agents are isolated."""
    await tmp_store.insert_agent_event("agent_a", "tool_use", "A event", tool_name="Read")
    await tmp_store.insert_agent_event("agent_b", "tool_use", "B event", tool_name="Write")

    events_a = await tmp_store.list_agent_events("agent_a")
    events_b = await tmp_store.list_agent_events("agent_b")
    assert len(events_a) == 1
    assert events_a[0]["summary"] == "A event"
    assert len(events_b) == 1
    assert events_b[0]["summary"] == "B event"


# ── Status/Summary Tracking Tests ────────────────────────────────────────


@pytest.mark.asyncio
async def test_track_status_creates_event(tmp_store, monkeypatch):
    """_track_status_summary_events inserts a status event on change."""
    import corral.web_server as ws
    ws._set_store(tmp_store)
    ws._last_known.clear()

    await ws._track_status_summary_events("agent1", "Reading files", None)
    events = await tmp_store.list_agent_events("agent1")
    assert len(events) == 1
    assert events[0]["event_type"] == "status"
    assert events[0]["summary"] == "Reading files"


@pytest.mark.asyncio
async def test_track_summary_creates_event(tmp_store, monkeypatch):
    """_track_status_summary_events inserts a goal event on change."""
    import corral.web_server as ws
    ws._set_store(tmp_store)
    ws._last_known.clear()

    await ws._track_status_summary_events("agent1", None, "Implement auth feature")
    events = await tmp_store.list_agent_events("agent1")
    assert len(events) == 1
    assert events[0]["event_type"] == "goal"
    assert events[0]["summary"] == "Implement auth feature"


@pytest.mark.asyncio
async def test_track_no_duplicate_on_same_status(tmp_store, monkeypatch):
    """Repeated calls with the same status/summary do not create duplicates."""
    import corral.web_server as ws
    ws._set_store(tmp_store)
    ws._last_known.clear()

    await ws._track_status_summary_events("agent1", "Reading files", "Build auth")
    await ws._track_status_summary_events("agent1", "Reading files", "Build auth")
    await ws._track_status_summary_events("agent1", "Reading files", "Build auth")

    events = await tmp_store.list_agent_events("agent1")
    assert len(events) == 2  # one status + one goal, no duplicates


@pytest.mark.asyncio
async def test_track_new_status_creates_new_event(tmp_store, monkeypatch):
    """Changing status inserts a new event while same summary does not."""
    import corral.web_server as ws
    ws._set_store(tmp_store)
    ws._last_known.clear()

    await ws._track_status_summary_events("agent1", "Reading files", "Build auth")
    await ws._track_status_summary_events("agent1", "Writing tests", "Build auth")

    events = await tmp_store.list_agent_events("agent1")
    # 1 goal + 2 status = 3
    assert len(events) == 3
    assert events[0]["event_type"] == "status"
    assert events[0]["summary"] == "Writing tests"


@pytest.mark.asyncio
async def test_track_with_session_id(tmp_store, monkeypatch):
    """Events are tagged with the agent's current session_id."""
    import corral.web_server as ws
    ws._set_store(tmp_store)
    ws._last_known.clear()

    await tmp_store.set_agent_session_id("agent1", "sess-123")
    await ws._track_status_summary_events("agent1", "Reading files", None)

    events = await tmp_store.list_agent_events("agent1")
    assert events[0]["session_id"] == "sess-123"


# ── API Tests ─────────────────────────────────────────────────────────────


@pytest.mark.asyncio
async def test_create_event_via_api(client, tmp_store):
    """POST /api/sessions/live/{name}/events creates an event."""
    resp = await client.post(
        "/api/sessions/live/my_agent/events",
        json={
            "event_type": "tool_use",
            "tool_name": "Read",
            "summary": "Read main.py",
            "detail_json": '{"file_path": "main.py"}',
        },
    )
    assert resp.status_code == 200
    data = resp.json()
    assert data["summary"] == "Read main.py"
    assert data["tool_name"] == "Read"
    assert "id" in data

    events = await tmp_store.list_agent_events("my_agent")
    assert len(events) == 1


@pytest.mark.asyncio
async def test_list_events_via_api(client, tmp_store):
    """GET /api/sessions/live/{name}/events returns events."""
    await tmp_store.insert_agent_event("my_agent", "tool_use", "Event 1", tool_name="Read")
    await tmp_store.insert_agent_event("my_agent", "tool_use", "Event 2", tool_name="Write")

    resp = await client.get("/api/sessions/live/my_agent/events")
    assert resp.status_code == 200
    events = resp.json()
    assert len(events) == 2
    assert events[0]["summary"] == "Event 2"  # newest first


@pytest.mark.asyncio
async def test_event_counts_via_api(client, tmp_store):
    """GET /api/sessions/live/{name}/events/counts returns aggregated counts."""
    await tmp_store.insert_agent_event("my_agent", "tool_use", "R1", tool_name="Read")
    await tmp_store.insert_agent_event("my_agent", "tool_use", "R2", tool_name="Read")
    await tmp_store.insert_agent_event("my_agent", "tool_use", "W1", tool_name="Write")

    resp = await client.get("/api/sessions/live/my_agent/events/counts")
    assert resp.status_code == 200
    counts = resp.json()
    counts_dict = {c["tool_name"]: c["count"] for c in counts}
    assert counts_dict["Read"] == 2
    assert counts_dict["Write"] == 1


@pytest.mark.asyncio
async def test_clear_events_via_api(client, tmp_store):
    """DELETE /api/sessions/live/{name}/events clears all events."""
    await tmp_store.insert_agent_event("my_agent", "tool_use", "Event 1", tool_name="Read")

    resp = await client.delete("/api/sessions/live/my_agent/events")
    assert resp.status_code == 200
    assert resp.json()["ok"] is True

    events = await tmp_store.list_agent_events("my_agent")
    assert len(events) == 0


@pytest.mark.asyncio
async def test_create_event_missing_fields(client):
    """POST with missing required fields returns an error."""
    resp = await client.post(
        "/api/sessions/live/my_agent/events",
        json={"event_type": "tool_use"},
    )
    assert resp.status_code == 200
    assert "error" in resp.json()


# ── Summary Generation Tests ──────────────────────────────────────────────


class TestMakeSummary:
    """Test _make_summary for each tool type."""

    def test_read(self):
        s = _make_summary("Read", {"file_path": "/home/user/main.py"}, None)
        assert s == "Read main.py"

    def test_read_with_offset(self):
        s = _make_summary("Read", {"file_path": "/home/user/main.py", "offset": 10, "limit": 50}, None)
        assert s == "Read main.py (lines 10-60)"

    def test_write(self):
        s = _make_summary("Write", {"file_path": "/home/user/test.py"}, None)
        assert s == "Wrote test.py"

    def test_edit(self):
        s = _make_summary("Edit", {"file_path": "/home/user/config.json"}, None)
        assert s == "Edited config.json"

    def test_bash(self):
        s = _make_summary("Bash", {"command": "npm test"}, None)
        assert s == "Ran: npm test"

    def test_bash_long_command_truncated(self):
        cmd = "a" * 200
        s = _make_summary("Bash", {"command": cmd}, None)
        assert len(s) <= 90  # "Ran: " + 80 chars + "..."
        assert s.endswith("...")

    def test_grep(self):
        s = _make_summary("Grep", {"pattern": "TODO", "path": "/home/user/src"}, None)
        assert s == "Searched for 'TODO' in src/"

    def test_grep_no_path(self):
        s = _make_summary("Grep", {"pattern": "TODO"}, None)
        assert s == "Searched for 'TODO'"

    def test_glob(self):
        s = _make_summary("Glob", {"pattern": "**/*.py"}, None)
        assert s == "Glob: **/*.py"

    def test_web_fetch(self):
        s = _make_summary("WebFetch", {"url": "https://example.com/api"}, None)
        assert s == "Fetched https://example.com/api"

    def test_web_search(self):
        s = _make_summary("WebSearch", {"query": "python async tutorial"}, None)
        assert s == "Searched: python async tutorial"

    def test_task_create(self):
        s = _make_summary("TaskCreate", {"subject": "Fix bug in auth"}, None)
        assert s == "Created task: Fix bug in auth"

    def test_task_update(self):
        s = _make_summary("TaskUpdate", {"taskId": "5", "status": "completed"}, None)
        assert s == "Updated task #5 -> completed"

    def test_task_agent(self):
        s = _make_summary("Task", {"description": "explore code"}, None)
        assert s == "Launched subagent: explore code"

    def test_unknown_tool(self):
        s = _make_summary("SomeTool", {}, None)
        assert s == "Used SomeTool"

    def test_task_list(self):
        s = _make_summary("TaskList", {}, None)
        assert s == "Listed tasks"

    def test_task_get(self):
        s = _make_summary("TaskGet", {"taskId": "3"}, None)
        assert s == "Got task #3"


class TestMakeDetailJson:
    """Test _make_detail_json output."""

    def test_read_detail(self):
        d = _make_detail_json("Read", {"file_path": "/home/user/main.py"})
        assert '"file_path"' in d
        assert "main.py" in d

    def test_bash_detail(self):
        d = _make_detail_json("Bash", {"command": "npm test"})
        assert '"command"' in d

    def test_unknown_tool_returns_none(self):
        d = _make_detail_json("UnknownTool", {"foo": "bar"})
        assert d is None

    def test_empty_input_returns_none(self):
        d = _make_detail_json("Read", {})
        assert d is None


class TestTruncate:
    """Test _truncate helper."""

    def test_short_string_unchanged(self):
        assert _truncate("hello", 10) == "hello"

    def test_exact_length_unchanged(self):
        assert _truncate("hello", 5) == "hello"

    def test_long_string_truncated(self):
        result = _truncate("hello world", 8)
        assert result == "hello..."
        assert len(result) == 8
